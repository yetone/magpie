package qoder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// UsageHTTPError preserves the upstream status without exposing response data.
type UsageHTTPError struct {
	StatusCode int
}

func (e *UsageHTTPError) Error() string {
	return fmt.Sprintf("qoder usage: upstream HTTP %d", e.StatusCode)
}

// FetchUsage retrieves the account usage envelope using a device token.
func FetchUsage(ctx context.Context, client *http.Client, baseURL, deviceToken string) (json.RawMessage, error) {
	if strings.TrimSpace(deviceToken) == "" {
		return nil, fmt.Errorf("qoder usage: missing device token")
	}
	if client == nil {
		client = &http.Client{}
	}
	if baseURL == "" {
		baseURL = openAPIHost
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+AccountUsagePath, nil)
	if err != nil {
		return nil, fmt.Errorf("qoder usage: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+deviceToken)
	req.Header.Set("Cosy-ClientType", "10")
	req.Header.Set("User-Agent", "Qoder")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qoder usage: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &UsageHTTPError{StatusCode: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("qoder usage: read response: %w", err)
	}
	var envelope struct {
		DisplayMode string          `json:"displayMode"`
		QoderUsage  json.RawMessage `json:"qoderUsage"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("qoder usage: decode response: %w", err)
	}
	if envelope.DisplayMode != "qoder" && envelope.DisplayMode != "enterprise" {
		return nil, fmt.Errorf("qoder usage: unknown display mode")
	}
	if envelope.DisplayMode == "qoder" && (len(envelope.QoderUsage) == 0 || string(envelope.QoderUsage) == "null") {
		return nil, fmt.Errorf("qoder usage: missing quota data")
	}
	return body, nil
}
