package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, errBody("invalid request body"))
			return
		}
		if body.URL == "" {
			writeJSON(w, http.StatusBadRequest, errBody("url is required"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		m, created, err := createMonitor(ctx, db, body.URL, bucket)
		if err != nil {
			log.Printf("POST /monitors url=%q: %v", body.URL, err)
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

func handleEvents(db *sql.DB, timeout time.Duration) http.HandlerFunc {
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
		if err != nil {
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

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		events, err := lookupEvents(ctx, db, blogID, since, until)
		if err != nil {
			log.Printf("GET /events blog_id=%d: %v", blogID, err)
			writeJSON(w, http.StatusInternalServerError, errBody("internal server error"))
			return
		}

		writeJSON(w, http.StatusOK, events)
	}
}

func handleHealthz(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "error", "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// authMiddleware rejects requests that don't carry the expected Bearer token.
func authMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != token {
			writeJSON(w, http.StatusUnauthorized, errBody("unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func errBody(msg string) map[string]string {
	return map[string]string{"error": msg}
}
