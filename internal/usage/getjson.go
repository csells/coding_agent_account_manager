package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// jsonRequest is one bearer-token GET a usage fetcher makes: where, under
// which headers, and how a refusal is worded for the row.
type jsonRequest struct {
	provider string
	url      string
	// token is the bearer token; an empty one is refused before any request.
	token string
	// headers adds the provider's own headers (User-Agent, device identity)
	// on top of Authorization and Accept.
	headers func(*http.Request)
	// unauthorized words the row error for a 401 or 403. detail is the
	// API's own explanation of the refusal, as a " (...)" suffix, or "".
	unauthorized func(detail string) string
	// detail extracts that explanation from an error body, for APIs that
	// send one. nil keeps status errors bare.
	detail func(body []byte) string
}

func (r jsonRequest) errorDetail(body []byte) string {
	if r.detail == nil {
		return ""
	}
	return r.detail(body)
}

// getJSON runs the request and decodes a 200 body (read up to 1 MiB) into
// out. The returned UsageInfo names the provider and the fetch time and, on
// any failure past building the request, carries the row error too; the
// error alongside says the same for the caller's log.
func getJSON(ctx context.Context, client *http.Client, r jsonRequest, out any) (*UsageInfo, error) {
	if r.token == "" {
		return nil, fmt.Errorf("access token is empty")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", r.url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Accept", "application/json")
	if r.headers != nil {
		r.headers(req)
	}

	resp, err := client.Do(req)
	if err != nil {
		return &UsageInfo{
			Provider:  r.provider,
			FetchedAt: time.Now(),
			Error:     fmt.Sprintf("request failed: %v", err),
		}, err
	}
	defer resp.Body.Close()

	info := &UsageInfo{
		Provider:  r.provider,
		Source:    SourceAPI,
		FetchedAt: time.Now(),
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		info.Error = fmt.Sprintf("read response: %v", err)
		return info, fmt.Errorf("read response: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		detail := r.errorDetail(body)
		info.Error = r.unauthorized(detail)
		return info, fmt.Errorf("unauthorized: status %d%s", resp.StatusCode, detail)
	default:
		detail := r.errorDetail(body)
		info.Error = fmt.Sprintf("API error: status %d%s", resp.StatusCode, detail)
		return info, fmt.Errorf("API error: status %d%s", resp.StatusCode, detail)
	}

	if err := json.Unmarshal(body, out); err != nil {
		info.Error = fmt.Sprintf("decode error: %v", err)
		return info, fmt.Errorf("decode response: %w", err)
	}
	return info, nil
}
