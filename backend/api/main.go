package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/example/yamanote-api/train"
)

// defaultODPTConsumerKey is the project's public ODPT key. Deployments may
// supply ODPT_CONSUMER_KEY to use their own key instead.
const defaultODPTConsumerKey = "28ec545d2613c4859a12744a384849328606b15bb494fbd0b827d0633776e63c"

func odptConsumerKey() string {
	if key := strings.TrimSpace(os.Getenv("ODPT_CONSUMER_KEY")); key != "" {
		return key
	}
	return defaultODPTConsumerKey
}

func duration(name string, fallback time.Duration) time.Duration {
	if raw := os.Getenv(name); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			return d
		}
		log.Printf("invalid %s=%q; using %s", name, raw, fallback)
	}
	return fallback
}
func main() {
	poller, err := train.NewPoller(train.Config{ConsumerKey: odptConsumerKey(), Interval: duration("ODPT_POLL_INTERVAL", 30*time.Second), HTTPTimeout: duration("ODPT_HTTP_TIMEOUT", 10*time.Second)})
	if err != nil {
		log.Fatal(err)
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	server := &http.Server{Addr: addr, Handler: train.NewServer(poller, os.Getenv("CORS_ALLOWED_ORIGINS")).Handler(), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go poller.Run(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("listening on %s", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
