// Package searchclient implements rolling public search-wire negotiation.
package searchclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Client prefers the negotiated v2 endpoint and falls back to the original
// strict v1 request shape when an older server does not expose v2.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// Search performs a public search without ever sending v2-only properties to
// /v1. A v1 response is accepted as a capability downgrade: fields such as
// exactFailureMatched remain unavailable/false and SchemaVersion stays 1.
func (c Client) Search(ctx context.Context, req domain.SearchRequest) (domain.SearchResponse, error) {
	if req.SchemaVersion >= 2 {
		var response domain.SearchResponse
		status, err := c.post(ctx, "/v2/search", req, &response)
		if err == nil && status/100 == 2 {
			return response, nil
		}
		if err != nil || (status != http.StatusNotFound && status != http.StatusMethodNotAllowed) {
			return domain.SearchResponse{}, searchError("v2", status, err)
		}
	}

	legacy := v1Request{
		SchemaVersion: 1,
		Query:         req.Query, Packages: req.Packages, Symbols: req.Symbols,
		Environment: req.Environment, ErrorFingerprint: req.ErrorFingerprint,
		ErrorCode: req.ErrorCode, Limit: req.Limit,
	}
	var response domain.SearchResponse
	status, err := c.post(ctx, "/v1/search", legacy, &response)
	if err != nil || status/100 != 2 {
		return domain.SearchResponse{}, searchError("v1", status, err)
	}
	return response, nil
}

type v1Request struct {
	SchemaVersion    int                           `json:"schemaVersion"`
	Query            string                        `json:"query"`
	Packages         []string                      `json:"packages,omitempty"`
	Symbols          []string                      `json:"symbols,omitempty"`
	Environment      domain.EnvironmentFingerprint `json:"environment"`
	ErrorFingerprint string                        `json:"errorFingerprint,omitempty"`
	ErrorCode        string                        `json:"errorCode,omitempty"`
	Limit            int                           `json:"limit,omitempty"`
}

func (c Client) post(ctx context.Context, path string, input, output any) (int, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(output); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

func searchError(version string, status int, err error) error {
	if err != nil {
		return fmt.Errorf("search %s: %w", version, err)
	}
	return fmt.Errorf("search %s: HTTP %d", version, status)
}
