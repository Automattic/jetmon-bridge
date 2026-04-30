package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxMonitorRequestBodyBytes = 4096

func handleTime(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]time.Time{
		"time": time.Now().UTC(),
	})
}

func handleMonitors(db *sql.DB, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		url := r.URL.Query().Get("url")
		if url == "" {
			writeJSON(w, http.StatusBadRequest, errBody("url parameter required"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		m, err := lookupMonitor(ctx, db, url)
		if err != nil {
			log.Printf("GET /monitors url=%q: %v", url, err)
			writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
			return
		}
		if m == nil {
			writeJSON(w, http.StatusNotFound, errBody("not found"))
			return
		}
		writeJSON(w, http.StatusOK, m)
	}
}

func handleMonitorsPost(db *sql.DB, bucket int, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxMonitorRequestBodyBytes)

		var body struct {
			URL string `json:"url"`
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("invalid request body"))
			return
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			writeJSON(w, http.StatusBadRequest, errBody("invalid request body"))
			return
		}
		monitorURL, err := validateMonitorURL(body.URL)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err.Error()))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		m, created, err := createMonitor(ctx, db, monitorURL, bucket)
		if err != nil {
			log.Printf("POST /monitors url=%q: %v", monitorURL, err)
			writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
			return
		}

		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, m)
	}
}

func handleMonitorsDelete(db *sql.DB, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		monitorURL := r.URL.Query().Get("url")
		if monitorURL == "" {
			writeJSON(w, http.StatusBadRequest, errBody("url parameter required"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		found, err := deactivateMonitor(ctx, db, monitorURL)
		if err != nil {
			log.Printf("DELETE /monitors url=%q: %v", monitorURL, err)
			writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, errBody("not found"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type eventLookupFunc func(ctx context.Context, blogID int64, since, until time.Time) ([]event, error)

func handleEvents(db *sql.DB, timeout time.Duration) http.HandlerFunc {
	return handleEventsLookup(timeout, func(ctx context.Context, blogID int64, since, until time.Time) ([]event, error) {
		return lookupEvents(ctx, db, blogID, since, until)
	})
}

func handleHistoryEvents(history *historyStore, timeout time.Duration) http.HandlerFunc {
	return handleEventsLookup(timeout, history.lookupEvents)
}

func handleEventsLookup(timeout time.Duration, lookup eventLookupFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		blogIDStr := q.Get("blog_id")
		sinceStr := q.Get("since")
		untilStr := q.Get("until")

		if blogIDStr == "" || sinceStr == "" || untilStr == "" {
			writeJSON(w, http.StatusBadRequest, errBody("blog_id, since, and until parameters required"))
			return
		}

		blogID, err := strconv.ParseInt(blogIDStr, 10, 64)
		if err != nil || blogID <= 0 {
			writeJSON(w, http.StatusBadRequest, errBody("invalid blog_id"))
			return
		}

		since, err := time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("invalid since: must be RFC3339"))
			return
		}

		until, err := time.Parse(time.RFC3339, untilStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("invalid until: must be RFC3339"))
			return
		}
		if !since.Before(until) {
			writeJSON(w, http.StatusBadRequest, errBody("since must be before until"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		events, err := lookup(ctx, blogID, since, until)
		if err != nil {
			log.Printf("GET /events blog_id=%d: %v", blogID, err)
			writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
			return
		}

		writeJSON(w, http.StatusOK, events)
	}
}

func handleHealthz(db *sql.DB, timeout time.Duration) http.HandlerFunc {
	return handleHealthzWithHistory(db, nil, timeout)
}

func handleHealthzWithHistory(db *sql.DB, history *historyStore, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "error": err.Error()})
			return
		}
		body := map[string]string{"status": "ok"}
		if history != nil {
			if err := history.PingContext(ctx); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"status":  "error",
					"error":   err.Error(),
					"history": "error",
				})
				return
			}
			body["history"] = "ok"
		}
		writeJSON(w, http.StatusOK, body)
	}
}

// authMiddleware rejects requests that don't carry the expected Bearer token.
func authMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, errBody("unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validateMonitorURL(raw string) (string, error) {
	monitorURL := strings.TrimSpace(raw)
	if monitorURL == "" {
		return "", fmt.Errorf("url is required")
	}
	u, err := url.Parse(monitorURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("url must be an absolute http or https URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("url must use http or https")
	}
	if u.User != nil {
		return "", fmt.Errorf("url must not include credentials")
	}
	if u.Fragment != "" {
		return "", fmt.Errorf("url must not include a fragment")
	}
	return monitorURL, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func errBody(msg string) map[string]string {
	return map[string]string{"error": msg}
}
