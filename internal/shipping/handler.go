package shipping

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
)

// Handler — HTTP-ручка для POST /orders/{id}/ttn.
// Сервіс не виставлений у Caddy назовні: його смикає лише Directus (Manual Flow)
// з тієї ж docker-мережі, тож додаткової автентифікації тут немає.
type Handler struct {
	Service *Service
	Log     *slog.Logger
}

type createTTNRequest struct {
	Weight        float64 `json:"weight"`
	DeclaredValue float64 `json:"declared_value"`
}

// CreateTTN обробляє POST /orders/{id}/ttn.
func (h *Handler) CreateTTN(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "невірний id замовлення")
		return
	}

	var req createTTNRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "невірне тіло запиту: "+err.Error())
		return
	}
	if req.Weight <= 0 {
		writeError(w, http.StatusBadRequest, "вага має бути більшою за нуль")
		return
	}

	ttn, err := h.Service.CreateTTN(r.Context(), id, req.Weight, req.DeclaredValue)
	if err != nil {
		h.Log.Error("create ttn failed", "order_id", id, "err", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"ttn": ttn})
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
