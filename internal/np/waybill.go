package np

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// WaybillRequest — дані, потрібні для створення однієї ТТН.
type WaybillRequest struct {
	SenderCityName    string
	SenderWarehouseNo string

	RecipientFullName    string
	RecipientPhone       string
	RecipientCityName    string
	RecipientWarehouseNo string

	Weight        float64 // кг
	DeclaredValue float64 // оголошена вартість, грн

	// CashOnDelivery > 0 — сума "наложки" (НП сама збирає гроші з отримувача при видачі).
	CashOnDelivery float64

	Description string // опис вмісту; якщо порожньо — типовий текст
}

type contactPerson struct {
	Ref    string `json:"Ref"`
	Phones string `json:"Phones"`
}

// resolveSender знаходить контрагента-відправника (прив'язаного до API-ключа) і його контактну особу.
func (c *Client) resolveSender(ctx context.Context) (ref, contactRef, phone string, err error) {
	var counterparties []struct {
		Ref string `json:"Ref"`
	}
	if err = c.call(ctx, "Counterparty", "getCounterparties", map[string]any{
		"CounterpartyProperty": "Sender",
	}, &counterparties); err != nil {
		return "", "", "", err
	}
	if len(counterparties) == 0 {
		return "", "", "", fmt.Errorf("np: на цьому API-ключі не зареєстровано відправника")
	}
	ref = counterparties[0].Ref

	contact, err := c.firstContactPerson(ctx, ref)
	if err != nil {
		return "", "", "", err
	}
	return ref, contact.Ref, contact.Phones, nil
}

func (c *Client) firstContactPerson(ctx context.Context, counterpartyRef string) (contactPerson, error) {
	var contacts []contactPerson
	if err := c.call(ctx, "Counterparty", "getCounterpartyContactPersons", map[string]any{
		"Ref": counterpartyRef,
	}, &contacts); err != nil {
		return contactPerson{}, err
	}
	if len(contacts) == 0 {
		return contactPerson{}, fmt.Errorf("np: контактну особу для %s не знайдено", counterpartyRef)
	}
	return contacts[0], nil
}

// ensureRecipient створює (або повертає вже наявного — НП дедублює за телефоном) отримувача-фізособу.
func (c *Client) ensureRecipient(ctx context.Context, fullName, phone string) (string, error) {
	first, last, middle := splitFullName(fullName)
	var created []struct {
		Ref string `json:"Ref"`
	}
	props := map[string]any{
		"CounterpartyType":     "PrivatePerson",
		"CounterpartyProperty": "Recipient",
		"FirstName":            first,
		"LastName":             last,
		"Phone":                phone,
	}
	if middle != "" {
		props["MiddleName"] = middle
	}
	if err := c.call(ctx, "Counterparty", "save", props, &created); err != nil {
		return "", err
	}
	if len(created) == 0 {
		return "", fmt.Errorf("np: не вдалося створити отримувача")
	}
	return created[0].Ref, nil
}

