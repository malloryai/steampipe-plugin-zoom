package zoom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type row map[string]any

type client struct {
	http                              *http.Client
	apiURL, tokenURL                  string
	accountID, clientID, clientSecret string
	mu                                sync.Mutex
	token                             string
	expires                           time.Time
	renewing                          chan struct{}
	requests                          chan struct{}
}

// The SDK cache formats rejected values in errors that it logs.
func (*client) String() string { return "Zoom client (credentials redacted)" }

func newClient(cfg zoomConfig) (*client, error) {
	for _, value := range []*string{cfg.AccountID, cfg.ClientID, cfg.ClientSecret} {
		if value == nil || strings.TrimSpace(*value) == "" {
			return nil, errors.New("Zoom requires explicit account_id, client_id and client_secret")
		}
	}
	return &client{http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, apiURL: "https://api.zoom.us/v2", tokenURL: "https://zoom.us/oauth/token", accountID: *cfg.AccountID, clientID: *cfg.ClientID, clientSecret: *cfg.ClientSecret, requests: make(chan struct{}, 4)}, nil
}

func (c *client) accessToken(ctx context.Context, rejected string) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		c.mu.Lock()
		if c.token != "" && c.token != rejected && time.Now().Before(c.expires) {
			token := c.token
			c.mu.Unlock()
			return token, nil
		}
		if pending := c.renewing; pending != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-pending:
			}
			continue
		}
		c.renewing = make(chan struct{})
		c.mu.Unlock()
		token, expires, err := c.renewToken(ctx)
		c.mu.Lock()
		if err == nil {
			c.token, c.expires = token, expires
		}
		close(c.renewing)
		c.renewing = nil
		c.mu.Unlock()
		return token, err
	}
}

func (c *client) renewToken(ctx context.Context) (string, time.Time, error) {
	form := url.Values{"grant_type": {"account_credentials"}, "account_id": {c.accountID}}
	body, err := c.request(ctx, http.MethodPost, c.tokenURL, form, "")
	if err != nil {
		return "", time.Time{}, err
	}
	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if json.Unmarshal(body, &result) != nil || result.AccessToken == "" || result.ExpiresIn <= 0 || result.ExpiresIn > 86400 {
		return "", time.Time{}, errors.New("Zoom returned an invalid token response")
	}
	ttl := time.Duration(result.ExpiresIn) * time.Second
	skew := min(30*time.Second, ttl/10)
	return result.AccessToken, time.Now().Add(ttl - skew), nil
}

type responseError struct{ status int }

func (e *responseError) Error() string {
	return fmt.Sprintf("Zoom request failed (HTTP %d); check credentials, read scopes and account entitlement", e.status)
}

func (c *client) request(ctx context.Context, method, target string, values url.Values, token string) ([]byte, error) {
	select {
	case c.requests <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.requests }()
	for attempt := 0; attempt < 4; attempt++ {
		var body io.Reader
		requestURL := target
		if method == http.MethodPost {
			body = strings.NewReader(values.Encode())
		} else if len(values) > 0 {
			requestURL += "?" + values.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, method, requestURL, body)
		if err != nil {
			return nil, errors.New("Zoom request could not be created")
		}
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.SetBasicAuth(c.clientID, c.clientSecret)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("Zoom request failed during transport")
		}
		if response.StatusCode == 429 {
			retryAfter := response.Header.Get("Retry-After")
			response.Body.Close()
			if attempt == 3 {
				return nil, &responseError{429}
			}
			delay := time.Duration(1<<attempt) * time.Second
			if seconds, err := strconv.ParseInt(retryAfter, 10, 64); err == nil && seconds >= 0 && seconds <= 30 {
				delay = time.Duration(seconds) * time.Second
			} else if retryAfter != "" {
				when, err := http.ParseTime(retryAfter)
				if err != nil {
					return nil, &responseError{429}
				}
				delay = max(0, time.Until(when))
			}
			if delay > 30*time.Second {
				return nil, &responseError{429}
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			response.Body.Close()
			return nil, &responseError{response.StatusCode}
		}
		const maxResponse = 32 * 1024 * 1024
		data, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
		response.Body.Close()
		if err != nil {
			return nil, errors.New("Zoom response could not be read")
		}
		if len(data) > maxResponse {
			return nil, errors.New("Zoom response exceeds the safe page size")
		}
		return data, nil
	}
	return nil, &responseError{429}
}

func (c *client) get(ctx context.Context, path string, values url.Values) ([]byte, error) {
	token, err := c.accessToken(ctx, "")
	if err != nil {
		return nil, err
	}
	body, err := c.request(ctx, http.MethodGet, c.apiURL+path, values, token)
	var response *responseError
	if errors.As(err, &response) && response.status == 401 {
		token, err = c.accessToken(ctx, token)
		if err != nil {
			return nil, err
		}
		return c.request(ctx, http.MethodGet, c.apiURL+path, values, token)
	}
	return body, err
}

func (c *client) pages(ctx context.Context, path, key string, values url.Values, emit func(row) error) error {
	query := url.Values{"page_size": {"300"}}
	for k, v := range values {
		query[k] = append([]string(nil), v...)
	}
	seen := map[string]bool{}
	for {
		body, err := c.get(ctx, path, query)
		if err != nil {
			return err
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal(body, &envelope) != nil {
			return errors.New("Zoom returned an invalid page envelope")
		}
		raw, ok := envelope[key]
		if !ok || len(raw) == 0 || raw[0] != '[' {
			return errors.New("Zoom response is missing its row array")
		}
		var items []row
		if json.Unmarshal(raw, &items) != nil {
			return errors.New("Zoom returned an invalid row array")
		}
		var next string
		if raw, ok := envelope["next_page_token"]; ok {
			if string(raw) == "null" || json.Unmarshal(raw, &next) != nil {
				return errors.New("Zoom returned an invalid page token")
			}
		}
		for _, item := range items {
			if item == nil {
				return errors.New("Zoom returned an invalid row")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := emit(item); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		if seen[next] {
			return errors.New("Zoom returned a repeated page token")
		}
		seen[next] = true
		query.Set("next_page_token", next)
	}
}
