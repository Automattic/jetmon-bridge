package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- auth middleware ---

func TestAuthMiddleware_ValidToken(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := authMiddleware("s3cr3t", inner)

	req := httptest.NewRequest("GET", "/time", nil)
	req.Header.Set("Authorization", "Bearer s3cr3t")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("valid token: got %d, want 200", rec.Code)
	}
}

func TestAuthMiddleware_Unauthorized(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := authMiddleware("s3cr3t", inner)

	cases := []struct{ name, header string }{
		{"no header", ""},
		{"wrong token", "Bearer wrongtoken"},
		{"basic scheme", "Basic s3cr3t"},
		{"missing space", "Bearers3cr3t"},
		{"empty bearer", "Bearer "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/time", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("got %d, want 401", rec.Code)
			}
			assertJSONError(t, rec.Body, "unauthorized")
		})
	}
}

// --- GET /time ---

func TestHandleTime(t *testing.T) {
	req := httptest.NewRequest("GET", "/time", nil)
	rec := httptest.NewRecorder()
	handleTime(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("got %d, want 200", rec.Code)
	}
	assertContentType(t, rec)

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body["time"] == "" {
		t.Error("expected non-empty 'time' field in response")
	}
}

// --- GET /monitors ---

func TestHandleMonitors_MissingURL(t *testing.T) {
	h := handleMonitors(nil, time.Second)
	req := httptest.NewRequest("GET", "/monitors", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
	assertJSONError(t, rec.Body, "url parameter required")
}

func TestHandleMonitors_ContentTypeOnError(t *testing.T) {
	h := handleMonitors(nil, time.Second)
	req := httptest.NewRequest("GET", "/monitors", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	assertContentType(t, rec)
}

// --- POST /monitors ---

func TestHandleMonitorsPost_MalformedJSON(t *testing.T) {
	h := handleMonitorsPost(nil, 0, time.Second)
	req := httptest.NewRequest("POST", "/monitors", strings.NewReader("{bad json"))
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
	assertJSONError(t, rec.Body, "invalid request body")
}

func TestHandleMonitorsPost_EmptyBody(t *testing.T) {
	h := handleMonitorsPost(nil, 0, time.Second)
	req := httptest.NewRequest("POST", "/monitors", http.NoBody)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
}

func TestHandleMonitorsPost_MissingURLField(t *testing.T) {
	h := handleMonitorsPost(nil, 0, time.Second)
	req := httptest.NewRequest("POST", "/monitors", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
	assertJSONError(t, rec.Body, "url is required")
}

func TestHandleMonitorsPost_EmptyURL(t *testing.T) {
	h := handleMonitorsPost(nil, 0, time.Second)
	req := httptest.NewRequest("POST", "/monitors", strings.NewReader(`{"url":""}`))
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
	assertJSONError(t, rec.Body, "url is required")
}

// --- DELETE /monitors ---

func TestHandleMonitorsDelete_MissingURL(t *testing.T) {
	h := handleMonitorsDelete(nil, time.Second)
	req := httptest.NewRequest("DELETE", "/monitors", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
	assertJSONError(t, rec.Body, "url parameter required")
}

// --- GET /events ---

func TestHandleEvents_MissingParams(t *testing.T) {
	h := handleEvents(nil, time.Second)
	cases := []struct{ name, url string }{
		{"no params", "/events"},
		{"blog_id only", "/events?blog_id=1"},
		{"since and until only", "/events?since=2024-01-01T00:00:00Z&until=2024-01-02T00:00:00Z"},
		{"missing until", "/events?blog_id=1&since=2024-01-01T00:00:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", c.url, nil)
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: got %d, want 400", c.name, rec.Code)
			}
		})
	}
}

func TestHandleEvents_InvalidBlogID(t *testing.T) {
	h := handleEvents(nil, time.Second)
	req := httptest.NewRequest("GET", "/events?blog_id=notanumber&since=2024-01-01T00:00:00Z&until=2024-01-02T00:00:00Z", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
	assertJSONError(t, rec.Body, "invalid blog_id")
}

func TestHandleEvents_InvalidSince(t *testing.T) {
	h := handleEvents(nil, time.Second)
	req := httptest.NewRequest("GET", "/events?blog_id=1&since=not-a-date&until=2024-01-02T00:00:00Z", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
	assertJSONError(t, rec.Body, "invalid since")
}

func TestHandleEvents_InvalidUntil(t *testing.T) {
	h := handleEvents(nil, time.Second)
	req := httptest.NewRequest("GET", "/events?blog_id=1&since=2024-01-01T00:00:00Z&until=not-a-date", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
	assertJSONError(t, rec.Body, "invalid until")
}

// --- write mode routing ---

// TestWriteModeRouting_Returns405WhenDisabled verifies that Go 1.22's ServeMux
// returns 405 for POST and DELETE on /monitors when write routes are not registered.
// The uptime-bench adapter relies on this to fall back to read-only provisioning.
func TestWriteModeRouting_Returns405WhenDisabled(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /monitors", handleMonitors(nil, time.Second))
	// POST and DELETE not registered — simulates -write=false

	cases := []struct {
		method string
		body   io.Reader
	}{
		{"POST", strings.NewReader(`{"url":"https://example.com"}`)},
		{"DELETE", nil},
	}
	for _, c := range cases {
		t.Run(c.method, func(t *testing.T) {
			req := httptest.NewRequest(c.method, "/monitors?url=https://example.com", c.body)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s /monitors with write mode off: got %d, want 405", c.method, rec.Code)
			}
		})
	}
}

func TestWriteModeRouting_RoutesRegisteredWhenEnabled(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /monitors", handleMonitors(nil, time.Second))
	mux.HandleFunc("POST /monitors", handleMonitorsPost(nil, 0, time.Second))
	mux.HandleFunc("DELETE /monitors", handleMonitorsDelete(nil, time.Second))

	// POST with empty url returns 400 (not 405) — confirms the route is registered.
	req := httptest.NewRequest("POST", "/monitors", strings.NewReader(`{"url":""}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusMethodNotAllowed {
		t.Error("POST /monitors with write mode on returned 405 — route not registered")
	}
}

// --- helpers ---

func assertJSONError(t *testing.T, body io.Reader, wantSubstr string) {
	t.Helper()
	var resp map[string]string
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if !strings.Contains(resp["error"], wantSubstr) {
		t.Errorf("error field %q does not contain %q", resp["error"], wantSubstr)
	}
}

func assertContentType(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}
