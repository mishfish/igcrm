package shipping

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestHandler(svc *Service) http.Handler {
	mux := http.NewServeMux()
	h := &Handler{Service: svc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux.HandleFunc("POST /orders/{id}/ttn", h.CreateTTN)
	mux.HandleFunc("DELETE /orders/{id}/ttn", h.DeleteTTN)
	return mux
}

func doDelete(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func doRequest(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandler_Success(t *testing.T) {
	st := &fakeStore{order: baseOrder()}
	nc := &fakeNP{ttn: "20450000000001"}
	svc := &Service{Store: st, NP: nc, SenderCity: "Одеса", SenderWarehouseNo: "143"}
	h := newTestHandler(svc)

	rec := doRequest(t, h, "/orders/42/ttn", `{"weight": 1.2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		TTN string `json:"ttn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TTN != "20450000000001" {
		t.Fatalf("expected ttn in response, got %q", resp.TTN)
	}
}

func TestHandler_InvalidWeightReturns400(t *testing.T) {
	st := &fakeStore{order: baseOrder()}
	nc := &fakeNP{ttn: "x"}
	svc := &Service{Store: st, NP: nc, SenderCity: "Одеса", SenderWarehouseNo: "143"}
	h := newTestHandler(svc)

	rec := doRequest(t, h, "/orders/42/ttn", `{"weight": 0}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for zero weight, got %d: %s", rec.Code, rec.Body.String())
	}
	if nc.called {
		t.Fatal("np client must not be called for invalid input")
	}
}

func TestHandler_ServiceErrorReturns400(t *testing.T) {
	order := baseOrder()
	existing := "already-shipped"
	order.TTN = &existing
	st := &fakeStore{order: order}
	nc := &fakeNP{ttn: "x"}
	svc := &Service{Store: st, NP: nc, SenderCity: "Одеса", SenderWarehouseNo: "143"}
	h := newTestHandler(svc)

	rec := doRequest(t, h, "/orders/42/ttn", `{"weight": 1.2}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_MalformedBodyReturns400(t *testing.T) {
	svc := &Service{Store: &fakeStore{order: baseOrder()}, NP: &fakeNP{}}
	h := newTestHandler(svc)

	rec := doRequest(t, h, "/orders/42/ttn", `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed body, got %d", rec.Code)
	}
}

func TestHandler_NonNumericIDReturns400(t *testing.T) {
	svc := &Service{Store: &fakeStore{order: baseOrder()}, NP: &fakeNP{}}
	h := newTestHandler(svc)

	rec := doRequest(t, h, "/orders/abc/ttn", `{"weight": 1.2}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-numeric id, got %d", rec.Code)
	}
}

func TestHandler_DeleteSuccess(t *testing.T) {
	st := &fakeStore{order: shippedOrder()}
	nc := &fakeNP{}
	svc := &Service{Store: st, NP: nc}
	h := newTestHandler(svc)

	rec := doDelete(t, h, "/orders/42/ttn")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !nc.deleteCalled {
		t.Fatal("expected np client DeleteWaybill to be called")
	}
}

func TestHandler_DeleteNoTTNReturns400(t *testing.T) {
	st := &fakeStore{order: baseOrder()}
	nc := &fakeNP{}
	svc := &Service{Store: st, NP: nc}
	h := newTestHandler(svc)

	rec := doDelete(t, h, "/orders/42/ttn")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
