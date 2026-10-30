package pulse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type handlerTransport struct{ handler http.Handler }

func (h handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w.Result(), nil
}
func TestCaptureBatch(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/capture" || r.Method != "POST" {
			t.Error("wrong endpoint")
		}
		var body struct {
			Key    string  `json:"api_key"`
			Events []Event `json:"events"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Key != "pk_test" || len(body.Events) != 1 || body.Events[0].ID != "order-1" || body.Events[0].Timestamp.IsZero() {
			t.Errorf("invalid payload: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"accepted":1}`))
	})
	c, err := New("https://analytics.example", "pk_test")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTPClient = &http.Client{Transport: handlerTransport{handler}}
	n, err := c.CaptureBatch(context.Background(), []Event{{ID: "order-1", Name: "paid", DistinctID: "u"}})
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
}
func TestClientValidationAndErrors(t *testing.T) {
	if _, err := New("file:///tmp/test", "key"); err == nil {
		t.Fatal("invalid host accepted")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"Unknown key"}`))
	})
	c, _ := New("https://analytics.example", "key")
	c.HTTPClient = &http.Client{Transport: handlerTransport{handler}}
	if err := c.Capture(context.Background(), Event{Name: "x", DistinctID: "u"}); err == nil {
		t.Fatal("HTTP error hidden")
	}
	if _, err := c.CaptureBatch(context.Background(), nil); err == nil {
		t.Fatal("empty batch accepted")
	}
}
