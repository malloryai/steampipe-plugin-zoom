package zoom

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func ptr(s string) *string { return &s }
func config() zoomConfig {
	return zoomConfig{AccountID: ptr("account"), ClientID: ptr("client"), ClientSecret: ptr("secret")}
}
func testClient(t *testing.T, handler http.HandlerFunc) *client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := newClient(config())
	if err != nil {
		t.Fatal(err)
	}
	c.http = server.Client()
	c.apiURL = server.URL
	c.tokenURL = server.URL + "/oauth/token"
	return c
}
func tokenResponse(w http.ResponseWriter) {
	fmt.Fprint(w, `{"access_token":"token","expires_in":3600,"token_type":"bearer"}`)
}
func TestExplicitCredentials(t *testing.T) {
	t.Setenv("ZOOM_ACCOUNT_ID", "ambient")
	t.Setenv("ZOOM_CLIENT_ID", "ambient")
	t.Setenv("ZOOM_CLIENT_SECRET", "ambient")
	for _, c := range []zoomConfig{{}, {AccountID: ptr("a")}, {AccountID: ptr("a"), ClientID: ptr("b")}, {AccountID: ptr(" "), ClientID: ptr("b"), ClientSecret: ptr("c")}} {
		if _, err := newClient(c); err == nil {
			t.Fatal("accepted incomplete explicit credentials")
		}
	}
}
func TestTokenRenewalAndCoalescing(t *testing.T) {
	var tokens atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			n := tokens.Add(1)
			r.ParseForm()
			user, pass, ok := r.BasicAuth()
			if !ok || user != "client" || pass != "secret" || r.Form.Get("account_id") != "account" || r.Form.Get("grant_type") != "account_credentials" {
				t.Error("incorrect token request")
			}
			fmt.Fprintf(w, `{"access_token":"token%d","expires_in":3600}`, n)
			return
		}
		if r.Header.Get("Authorization") == "Bearer token1" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"users":[]}`)
	})
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if err := c.pages(context.Background(), "/users", "users", nil, func(row) error { return nil }); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if tokens.Load() != 2 {
		t.Fatalf("got %d token requests, want 2", tokens.Load())
	}
	c.mu.Lock()
	c.expires = time.Now().Add(-time.Second)
	c.mu.Unlock()
	if err := c.pages(context.Background(), "/users", "users", nil, func(row) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if tokens.Load() != 3 {
		t.Fatal("expired token not renewed")
	}
}
func TestPaginationAndMalformedEnvelopes(t *testing.T) {
	for _, body := range []string{`{}`, `{"users":null}`, `{"users":{}}`, `{"users":[null]}`, `{"users":[],"next_page_token":4}`, `{"users":[],"next_page_token":"repeat"}`} {
		t.Run(body, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					tokenResponse(w)
					return
				}
				fmt.Fprint(w, body)
			})
			if err := c.pages(context.Background(), "/users", "users", nil, func(row) error { return nil }); err == nil {
				t.Fatal("accepted malformed or cycling page")
			}
		})
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			tokenResponse(w)
			return
		}
		if r.URL.Query().Get("next_page_token") == "" {
			fmt.Fprint(w, `{"users":[{"id":"one"}],"next_page_token":"next"}`)
		} else {
			fmt.Fprint(w, `{"users":[{"id":"two"}]}`)
		}
	})
	var ids []string
	if err := c.pages(context.Background(), "/users", "users", url.Values{"status": {"pending"}}, func(r row) error { ids = append(ids, r["id"].(string)); return nil }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "one,two" {
		t.Fatal(ids)
	}
}
func TestSafeErrorsRetriesAndCancellation(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					tokenResponse(w)
					return
				}
				calls.Add(1)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"message":"secret payload email@example.com"}`)
			})
			err := c.pages(context.Background(), "/users", "users", nil, func(row) error { return nil })
			if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "email") {
				t.Fatalf("unsafe/missing error %v", err)
			}
			if calls.Load() > 4 {
				t.Fatal("unbounded retries")
			}
		})
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			tokenResponse(w)
			return
		}
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(429)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.pages(ctx, "/users", "users", nil, func(row) error { return nil }); err == nil {
		t.Fatal("cancel ignored")
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancellation delayed")
	}
}
func TestTokenIsolation(t *testing.T) {
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			r.ParseForm()
			json.NewEncoder(w).Encode(map[string]any{"access_token": r.Form.Get("account_id"), "expires_in": 3600})
			return
		}
		got = append(got, r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"users":[]}`)
	}))
	defer server.Close()
	for _, account := range []string{"a", "b"} {
		cfg := config()
		cfg.AccountID = ptr(account)
		c, err := newClient(cfg)
		if err != nil {
			t.Fatal(err)
		}
		c.apiURL = server.URL
		c.tokenURL = server.URL + "/oauth/token"
		if err := c.pages(context.Background(), "/users", "users", nil, func(row) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(got, ",") != "Bearer a,Bearer b" {
		t.Fatal(got)
	}
}

func TestWaitingForTokenRespectsCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			close(entered)
			<-release
			tokenResponse(w)
			return
		}
		fmt.Fprint(w, `{"users":[]}`)
	})
	done := make(chan struct{})
	go func() { defer close(done); _, _ = c.accessToken(context.Background(), "") }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := make(chan error, 1)
	go func() { _, err := c.accessToken(ctx, ""); canceled <- err }()
	select {
	case err := <-canceled:
		if err != context.Canceled {
			t.Errorf("expected cancellation, got %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("token wait ignored cancellation")
	}
	close(release)
	<-done
}

func TestRequestsHaveBoundedConcurrency(t *testing.T) {
	entered := make(chan struct{}, 20)
	release := make(chan struct{})
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		fmt.Fprint(w, `{"users":[]}`)
	})
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			_, err := c.request(context.Background(), http.MethodGet, c.apiURL+"/users", nil, "token")
			if err != nil {
				t.Error(err)
			}
		})
	}
	for range 4 {
		<-entered
	}
	select {
	case <-entered:
		t.Error("more than four requests in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	wg.Wait()
}

func TestClientFormattingDoesNotExposeCredentials(t *testing.T) {
	c, err := newClient(config())
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%+v", "%s"} {
		if got := fmt.Sprintf(format, c); got != "Zoom client (credentials redacted)" {
			t.Fatal("client formatting did not redact credentials")
		}
	}
	cacheError := fmt.Errorf("An error has occurred while setting value '%v' on key '%v'", c, "connection-key")
	if cacheError.Error() != "An error has occurred while setting value 'Zoom client (credentials redacted)' on key 'connection-key'" {
		t.Fatal("SDK cache error did not redact credentials")
	}
}
