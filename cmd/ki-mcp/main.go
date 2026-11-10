// Command ki-mcp serves the kio knowledge base over MCP (streamable
// HTTP). It sits behind the cluster ingress on a private hostname that only
// the operator's VPN can reach; every request also carries a device token.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kislow/kio/internal/kb"
	"github.com/kislow/kio/internal/server"
	"github.com/kislow/kio/pkg/config"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// shutdownGrace is how long in-flight requests get after SIGTERM.
const shutdownGrace = 20 * time.Second

// main only translates run's result into an exit code, so deferred cleanup always runs.
func main() {
	os.Exit(realMain())
}

func realMain() int {
	cfgPath := flag.String("config", "/etc/ki/config.json", "path to config file")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger, *cfgPath); err != nil {
		logger.Error("service failed", "err", err)
		return 1
	}
	return 0
}

// run serves until ctx is cancelled, then shuts the server down gracefully.
func run(ctx context.Context, logger *slog.Logger, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	tokens, err := config.LoadTokens(cfg.TokensFile, cfg)
	if err != nil {
		return fmt.Errorf("loading tokens: %w", err)
	}

	srv := server.New(cfg, kb.NewClient(), logger, version)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	mux := http.NewServeMux()
	// The SDK applies no cross-origin protection by default since v1.6.0, so reject
	// cross-origin browser requests here.
	mux.Handle("/mcp", http.NewCrossOriginProtection().Handler(auth.RequireBearerToken(tokens.Verifier(), nil)(mcpHandler)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// Prometheus metrics for the cluster only: the IngressRoute exposes /mcp and
	// /healthz, so nothing outside the cluster reaches this path.
	mux.Handle("GET /metrics", server.MetricsHandler())

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// A tool call fans out to every brain's kb-query; give it room, but bound it.
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Listen, "brains", len(cfg.Brains), "version", version)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("serving http: %w", err)
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	// ctx is already cancelled here, so detach from its cancellation but keep its values.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down http: %w", err)
	}
	if err := <-serveErr; err != nil {
		return err
	}
	logger.Info("shutdown complete")
	return nil
}
