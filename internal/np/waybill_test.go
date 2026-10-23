package np

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// npServer симулює послідовність методів НП для повного створення ТТН:
// Counterparty/getCounterparties (Sender), Counterparty/getCounterpartyContactPersons (sender),
// Address/getCities, Address/getWarehouses, Counterparty/save (Recipient),
// Counterparty/getCounterpartyContactPersons (recipient), InternetDocument/save.
// Назви моделей/полів тут звірені з реальним API (не лише задокументовані) — див. коміт.
func npServer(t *testing.T, onInternetDocumentSave func(props map[string]any)) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		switch req.CalledMethod {
		case "getCounterparties":
			writeJSON(t, w, map[string]any{"success": true, "data": []map[string]string{
				{"Ref": "sender-cp-ref"},
			}})
		case "getCounterpartyContactPersons":
			if req.ModelName != "Counterparty" {
				t.Fatalf("getCounterpartyContactPersons must be called on model Counterparty, got %q", req.ModelName)
			}
			ref, _ := req.MethodProperties["Ref"].(string)
			if ref == "sender-cp-ref" {
				writeJSON(t, w, map[string]any{"success": true, "data": []map[string]string{
					{"Ref": "sender-contact-ref", "Phones": "380501112233"},
				}})
				return
			}
			writeJSON(t, w, map[string]any{"success": true, "data": []map[string]string{
				{"Ref": "recipient-contact-ref", "Phones": "380671234567"},
			}})
		case "getCities":
			name, _ := req.MethodProperties["FindByString"].(string)
			ref := "odesa-ref"
			if name == "Київ" {
				ref = "kyiv-ref"
			}
			writeJSON(t, w, map[string]any{"success": true, "data": []map[string]string{
				{"Ref": ref, "Description": name},
			}})
		case "getWarehouses":
			cityRef, _ := req.MethodProperties["CityRef"].(string)
			number, wh := "143", "odesa-wh-143-ref"
			if cityRef == "kyiv-ref" {
				number, wh = "45", "kyiv-wh-45-ref"
			}
			writeJSON(t, w, map[string]any{"success": true, "data": []map[string]string{
				{"Ref": wh, "Number": number, "Description": "Відділення №" + number},
			}})
		case "save":
			switch req.ModelName {
			case "Counterparty":
				writeJSON(t, w, map[string]any{"success": true, "data": []map[string]string{
					{"Ref": "recipient-cp-ref"},
				}})
			case "InternetDocument":
				if onInternetDocumentSave != nil {
					onInternetDocumentSave(req.MethodProperties)
				}
				writeJSON(t, w, map[string]any{"success": true, "data": []map[string]string{
					{"IntDocNumber": "20450000000001", "Ref": "doc-ref-1"},
				}})
			default:
				t.Fatalf("unexpected save on model %s", req.ModelName)
			}
		default:
			t.Fatalf("unexpected method %s/%s", req.ModelName, req.CalledMethod)
		}
	}
}

func TestCreateWaybill_PrepaidOrder(t *testing.T) {
	var sentProps map[string]any
	c := newTestClient(t, npServer(t, func(props map[string]any) { sentProps = props }))

	ttn, docRef, err := c.CreateWaybill(context.Background(), WaybillRequest{
		SenderCityName:       "Одеса",
		SenderWarehouseNo:    "143",
		RecipientFullName:    "Іваненко Іван",
		RecipientPhone:       "0671234567",
		RecipientCityName:    "Київ",
		RecipientWarehouseNo: "45",
		Weight:               1.2,
		DeclaredValue:        850,
		CashOnDelivery:       0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ttn != "20450000000001" {
		t.Fatalf("expected ttn 20450000000001, got %q", ttn)
	}
	if docRef != "doc-ref-1" {
		t.Fatalf("expected doc ref doc-ref-1, got %q", docRef)
	}
	if _, ok := sentProps["BackwardDeliveryData"]; ok {
		t.Fatalf("prepaid order must not include BackwardDeliveryData, got %v", sentProps["BackwardDeliveryData"])
	}
	if sentProps["Weight"] != "1.2" {
		t.Fatalf("expected Weight=1.2, got %v", sentProps["Weight"])
	}
	if sentProps["Cost"] != "850" {
		t.Fatalf("expected Cost=850, got %v", sentProps["Cost"])
	}
	if sentProps["RecipientsPhone"] != "380671234567" {
		t.Fatalf("expected normalized phone, got %v", sentProps["RecipientsPhone"])
	}
}

func TestCreateWaybill_CashOnDeliveryIncludesBackwardDeliveryData(t *testing.T) {
	var sentProps map[string]any
	c := newTestClient(t, npServer(t, func(props map[string]any) { sentProps = props }))

	_, _, err := c.CreateWaybill(context.Background(), WaybillRequest{
		SenderCityName:       "Одеса",
		SenderWarehouseNo:    "143",
		RecipientFullName:    "Іваненко Іван",
		RecipientPhone:       "0671234567",
		RecipientCityName:    "Київ",
		RecipientWarehouseNo: "45",
		Weight:               1.2,
		DeclaredValue:        850,
		CashOnDelivery:       850,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, ok := sentProps["BackwardDeliveryData"]
	if !ok {
		t.Fatal("expected BackwardDeliveryData for cash-on-delivery order")
	}
	b, _ := json.Marshal(raw)
	var backward []map[string]string
	if err := json.Unmarshal(b, &backward); err != nil {
		t.Fatalf("decode BackwardDeliveryData: %v", err)
	}
	if len(backward) != 1 || backward[0]["PayerType"] != "Recipient" || backward[0]["CargoType"] != "Money" || backward[0]["RedeliveryString"] != "850" {
		t.Fatalf("unexpected BackwardDeliveryData: %+v", backward)
	}
}

func TestDeleteWaybill_Success(t *testing.T) {
	var sentProps map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if req.ModelName != "InternetDocument" || req.CalledMethod != "delete" {
			t.Fatalf("unexpected call: %+v", req)
		}
		sentProps = req.MethodProperties
		writeJSON(t, w, map[string]any{"success": true, "data": []map[string]string{
			{"Ref": "doc-ref-1"},
		}})
	})

	if err := c.DeleteWaybill(context.Background(), "doc-ref-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	docs, ok := sentProps["Documents"].([]any)
	if !ok || len(docs) != 1 || docs[0] != "doc-ref-1" {
		t.Fatalf("expected Documents=[doc-ref-1], got %v", sentProps["Documents"])
	}
}

func TestDeleteWaybill_APIErrorReturnsError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"success": false, "errors": []string{"Document not found"}})
	})

	if err := c.DeleteWaybill(context.Background(), "bad-ref"); err == nil {
		t.Fatal("expected error when NP API reports failure")
	}
}
