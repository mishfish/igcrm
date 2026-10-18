package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"igcrm/internal/store"
)

const SystemPrompt = `Ти витягуєш дані клієнта з переписки інтернет-магазину в Instagram Direct.
"Клієнт" — покупець, "Магазин" — продавець. Поверни ТІЛЬКИ JSON-об'єкт з полями:

{
  "full_name":     ПІБ отримувача посилки,
  "phone":         телефон отримувача,
  "city":          місто/населений пункт доставки,
  "np_branch":     відділення або поштомат Нової Пошти, формат "Відділення №45" або "Поштомат №1234",
  "sizes":         розміри клієнта, коротко, напр. "одяг M, взуття 38",
  "last_interest": що клієнт хоче купити/замовив останнім, коротко, до 80 символів,
  "order_ready":   true / false,
  "order_amount":  сума замовлення в грн, якщо прямо названа в переписці, інакше null,
  "payment_type":  "full_prepay" (повна передоплата) / "partial_prepay" (часткова, напр. 50/50) /
                    "cod" (оплата при отриманні, накладений платіж) / null, якщо не обговорювали
}

Правила:
- Лише те, що прямо написано в переписці. Нічого не вигадуй і не здогадуйся.
- Якщо значення немає — null.
- Якщо значення змінювалось (нова адреса, інший розмір) — бери найсвіжіше.
- Дані можуть бути в повідомленнях і клієнта, і магазину (магазин іноді перепитує "Київ, 45 відділення?" — "так").
- Не плутай ПІБ отримувача з ім'ям продавця.
- Мова значень — як у переписці, без перекладу.
- order_ready = true лише якщо клієнт явно підтвердив, що бере товар ("так, беру",
  "оформляйте", "замовляю") — не просто цікавиться, питає ціну чи наявність.`

// BuildUserPrompt — поточна картка + останні повідомлення.
func BuildUserPrompt(current map[string]string, lines []store.ChatLine) string {
	var b strings.Builder
	b.WriteString("Поточна картка клієнта (може бути неповна або застаріла):\n")
	for _, f := range store.Fields {
		v := current[f]
		if v == "" {
			v = "—"
		}
		fmt.Fprintf(&b, "- %s: %s\n", f, v)
	}
	b.WriteString("\nПереписка (стара → нова):\n")
	for _, l := range lines {
		who := "Клієнт"
		if l.Direction == "out" {
			who = "Магазин"
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", l.SentAt.Local().Format("2006-01-02 15:04"), who, strings.TrimSpace(l.Text))
	}
	b.WriteString("\nПоверни JSON.")
	return b.String()
}

var (
	spaces    = regexp.MustCompile(`\s+`)
	nonDigits = regexp.MustCompile(`\D`)
	branchRe  = regexp.MustCompile(`(?i)^(?:(відділення|отделение|відд?\.?|пункт)|(поштомат|почтомат|пштм))?\s*(?:№|n|#|no\.?)?\s*(\d{1,5})$`)
	emptyVals = map[string]bool{"": true, "null": true, "none": true, "-": true, "—": true, "невідомо": true, "немає": true, "n/a": true}
)

// Normalize чистить значення поля; "" = немає значення.
func Normalize(field, v string) string {
	v = strings.TrimSpace(spaces.ReplaceAllString(v, " "))
	if emptyVals[strings.ToLower(v)] {
		return ""
	}
	switch field {
	case "phone":
		return NormalizePhone(v)
	case "np_branch":
		return normalizeBranch(v)
	}
	return v
}

// NormalizePhone приводить українські номери до +380XXXXXXXXX; інші лишає як є.
func NormalizePhone(v string) string {
	d := nonDigits.ReplaceAllString(v, "")
	switch {
	case len(d) == 12 && strings.HasPrefix(d, "380"):
		return "+" + d
	case len(d) == 10 && strings.HasPrefix(d, "0"):
		return "+38" + d
	case strings.HasPrefix(strings.TrimSpace(v), "+") && len(d) >= 8:
		return "+" + d
	}
	return v
}

func normalizeBranch(v string) string {
	m := branchRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return v // "Відділення №45 (до 30 кг)", адреса тощо — як є
	}
	if m[2] != "" {
		return "Поштомат №" + m[3]
	}
	return "Відділення №" + m[3]
}

// paymentTypes — дозволені значення payment_type (мають співпадати з CHECK у БД).
var paymentTypes = map[string]bool{"full_prepay": true, "partial_prepay": true, "cod": true}