// CreateWaybill створює ТТН і повертає її номер (ttn) та внутрішній Ref документа
// (docRef — потрібен лише для DeleteWaybill, це не те саме, що номер ТТН).
func (c *Client) CreateWaybill(ctx context.Context, req WaybillRequest) (ttn, docRef string, err error) {
	senderRef, senderContactRef, senderPhone, err := c.resolveSender(ctx)
	if err != nil {
		return "", "", fmt.Errorf("відправник: %w", err)
	}

	senderCity, err := c.FindCity(ctx, req.SenderCityName)
	if err != nil {
		return "", "", fmt.Errorf("місто відправника: %w", err)
	}
	senderWarehouse, err := c.FindWarehouse(ctx, senderCity.Ref, req.SenderWarehouseNo)
	if err != nil {
		return "", "", fmt.Errorf("відділення відправника: %w", err)
	}

	recipientCity, err := c.FindCity(ctx, req.RecipientCityName)
	if err != nil {
		return "", "", fmt.Errorf("місто отримувача: %w", err)
	}
	recipientWarehouse, err := c.FindWarehouse(ctx, recipientCity.Ref, req.RecipientWarehouseNo)
	if err != nil {
		return "", "", fmt.Errorf("відділення отримувача: %w", err)
	}

	recipientPhone := normalizePhone(req.RecipientPhone)
	recipientRef, err := c.ensureRecipient(ctx, req.RecipientFullName, recipientPhone)
	if err != nil {
		return "", "", fmt.Errorf("отримувач: %w", err)
	}
	recipientContact, err := c.firstContactPerson(ctx, recipientRef)
	if err != nil {
		return "", "", fmt.Errorf("контакт отримувача: %w", err)
	}

	description := req.Description
	if description == "" {
		description = "Посилка"
	}

	props := map[string]any{
		"PayerType":        "Sender",
		"PaymentMethod":    "Cash",
		"DateTime":         time.Now().Format("02.01.2006"),
		"CargoType":        "Parcel",
		"Weight":           formatNumber(req.Weight),
		"ServiceType":      "WarehouseWarehouse",
		"SeatsAmount":      "1",
		"Description":      description,
		"Cost":             formatNumber(req.DeclaredValue),
		"CitySender":       senderCity.Ref,
		"Sender":           senderRef,
		"SenderAddress":    senderWarehouse.Ref,
		"ContactSender":    senderContactRef,
		"SendersPhone":     senderPhone,
		"CityRecipient":    recipientCity.Ref,
		"Recipient":        recipientRef,
		"RecipientAddress": recipientWarehouse.Ref,
		"ContactRecipient": recipientContact.Ref,
		"RecipientsPhone":  recipientPhone,
	}
	if req.CashOnDelivery > 0 {
		props["BackwardDeliveryData"] = []map[string]string{{
			"PayerType":        "Recipient",
			"CargoType":        "Money",
			"RedeliveryString": formatNumber(req.CashOnDelivery),
		}}
	}

	var docs []struct {
		IntDocNumber string `json:"IntDocNumber"`
		Ref          string `json:"Ref"`
	}
	if err := c.call(ctx, "InternetDocument", "save", props, &docs); err != nil {
		return "", "", fmt.Errorf("створення ТТН: %w", err)
	}
	if len(docs) == 0 || docs[0].IntDocNumber == "" {
		return "", "", fmt.Errorf("np: порожня відповідь при створенні ТТН")
	}
	return docs[0].IntDocNumber, docs[0].Ref, nil
}

// DeleteWaybill скасовує ТТН за її внутрішнім Ref (не номером) — працює, доки
// посилку фізично не занесли на відділення.
func (c *Client) DeleteWaybill(ctx context.Context, docRef string) error {
	return c.call(ctx, "InternetDocument", "delete", map[string]any{
		"Documents": []string{docRef},
	}, nil)
}

// splitFullName розбирає "Прізвище Ім'я Побатькові" на складові.
// Нова Пошта вимагає прізвище+ім'я окремо; побатькові — опціонально.
func splitFullName(full string) (first, last, middle string) {
	parts := strings.Fields(full)
	switch len(parts) {
	case 0:
		return "", "", ""
	case 1:
		return parts[0], parts[0], ""
	case 2:
		return parts[1], parts[0], ""
	default:
		return parts[1], parts[0], strings.Join(parts[2:], " ")
	}
}

// normalizePhone приводить номер до формату 380XXXXXXXXX, як вимагає НП.
func normalizePhone(phone string) string {
	var digits strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()
	switch {
	case strings.HasPrefix(d, "380") && len(d) == 12:
		return d
	case strings.HasPrefix(d, "0") && len(d) == 10:
		return "38" + d
	case len(d) == 9:
		return "380" + d
	}
	return d
}

// formatNumber форматує float без зайвих нулів ("1.20" -> "1.2", "850.00" -> "850").
func formatNumber(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
