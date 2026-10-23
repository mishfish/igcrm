package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"igcrm/internal/shipping"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct{ db *sql.DB }

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetConnMaxIdleTime(5 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if err = db.PingContext(pingCtx); err == nil {
			return &Store{db: db}, nil
		}
		select {
		case <-pingCtx.Done():
			return nil, fmt.Errorf("postgres unreachable: %w", err)
		case <-time.After(time.Second):
		}
	}
}

func (s *Store) Close() error { return s.db.Close() }

// Migrate застосовує migrations/*.sql по порядку, кожну в окремій транзакції.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// захист від паралельного запуску кількох інстансів
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(727001)`); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(727001)`)

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		var exists bool
		if err := conn.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		body, err := migrationsFS.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ---------- messages ----------

type Message struct {
	MID         string
	IGSID       string // співрозмовник (клієнт), не твій акаунт
	Direction   string // in | out
	Text        string
	Attachments json.RawMessage
	SentAt      time.Time
	Source      string
	Raw         json.RawMessage
}

// SaveMessage створює контакт за потреби, зберігає повідомлення (ідемпотентно за mid)
// і ставить контакт у чергу на екстракцію через debounce.
// Повертає inserted=false, якщо повідомлення вже було (ретрай від Meta).
func (s *Store) SaveMessage(ctx context.Context, m Message, debounce time.Duration) (inserted bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var contactID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO contacts (igsid) VALUES ($1)
		ON CONFLICT (igsid) DO NOTHING
		RETURNING id`, m.IGSID).Scan(&contactID)
	if errors.Is(err, sql.ErrNoRows) {
		// контакт уже існує: без DO NOTHING ... RETURNING нічого не повернеться,
		// а зайвий DO UPDATE на кожне повідомлення бив би по contacts_touch тригеру
		err = tx.QueryRowContext(ctx, `SELECT id FROM contacts WHERE igsid = $1`, m.IGSID).Scan(&contactID)
	}
	if err != nil {
		return false, fmt.Errorf("upsert contact: %w", err)
	}

	source := m.Source
	if source == "" {
		source = "webhook"
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO messages (mid, contact_id, direction, text, attachments, sent_at, source, raw)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8)
		ON CONFLICT (mid) DO NOTHING`,
		m.MID, contactID, m.Direction, m.Text, nullJSON(m.Attachments), m.SentAt, source, nullJSON(m.Raw))
	if err != nil {
		return false, fmt.Errorf("insert message: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, tx.Commit()
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE contacts SET
			last_message_at = GREATEST(COALESCE(last_message_at, $2), $2),
			extract_due_at  = CASE WHEN auto_update THEN now() + make_interval(secs => $3) ELSE extract_due_at END
		WHERE id = $1`, contactID, m.SentAt, debounce.Seconds()); err != nil {
		return false, fmt.Errorf("touch contact: %w", err)
	}
	return true, tx.Commit()
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return []byte(b)
}

// ---------- extraction queue ----------

// ClaimDue забирає контакти, у яких минув debounce, і знімає їх з черги.
func (s *Store) ClaimDue(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		UPDATE contacts SET extract_due_at = NULL
		WHERE id IN (
			SELECT id FROM contacts
			WHERE extract_due_at <= now() AND auto_update
			ORDER BY extract_due_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED)
		RETURNING id`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Reschedule повертає контакт у чергу (після помилки LLM), якщо його вже не поставили знову.
func (s *Store) Reschedule(ctx context.Context, id int64, after time.Duration) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE contacts SET extract_due_at = now() + make_interval(secs => $2)
		WHERE id = $1 AND extract_due_at IS NULL`, id, after.Seconds())
	return err
}

// Fields — поля картки, які заповнює екстракція. Порядок важливий для стабільного вигляду.
var Fields = []string{"full_name", "phone", "city", "np_branch", "sizes", "last_interest"}

type Contact struct {
	ID         int64
	Username   string
	AutoUpdate bool
	Values     map[string]string // поточні значення Fields
}

func (s *Store) Contact(ctx context.Context, id int64) (Contact, error) {
	c := Contact{ID: id, Values: map[string]string{}}
	cols := make([]any, len(Fields))
	vals := make([]sql.NullString, len(Fields))
	for i := range vals {
		cols[i] = &vals[i]
	}
	var username sql.NullString
	q := `SELECT username, auto_update, ` + strings.Join(Fields, ", ") + ` FROM contacts WHERE id = $1`
	if err := s.db.QueryRowContext(ctx, q, id).Scan(append([]any{&username, &c.AutoUpdate}, cols...)...); err != nil {
		return c, err
	}
	c.Username = username.String
	for i, f := range Fields {
		c.Values[f] = vals[i].String
	}
	return c, nil
}

type ChatLine struct {
	Direction string
	Text      string
	SentAt    time.Time
}

// RecentMessages — останні n текстових повідомлень у хронологічному порядку.
func (s *Store) RecentMessages(ctx context.Context, contactID int64, n int) ([]ChatLine, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT direction, text, sent_at FROM (
			SELECT direction, text, sent_at FROM messages
			WHERE contact_id = $1 AND text IS NOT NULL
			ORDER BY sent_at DESC LIMIT $2) t
		ORDER BY sent_at`, contactID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatLine
	for rows.Next() {
		var l ChatLine
		if err := rows.Scan(&l.Direction, &l.Text, &l.SentAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

type Change struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// ApplyExtraction записує змінені поля (лише з білого списку Fields) і журнал.
// Якщо поки йшов запит до LLM ти вимкнув auto_update — поля не чіпаються.
func (s *Store) ApplyExtraction(ctx context.Context, id int64, model string, result json.RawMessage, changes map[string]Change) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	applied := map[string]Change{}
	if len(changes) > 0 {
		var sets []string
		args := []any{id}
		for _, f := range Fields {
			ch, ok := changes[f]
			if !ok {
				continue
			}
			args = append(args, ch.New)
			sets = append(sets, fmt.Sprintf("%s = $%d", f, len(args)))
		}
		q := `UPDATE contacts SET ` + strings.Join(sets, ", ") + `, needs_review = true WHERE id = $1 AND auto_update`
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("update contact: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			applied = changes
		}
	}
	changesJSON, _ := json.Marshal(applied)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO extractions (contact_id, model, result, changes) VALUES ($1, $2, $3, $4)`,
		id, model, []byte(result), changesJSON); err != nil {
		return fmt.Errorf("insert extraction: %w", err)
	}
	return tx.Commit()
}

// ---------- orders ----------

// CreateOrderIfNone створює замовлення з позначкою auto_created, якщо у контакта
// зараз немає жодного відкритого (new/awaiting_payment) — щоб той самий сигнал від LLM
// не плодив дублікати на кожному debounce-циклі, поки клієнт ще щось дописує.
// Дані отримувача (ПІБ/телефон/місто/відділення) підтягуються тригером orders_fill з картки.
func (s *Store) CreateOrderIfNone(ctx context.Context, contactID int64, items string, amount *float64, paymentType string) (orderID int64, created bool, err error) {
	var amt any
	if amount != nil {
		amt = *amount
	}
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO orders (contact_id, items, amount, payment_type, auto_created)
		SELECT $1, NULLIF($2, ''), $3, NULLIF($4, ''), true
		WHERE NOT EXISTS (
			SELECT 1 FROM orders WHERE contact_id = $1 AND status IN ('new', 'awaiting_payment'))
		RETURNING id`, contactID, items, amt, paymentType).Scan(&orderID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return orderID, true, nil
}

// OrderForShipping читає дані замовлення, потрібні для створення ТТН у Новій Пошті.
func (s *Store) OrderForShipping(ctx context.Context, id int64) (shipping.Order, error) {
	var o shipping.Order
	var fullName, phone, city, npBranch, paymentType sql.NullString
	var amount sql.NullFloat64
	var ttn sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT full_name, phone, city, np_branch, amount, payment_type, ttn
		FROM orders WHERE id = $1`, id).
		Scan(&fullName, &phone, &city, &npBranch, &amount, &paymentType, &ttn)
	if errors.Is(err, sql.ErrNoRows) {
		return o, fmt.Errorf("order %d not found", id)
	}
	if err != nil {
		return o, err
	}
	o = shipping.Order{
		ID:          id,
		FullName:    fullName.String,
		Phone:       phone.String,
		City:        city.String,
		NPBranch:    npBranch.String,
		PaymentType: paymentType.String,
	}
	if amount.Valid {
		o.Amount = &amount.Float64
	}
	if ttn.Valid && ttn.String != "" {
		o.TTN = &ttn.String
	}
	return o, nil
}

// SetOrderTTN записує номер ТТН і переводить замовлення в статус "shipped".
// Якщо в замовлення вже є ТТН (повторний клік) — нічого не змінює й повертає помилку.
func (s *Store) SetOrderTTN(ctx context.Context, id int64, ttn string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE orders SET ttn = $2, status = 'shipped' WHERE id = $1 AND ttn IS NULL`, id, ttn)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("order %d not found or already has a ttn", id)
	}
	return nil
}

// ---------- profiles ----------

type ProfileJob struct {
	ID    int64
	IGSID string
}

func (s *Store) ContactsWithoutProfile(ctx context.Context, limit int) ([]ProfileJob, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, igsid FROM contacts
		WHERE profile_fetched_at IS NULL AND profile_attempts < 5
		ORDER BY id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProfileJob
	for rows.Next() {
		var j ProfileJob
		if err := rows.Scan(&j.ID, &j.IGSID); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) SetProfile(ctx context.Context, id int64, username, name string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE contacts SET username = NULLIF($2, ''), display_name = NULLIF($3, ''), profile_fetched_at = now()
		WHERE id = $1`, id, username, name)
	return err
}

func (s *Store) ProfileFailed(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE contacts SET profile_attempts = profile_attempts + 1 WHERE id = $1`, id)
	return err
}

// ---------- token ----------

type Token struct {
	AccessToken string
	ExpiresAt   *time.Time
}

var ErrNoToken = errors.New("no instagram token stored")

// SeedToken кладе токен з env у БД при першому запуску (або завжди, якщо force).
func (s *Store) SeedToken(ctx context.Context, token string, force bool) error {
	if token == "" {
		return nil
	}
	q := `INSERT INTO ig_token (id, access_token) VALUES (1, $1) ON CONFLICT (id) DO NOTHING`
	if force {
		q = `INSERT INTO ig_token (id, access_token) VALUES (1, $1)
		     ON CONFLICT (id) DO UPDATE SET access_token = EXCLUDED.access_token, expires_at = NULL, updated_at = now()`
	}
	_, err := s.db.ExecContext(ctx, q, token)
	return err
}

func (s *Store) Token(ctx context.Context) (Token, error) {
	var t Token
	var exp sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT access_token, expires_at FROM ig_token WHERE id = 1`).Scan(&t.AccessToken, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNoToken
	}
	if exp.Valid {
		t.ExpiresAt = &exp.Time
	}
	return t, err
}

func (s *Store) SaveToken(ctx context.Context, token string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE ig_token SET access_token = $1, expires_at = $2, refreshed_at = now(), updated_at = now()
		WHERE id = 1`, token, expiresAt)
	return err
}
