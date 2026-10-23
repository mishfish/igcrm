// Package shipping звʼязує замовлення (store) з API Нової Пошти (np): по кліку
// кнопки в Directus створює ТТН і записує номер/статус назад у замовлення.
package shipping

import (
	"context"
	"fmt"
	"regexp"

	"igcrm/internal/np"
)

// Order — дані замовлення, потрібні для відправки. Відповідає рядку таблиці orders.
type Order struct {
	ID          int64
	FullName    string
	Phone       string
	City        string
	NPBranch    string // "Відділення №45" / "Поштомат №1234" / довільний текст
	Amount      *float64
	PaymentType string // full_prepay | partial_prepay | cod
	TTN         *string
}

// Store — те, що Service потребує від БД.
type Store interface {
	OrderForShipping(ctx context.Context, id int64) (Order, error)
	SetOrderTTN(ctx context.Context, id int64, ttn string) error
}

// NPClient — те, що Service потребує від клієнта Нової Пошти.
type NPClient interface {
	CreateWaybill(ctx context.Context, req np.WaybillRequest) (string, error)
}

// Service створює ТТН для замовлення.
type Service struct {
	Store             Store
	NP                NPClient
	SenderCity        string
	SenderWarehouseNo string
}

var branchNumberRe = regexp.MustCompile(`\d+`)

// warehouseNumber витягує номер відділення/поштомату з тексту картки клієнта.
func warehouseNumber(npBranch string) (string, error) {
	m := branchNumberRe.FindString(npBranch)
	if m == "" {
		return "", fmt.Errorf("не вдалося визначити номер відділення з %q — виправ поле у картці клієнта", npBranch)
	}
	return m, nil
}

// CreateTTN створює ТТН для замовлення orderID і записує номер/статус "shipped" у БД.
// declaredValue <= 0 означає "взяти суму замовлення".
func (s *Service) CreateTTN(ctx context.Context, orderID int64, weight, declaredValue float64) (string, error) {
	order, err := s.Store.OrderForShipping(ctx, orderID)
	if err != nil {
		return "", fmt.Errorf("замовлення: %w", err)
	}
	if order.TTN != nil && *order.TTN != "" {
		return "", fmt.Errorf("замовлення №%d вже має ТТН %s", orderID, *order.TTN)
	}

	branchNo, err := warehouseNumber(order.NPBranch)
	if err != nil {
		return "", err
	}

	if declaredValue <= 0 {
		if order.Amount == nil {
			return "", fmt.Errorf("замовлення №%d: невідома сума, вкажи оголошену вартість вручну", orderID)
		}
		declaredValue = *order.Amount
	}

	var cod float64
	if order.PaymentType == "cod" {
		if order.Amount == nil {
			return "", fmt.Errorf("замовлення №%d: оплата при отриманні, але сума замовлення не вказана", orderID)
		}
		cod = *order.Amount
	}

	ttn, err := s.NP.CreateWaybill(ctx, np.WaybillRequest{
		SenderCityName:       s.SenderCity,
		SenderWarehouseNo:    s.SenderWarehouseNo,
		RecipientFullName:    order.FullName,
		RecipientPhone:       order.Phone,
		RecipientCityName:    order.City,
		RecipientWarehouseNo: branchNo,
		Weight:               weight,
		DeclaredValue:        declaredValue,
		CashOnDelivery:       cod,
	})
	if err != nil {
		return "", fmt.Errorf("створення ТТН: %w", err)
	}

	if err := s.Store.SetOrderTTN(ctx, orderID, ttn); err != nil {
		return "", fmt.Errorf("збереження ТТН: %w", err)
	}
	return ttn, nil
}
