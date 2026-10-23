// Package np — мінімальний клієнт API Нової Пошти (створення ТТН).
package np

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const apiBase = "https://api.novaposhta.ua/v2.0/json/"

// Client — клієнт Nova Poshta API.
type Client struct {
	APIKey  string
	HTTP    *http.Client
	BaseURL string // для тестів; порожньо = api.novaposhta.ua
}

type apiRequest struct {
	APIKey           string         `json:"apiKey"`
	ModelName        string         `json:"modelName"`
	CalledMethod     string         `json:"calledMethod"`
	MethodProperties map[string]any `json:"methodProperties,omitempty"`
}

type apiResponse struct {
	Success  bool              `json:"success"`
	Data     []json.RawMessage `json:"data"`
	Errors   []string          `json:"errors"`
	Warnings []string          `json:"warnings"`
}

// APIError — помилка, яку повернув сам API Нової Пошти (success: false).
type APIError struct {
	Errors []string
}

func (e *APIError) Error() string {
	return "nova poshta api: " + strings.Join(e.Errors, "; ")
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return apiBase
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// call виконує метод API й декодує data у out (вказівник на слайс).
func (c *Client) call(ctx context.Context, modelName, method string, props map[string]any, out any) error {
	body, err := json.Marshal(apiRequest{
		APIKey:           c.APIKey,
		ModelName:        modelName,
		CalledMethod:     method,
		MethodProperties: props,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	var ar apiResponse
	if err := json.Unmarshal(respBody, &ar); err != nil {
		return fmt.Errorf("np %s/%s: decode response: %w", modelName, method, err)
	}
	if !ar.Success {
		return &APIError{Errors: ar.Errors}
	}
	if out == nil {
		return nil
	}
	raw, err := json.Marshal(ar.Data)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// City — місто у довіднику Нової Пошти.
type City struct {
	Ref         string `json:"Ref"`
	Description string `json:"Description"`
}

// FindCity шукає місто за назвою; повертає перший знайдений збіг.
func (c *Client) FindCity(ctx context.Context, name string) (City, error) {
	var cities []City
	if err := c.call(ctx, "Address", "getCities", map[string]any{"FindByString": name}, &cities); err != nil {
		return City{}, err
	}
	if len(cities) == 0 {
		return City{}, fmt.Errorf("np: місто %q не знайдено", name)
	}
	return cities[0], nil
}

// Warehouse — відділення/поштомат Нової Пошти.
type Warehouse struct {
	Ref         string `json:"Ref"`
	Number      string `json:"Number"`
	Description string `json:"Description"`
}

// FindWarehouse шукає відділення за точним номером у межах міста.
// getWarehouses віддає максимум ~150 відділень за сторінку, тому для великих
// міст (Київ тощо) перебираємо сторінки, доки не знайдемо збіг або список не скінчиться.
func (c *Client) FindWarehouse(ctx context.Context, cityRef, number string) (Warehouse, error) {
	for page := 1; page <= 20; page++ {
		var warehouses []Warehouse
		err := c.call(ctx, "Address", "getWarehouses", map[string]any{
			"CityRef": cityRef,
			"Page":    fmt.Sprint(page),
		}, &warehouses)
		if err != nil {
			return Warehouse{}, err
		}
		if len(warehouses) == 0 {
			break
		}
		for _, w := range warehouses {
			if w.Number == number {
				return w, nil
			}
		}
	}
	return Warehouse{}, fmt.Errorf("np: відділення №%s не знайдено в місті (ref %s)", number, cityRef)
}
