package shipping

import (
	"context"
	"testing"

	"igcrm/internal/np"
)

type fakeStore struct {
	order    Order
	getErr   error
	setErr   error
	setCalls []struct {
		id  int64
		ttn string
	}
}

func (f *fakeStore) OrderForShipping(ctx context.Context, id int64) (Order, error) {
	return f.order, f.getErr
}

func (f *fakeStore) SetOrderTTN(ctx context.Context, id int64, ttn string) error {
	f.setCalls = append(f.setCalls, struct {
		id  int64
		ttn string
	}{id, ttn})
	return f.setErr
}

type fakeNP struct {
	req    np.WaybillRequest
	ttn    string
	err    error
	called bool
}

func (f *fakeNP) CreateWaybill(ctx context.Context, req np.WaybillRequest) (string, error) {
	f.called = true
	f.req = req
	return f.ttn, f.err
}

func amountPtr(v float64) *float64 { return &v }

func baseOrder() Order {
	return Order{
		ID:          42,
		FullName:    "Іваненко Іван",
		Phone:       "+380671234567",
		City:        "Київ",
		NPBranch:    "Відділення №45",
		Amount:      amountPtr(850),
		PaymentType: "full_prepay",
	}
}

func TestCreateTTN_Success(t *testing.T) {
	st := &fakeStore{order: baseOrder()}
	nc := &fakeNP{ttn: "20450000000001"}
	svc := &Service{Store: st, NP: nc, SenderCity: "Одеса", SenderWarehouseNo: "143"}

	ttn, err := svc.CreateTTN(context.Background(), 42, 1.2, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ttn != "20450000000001" {
		t.Fatalf("expected ttn 20450000000001, got %q", ttn)
	}
	if !nc.called {
		t.Fatal("expected np client to be called")
	}
	if nc.req.RecipientCityName != "Київ" || nc.req.RecipientWarehouseNo != "45" {
		t.Fatalf("unexpected recipient address: %+v", nc.req)
	}
	if nc.req.SenderCityName != "Одеса" || nc.req.SenderWarehouseNo != "143" {
		t.Fatalf("unexpected sender address: %+v", nc.req)
	}
	if nc.req.DeclaredValue != 850 {
		t.Fatalf("expected declared value to default to order amount 850, got %v", nc.req.DeclaredValue)
	}
	if nc.req.CashOnDelivery != 0 {
		t.Fatalf("expected no cash on delivery for full_prepay, got %v", nc.req.CashOnDelivery)
	}
	if len(st.setCalls) != 1 || st.setCalls[0].id != 42 || st.setCalls[0].ttn != "20450000000001" {
		t.Fatalf("expected SetOrderTTN(42, ttn) to be called once, got %+v", st.setCalls)
	}
}

func TestCreateTTN_AlreadyShipped(t *testing.T) {
	order := baseOrder()
	existing := "20450000000099"
	order.TTN = &existing
	st := &fakeStore{order: order}
	nc := &fakeNP{ttn: "should-not-be-used"}
	svc := &Service{Store: st, NP: nc, SenderCity: "Одеса", SenderWarehouseNo: "143"}

	_, err := svc.CreateTTN(context.Background(), 42, 1.2, 0)
	if err == nil {
		t.Fatal("expected error for order that already has a ttn")
	}
	if nc.called {
		t.Fatal("np client must not be called for an already-shipped order")
	}
}

func TestCreateTTN_CashOnDeliveryUsesOrderAmount(t *testing.T) {
	order := baseOrder()
	order.PaymentType = "cod"
	st := &fakeStore{order: order}
	nc := &fakeNP{ttn: "ttn-1"}
	svc := &Service{Store: st, NP: nc, SenderCity: "Одеса", SenderWarehouseNo: "143"}

	if _, err := svc.CreateTTN(context.Background(), 42, 1.2, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nc.req.CashOnDelivery != 850 {
		t.Fatalf("expected cash-on-delivery amount 850, got %v", nc.req.CashOnDelivery)
	}
}

func TestCreateTTN_InvalidBranchFormatReturnsError(t *testing.T) {
	order := baseOrder()
	order.NPBranch = "біля ринку, запитати продавчиню" // жодного номера відділення
	st := &fakeStore{order: order}
	nc := &fakeNP{ttn: "should-not-be-used"}
	svc := &Service{Store: st, NP: nc, SenderCity: "Одеса", SenderWarehouseNo: "143"}

	_, err := svc.CreateTTN(context.Background(), 42, 1.2, 0)
	if err == nil {
		t.Fatal("expected error when np_branch has no parseable warehouse number")
	}
	if nc.called {
		t.Fatal("np client must not be called when recipient address is unresolved")
	}
}

func TestCreateTTN_DeclaredValueOverridesOrderAmount(t *testing.T) {
	st := &fakeStore{order: baseOrder()}
	nc := &fakeNP{ttn: "ttn-1"}
	svc := &Service{Store: st, NP: nc, SenderCity: "Одеса", SenderWarehouseNo: "143"}

	if _, err := svc.CreateTTN(context.Background(), 42, 1.2, 500); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nc.req.DeclaredValue != 500 {
		t.Fatalf("expected explicit declared value 500 to win, got %v", nc.req.DeclaredValue)
	}
}
