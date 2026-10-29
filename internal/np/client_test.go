package np

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{APIKey: "test-key", BaseURL: srv.URL}
}

func decodeRequest(t *testing.T, r *http.Request) apiRequest {
	t.Helper()
	var req apiRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return req
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestFindCity_ReturnsFirstMatch(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if req.ModelName != "Address" || req.CalledMethod != "getCities" {
			t.Fatalf("unexpected call: %+v", req)
		}
		if req.MethodProperties["FindByString"] != "Одеса" {
			t.Fatalf("expected FindByString=Одеса, got %v", req.MethodProperties)
		}
		writeJSON(t, w, map[string]any{
			"success": true,
			"data": []map[string]string{
				{"Ref": "city-ref-1", "Description": "Одеса"},
			},
		})
	})

	city, err := c.FindCity(context.Background(), "Одеса")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if city.Ref != "city-ref-1" {
		t.Fatalf("expected city-ref-1, got %q", city.Ref)
	}
}

func TestFindCity_NoMatchReturnsError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"success": true, "data": []map[string]string{}})
	})

	_, err := c.FindCity(context.Background(), "Неіснуюче Місто")
	if err == nil {
		t.Fatal("expected error for no matching city")
	}
}

func TestFindWarehouse_ExactNumberMatch(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if req.CalledMethod != "getWarehouses" {
			t.Fatalf("unexpected call: %+v", req)
		}
		writeJSON(t, w, map[string]any{
			"success": true,
			"data": []map[string]string{
				{"Ref": "wh-ref-14", "Number": "14", "Description": "Відділення №14"},
				{"Ref": "wh-ref-143", "Number": "143", "Description": "Відділення №143"},
			},
		})
	})

	wh, err := c.FindWarehouse(context.Background(), "city-ref-1", "143")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wh.Ref != "wh-ref-143" {
		t.Fatalf("expected wh-ref-143, got %q", wh.Ref)
	}
}

func TestFindWarehouse_NotFoundReturnsError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if req.MethodProperties["Page"] != "1" {
			writeJSON(t, w, map[string]any{"success": true, "data": []map[string]string{}})
			return
		}
		writeJSON(t, w, map[string]any{
			"success": true,
			"data": []map[string]string{
				{"Ref": "wh-ref-14", "Number": "14", "Description": "Відділення №14"},
			},
		})
	})

	_, err := c.FindWarehouse(context.Background(), "city-ref-1", "999")
	if err == nil {
		t.Fatal("expected error for missing warehouse number")
	}
}

func TestCall_SurfacesAPIErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"success": false,
			"errors":  []string{"Invalid API key"},
		})
	})

	_, err := c.FindCity(context.Background(), "Одеса")
	if err == nil {
		t.Fatal("expected error when success=false")
	}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestFindWarehouse_LooksUpByNumberInOneRequest(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		req := decodeRequest(t, r)
		if req.MethodProperties["WarehouseId"] != "143" {
			t.Fatalf("expected WarehouseId=143, got %v", req.MethodProperties)
		}
		writeJSON(t, w, map[string]any{"success": true, "data": []map[string]string{
			{"Ref": "wh-ref-143", "Number": "143"},
		}})
	})

	wh, err := c.FindWarehouse(context.Background(), "city-ref-1", "143")
	if err != nil || wh.Ref != "wh-ref-143" {
		t.Fatalf("wh=%+v err=%v", wh, err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 request, got %d", calls)
	}
}

func TestFindWarehouse_FallsBackToPagingWhenNumberFilterMisses(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeRequest(t, r)
		if _, byNumber := req.MethodProperties["WarehouseId"]; byNumber {
			writeJSON(t, w, map[string]any{"success": true, "data": []map[string]string{}})
			return
		}
		if req.MethodProperties["Limit"] != "500" {
			t.Fatalf("expected Limit=500 when paging, got %v", req.MethodProperties)
		}
		if req.MethodProperties["Page"] != "1" {
			writeJSON(t, w, map[string]any{"success": true, "data": []map[string]string{}})
			return
		}
		writeJSON(t, w, map[string]any{"success": true, "data": []map[string]string{
			{"Ref": "wh-ref-7", "Number": "7"},
		}})
	})

	wh, err := c.FindWarehouse(context.Background(), "city-ref-1", "7")
	if err != nil || wh.Ref != "wh-ref-7" {
		t.Fatalf("wh=%+v err=%v", wh, err)
	}
}

func TestCall_RetriesOnTooManyRequests(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			writeJSON(t, w, map[string]any{"success": false, "errors": []string{"To many requests"}})
			return
		}
		writeJSON(t, w, map[string]any{"success": true, "data": []map[string]string{{"Ref": "city-ref-1"}}})
	})
	c.RetryBase = time.Millisecond

	city, err := c.FindCity(context.Background(), "Одеса")
	if err != nil || city.Ref != "city-ref-1" {
		t.Fatalf("city=%+v err=%v", city, err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
}

func TestCall_GivesUpAfterMaxRetries(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(t, w, map[string]any{"success": false, "errors": []string{"To many requests"}})
	})
	c.RetryBase = time.Millisecond

	if _, err := c.FindCity(context.Background(), "Одеса"); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if calls != maxRateLimitRetries+1 {
		t.Fatalf("expected %d attempts, got %d", maxRateLimitRetries+1, calls)
	}
}

func TestCall_DoesNotRetryOtherAPIErrors(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(t, w, map[string]any{"success": false, "errors": []string{"Invalid API key"}})
	})
	c.RetryBase = time.Millisecond

	if _, err := c.FindCity(context.Background(), "Одеса"); err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("expected 1 attempt, got %d", calls)
	}
}
