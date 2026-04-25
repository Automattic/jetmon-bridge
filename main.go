package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	dsn         := flag.String("dsn",          "",               "MySQL DSN for the Jetmon read replica (required)")
	writeDSN    := flag.String("write-dsn",    "",               "MySQL DSN for write operations (primary); required when -write is set")
	addr        := flag.String("addr",         "127.0.0.1:7400", "Listen address (host:port)")
	readTimeout := flag.Duration("read-timeout", 5*time.Second,  "Per-request DB query timeout")
	write       := flag.Bool("write",          false,            "Enable write endpoints: POST /monitors, DELETE /monitors")
	token       := flag.String("token",        "",               "Bearer token for auth on all requests; empty disables auth")
	bucket      := flag.Int("bucket",          0,                "Jetmon bucket number assigned to new monitors (must match an active worker bucket)")
	flag.Parse()

	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "jetmon-bridge: -dsn is required")
		flag.Usage()
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
			log.Println("WARNING: -write is set but -write-dsn is not — writes will use the read DSN; ensure it is the primary, not a read replica")
		} else {
			wdb, err := openDB(*writeDSN)
			if err != nil {
				log.Fatalf("write-db: %v", err)
			}
			defer wdb.Close()
			writeDB = wdb
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /time", handleTime)
	mux.HandleFunc("GET /monitors", handleMonitors(db, *readTimeout))
	mux.HandleFunc("GET /events", handleEvents(db, *readTimeout))
	mux.HandleFunc("GET /healthz", handleHealthz(db, *readTimeout))

	if *write {
		mux.HandleFunc("POST /monitors", handleMonitorsPost(writeDB, *bucket, *readTimeout))
		mux.HandleFunc("DELETE /monitors", handleMonitorsDelete(writeDB, *readTimeout))
		log.Printf("jetmon-bridge: write mode enabled (bucket=%d)", *bucket)
		if *bucket == 0 {
			log.Println("WARNING: -bucket=0 is the default; verify this bucket is assigned to active Jetmon workers — monitors in an unowned bucket are never checked")
		}
	}

	var handler http.Handler = mux
	if *token != "" {
		handler = authMiddleware(*token, mux)
		log.Println("jetmon-bridge: bearer token auth enabled")
	}

	srv := &http.Server{
		Addr:    *addr,
		Handler: handler,
	}

	go func() {
		log.Printf("jetmon-bridge listening on %s", *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("server shutdown: %v", err)
	}
	log.Println("stopped")
}
