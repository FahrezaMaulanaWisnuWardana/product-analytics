// Package productanalytics sends product events to an independent product-analytics service.
package productanalytics

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Event struct {
	ID         string         `json:"id,omitempty"`
	Name       string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	SessionID  string         `json:"session_id,omitempty"`
	Timestamp  time.Time      `json:"timestamp,omitzero"`
	Properties map[string]any `json:"properties,omitempty"`
}
type Client struct {
	endpoint   string
	key        string
	HTTPClient *http.Client
}

func New(host, key string) (*Client, error) {
	u, err := url.Parse(host)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("host must be an absolute HTTP(S) URL")
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("project key is required")
	}
	return &Client{endpoint: strings.TrimRight(host, "/") + "/capture", key: key, HTTPClient: &http.Client{Timeout: 10 * time.Second}}, nil
}
func (c *Client) Capture(ctx context.Context, event Event) error {
	_, err := c.CaptureBatch(ctx, []Event{event})
	return err
}

// CaptureBatch returns the number of newly accepted events. Reuse Event.ID across
// application retries to deduplicate; this client does not retry automatically.
func (c *Client) CaptureBatch(ctx context.Context, events []Event) (int, error) {
	if len(events) < 1 || len(events) > 100 {
		return 0, fmt.Errorf("a batch must contain 1–100 events")
	}
	prepared := append([]Event{}, events...)
	for i := range prepared {
		if prepared[i].Name == "" || prepared[i].DistinctID == "" {
			return 0, fmt.Errorf("event and distinct_id are required")
		}
		if prepared[i].ID == "" {
			b := make([]byte, 16)
			if _, err := rand.Read(b); err != nil {
				return 0, err
			}
			prepared[i].ID = hex.EncodeToString(b)
		}
		if prepared[i].Timestamp.IsZero() {
			prepared[i].Timestamp = time.Now().UTC()
		}
	}
	body, err := json.Marshal(struct {
		APIKey string  `json:"api_key"`
		Events []Event `json:"events"`
	}{c.key, prepared})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return 0, fmt.Errorf("product-analytics: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(detail)))
	}
	var result struct {
		Accepted int `json:"accepted"`
	}
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		return 0, err
	}
	return result.Accepted, nil
}