// OrderSignal читає з результату LLM ознаку готовності замовлення, суму (якщо названа)
// і тип оплати (якщо модель повернула щось поза білим списком — ігноруємо, щоб не впасти на CHECK у БД).
func OrderSignal(result json.RawMessage) (ready bool, amount *float64, paymentType string) {
	var r struct {
		OrderReady  bool     `json:"order_ready"`
		OrderAmount *float64 `json:"order_amount"`
		PaymentType string   `json:"payment_type"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return false, nil, ""
	}
	if !paymentTypes[r.PaymentType] {
		r.PaymentType = ""
	}
	return r.OrderReady, r.OrderAmount, r.PaymentType
}

// Diff порівнює результат LLM з карткою; повертає лише реальні зміни.
// Порожні значення від моделі ніколи не стирають наявні дані.
func Diff(current map[string]string, result json.RawMessage) (map[string]store.Change, error) {
	var r map[string]any
	if err := json.Unmarshal(result, &r); err != nil {
		return nil, err
	}
	changes := map[string]store.Change{}
	for _, f := range store.Fields {
		raw, ok := r[f]
		if !ok || raw == nil {
			continue
		}
		var s string
		switch v := raw.(type) {
		case string:
			s = v
		case float64:
			s = fmt.Sprintf("%.0f", v)
		default:
			continue
		}
		nv := Normalize(f, s)
		if nv == "" {
			continue
		}
		old := current[f]
		if strings.EqualFold(Normalize(f, old), nv) {
			continue
		}
		changes[f] = store.Change{Old: old, New: nv}
	}
	return changes, nil
}

// ---------- worker ----------

type Store interface {
	ClaimDue(ctx context.Context, limit int) ([]int64, error)
	Reschedule(ctx context.Context, id int64, after time.Duration) error
	Contact(ctx context.Context, id int64) (store.Contact, error)
	RecentMessages(ctx context.Context, contactID int64, n int) ([]store.ChatLine, error)
	ApplyExtraction(ctx context.Context, id int64, model string, result json.RawMessage, changes map[string]store.Change) error
	CreateOrderIfNone(ctx context.Context, contactID int64, items string, amount *float64, paymentType string) (orderID int64, created bool, err error)
}

type Completer interface {
	Complete(ctx context.Context, system, user string) (json.RawMessage, error)
}

// Notifier — мінімальний інтерфейс, щоб не тягнути internal/notify в тести Worker.
type Notifier interface {
	Alert(ctx context.Context, text string)
}

type Worker struct {
	Store   Store
	LLM     Completer
	Model   string
	History int
	Log     *slog.Logger
	Notify  Notifier // nil — без алертів (напр. у тестах)
}

func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		w.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (w *Worker) Tick(ctx context.Context) {
	ids, err := w.Store.ClaimDue(ctx, 5)
	if err != nil {
		w.Log.Error("extract claim", "err", err)
		return
	}
	for _, id := range ids {
		if err := w.Process(ctx, id); err != nil {
			w.Log.Error("extract", "contact", id, "err", err)
			// окремий, незалежний від ctx контекст: якщо Process провалився через
			// скасування ctx (напр. SIGTERM), Reschedule все одно має встигнути
			// звільнити контакт, інакше він лишиться без дати екстракції назавжди
			rescheduleCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := w.Store.Reschedule(rescheduleCtx, id, 10*time.Minute); err != nil {
				w.Log.Error("extract reschedule", "contact", id, "err", err)
			}
			cancel()
		}
	}
}

func (w *Worker) Process(ctx context.Context, id int64) error {
	c, err := w.Store.Contact(ctx, id)
	if err != nil {
		return err
	}
	if !c.AutoUpdate {
		return nil
	}
	lines, err := w.Store.RecentMessages(ctx, id, w.History)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	result, err := w.LLM.Complete(callCtx, SystemPrompt, BuildUserPrompt(c.Values, lines))
	if err != nil {
		return err
	}
	changes, err := Diff(c.Values, result)
	if err != nil {
		return err
	}
	if err := w.Store.ApplyExtraction(ctx, id, w.Model, result, changes); err != nil {
		return err
	}
	if len(changes) > 0 {
		fields := make([]string, 0, len(changes))
		for f := range changes {
			fields = append(fields, f)
		}
		w.Log.Info("contact updated", "contact", id, "username", c.Username, "fields", fields)
	}

	if ready, amount, paymentType := OrderSignal(result); ready {
		items := c.Values["last_interest"]
		if ch, ok := changes["last_interest"]; ok {
			items = ch.New
		}
		if items != "" {
			orderID, created, err := w.Store.CreateOrderIfNone(ctx, id, items, amount, paymentType)
			if err != nil {
				w.Log.Error("order auto-create", "contact", id, "err", err)
			} else if created {
				who := c.Username
				if who == "" {
					who = fmt.Sprintf("контакт %d", id)
				}
				w.Log.Info("order auto-created", "contact", id, "username", c.Username, "order", orderID, "items", items)
				if w.Notify != nil {
					msg := fmt.Sprintf("нове замовлення #%d від %s: %s", orderID, who, items)
					if amount != nil {
						msg += fmt.Sprintf(" (%.0f грн)", *amount)
					}
					w.Notify.Alert(ctx, msg)
				}
			}
		}
	}
	return nil
}
