package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func init() {
	flag.CommandLine.Usage = func() {
		fmt.Fprintf(os.Stderr, "jetmon-bridge %s\n\nUsage:\n", version)
		flag.PrintDefaults()
	}
}

func main() {
	showVersion := flag.Bool("version", false, "Print version and exit")
	dsn := flag.String("dsn", "", "MySQL DSN for the Jetmon read replica (required)")
	writeDSN := flag.String("write-dsn", "", "MySQL DSN for write operations (primary); required when -write is set")
	addr := flag.String("addr", "127.0.0.1:7400", "Listen address (host:port)")
	readTimeout := flag.Duration("read-timeout", 5*time.Second, "Per-request DB query timeout")
	write := flag.Bool("write", false, "Enable write endpoints: POST /monitors, DELETE /monitors")
	token := flag.String("token", "", "Bearer token for auth on all requests; empty disables auth")
	bucket := flag.Int("bucket", 0, "Jetmon bucket number assigned to new monitors (must match an active worker bucket)")
	historyPath := flag.String("history-path", envString("JETMON_HISTORY_PATH", ""), "SQLite file path for persistent event history; empty disables history")
	historyPoll := flag.Duration("history-poll-interval", envDuration("JETMON_HISTORY_POLL_INTERVAL", defaultHistoryPollInterval), "Polling interval for persistent history")
	historyBootstrap := flag.Bool("history-bootstrap", envBool("JETMON_HISTORY_BOOTSTRAP", true), "Poll once at startup to seed persistent history state")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "jetmon-bridge: -dsn is required")
		flag.Usage()
		os.Exit(1)
	}
	if *readTimeout <= 0 {
		fmt.Fprintln(os.Stderr, "jetmon-bridge: -read-timeout must be greater than 0")
		os.Exit(1)
	}
	db, err := openDB(*dsn)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close()

	// writeDB is the connection used for INSERT/UPDATE operations.
	// It should point at the primary, not the read replica.
	writeDB := db
	if *write {
		if *writeDSN == "" {
			log.Fatal("write mode requires -write-dsn pointing at the Jetmon primary")
		}
		wdb, err := openDB(*writeDSN)
		if err != nil {
			log.Fatalf("write-db: %v", err)
		}
		defer wdb.Close()
		writeDB = wdb
	}

	var history *historyStore
	var historyCancel context.CancelFunc
	if *historyPath != "" {
		if *historyPoll <= 0 {
			fmt.Fprintln(os.Stderr, "jetmon-bridge: -history-poll-interval must be greater than 0")
			os.Exit(1)
		}

		h, err := openHistoryStore(*historyPath)
		if err != nil {
			log.Fatalf("history-db: %v", err)
		}
		defer h.Close()
		history = h

		if *historyBootstrap {
			ctx, cancel := context.WithTimeout(context.Background(), *readTimeout)
			if err := history.poll(ctx, db, time.Now().UTC()); err != nil {
				log.Printf("history bootstrap: %v", err)
			}
			cancel()
		}

		var historyCtx context.Context
		historyCtx, historyCancel = context.WithCancel(context.Background())
		go runHistoryPoller(historyCtx, db, history, *historyPoll, *readTimeout)
		log.Printf("jetmon-bridge: persistent history enabled path=%q poll_interval=%s bootstrap=%t", *historyPath, historyPoll.String(), *historyBootstrap)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /time", handleTime)
	mux.HandleFunc("GET /monitors", handleMonitors(db, *readTimeout))
	if history != nil {
		mux.HandleFunc("GET /events", handleHistoryEvents(history, *readTimeout))
		mux.HandleFunc("GET /healthz", handleHealthzWithHistory(db, history, *readTimeout))
	} else {
		mux.HandleFunc("GET /events", handleEvents(db, *readTimeout))
		mux.HandleFunc("GET /healthz", handleHealthz(db, *readTimeout))
	}

	if *write {
		mux.HandleFunc("POST /monitors", handleMonitorsPost(writeDB, *bucket, *readTimeout))
		mux.HandleFunc("DELETE /monitors", handleMonitorsDelete(writeDB, *readTimeout))
		log.Printf("jetmon-bridge: write mode enabled (bucket=%d)", *bucket)
		if *token == "" {
			log.Println("WARNING: write mode is enabled without bearer token auth")
		}
		if *bucket == 0 {
			log.Println("WARNING: -bucket=0 is the default; verify this bucket is assigned to active Jetmon workers - monitors in an unowned bucket are never checked")
		}
	}

	var handler http.Handler = mux
	if *token != "" {
		handler = authMiddleware(*token, mux)
		log.Println("jetmon-bridge: bearer token auth enabled")
	}

	writeTimeout := *readTimeout + 5*time.Second
	if writeTimeout < 10*time.Second {
		writeTimeout = 10 * time.Second
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("jetmon-bridge %s listening on %s", version, *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down...")
	if historyCancel != nil {
		historyCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("server shutdown: %v", err)
	}
	log.Println("stopped")
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jetmon-bridge: invalid %s=%q, using %s\n", name, value, fallback)
		return fallback
	}
	return parsed
}

func envBool(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jetmon-bridge: invalid %s=%q, using %t\n", name, value, fallback)
		return fallback
	}
	return parsed
}
