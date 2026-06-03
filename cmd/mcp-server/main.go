package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gosom/google-maps-scraper/cmd/mcp-server/mcpauth"
	"github.com/gosom/google-maps-scraper/cmd/mcp-server/tools"
	pkgconfig "github.com/gosom/google-maps-scraper/pkg/config"
	"github.com/gosom/google-maps-scraper/postgres"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	serverName    = "brezel-mcp"
	serverVersion = "0.1.0"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := pkgconfig.Load()
	if err != nil {
		slog.Error("config_load_failed", "err", err)
		os.Exit(1)
	}

	if len(cfg.APIKeyServerSecret) < 32 {
		slog.Error("api_key_server_secret_too_short",
			"detail", "API_KEY_SERVER_SECRET must be at least 32 bytes for API key auth",
			"got_bytes", len(cfg.APIKeyServerSecret),
		)
		os.Exit(1)
	}

	// Independent background context for init — the signal-derived ctx is
	// scoped to the server loop's lifetime and must not gate startup.
	initCtx, initCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer initCancel()

	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		slog.Error("db_open_failed", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	if cfg.DB.MaxOpenConns == 0 {
		slog.Error("db_pool_misconfigured", "detail", "DB_MAX_OPEN_CONNS=0 creates unbounded pool")
		os.Exit(1)
	}
	db.SetMaxOpenConns(cfg.DB.MaxOpenConns)
	db.SetMaxIdleConns(cfg.DB.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.DB.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.DB.ConnMaxIdleTime)

	if err := db.PingContext(initCtx); err != nil {
		slog.Error("db_ping_failed", "err", err)
		os.Exit(1)
	}

	apiKeyAdapter := &mcpauth.APIKeyAdapter{
		ServerSecret: cfg.APIKeyServerSecret,
		Repo:         postgres.NewAPIKeyRepository(db),
	}

	prmURL := cfg.MCP.PublicURL + "/.well-known/oauth-protected-resource"
	authMW := mcpauth.New(mcpauth.Config{
		APIKey:              apiKeyAdapter,
		OAuth:               nil,
		ResourceMetadataURL: prmURL,
	})

	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, nil)
	mcp.AddTool(mcpServer, tools.PingTool(), tools.Ping)

	streamableHandler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
		return mcpServer
	}, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.Handle("/mcp", authMW(streamableHandler))
	mux.Handle("/mcp/", authMW(streamableHandler))

	// Streamable HTTP uses long-lived SSE responses, so WriteTimeout is
	// intentionally generous; ReadHeaderTimeout/IdleTimeout/MaxHeaderBytes
	// match the house style at web/web.go.
	srv := &http.Server{
		Addr:              cfg.MCP.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("mcp-server listening", "addr", cfg.MCP.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen failed", "err", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown_error", "err", err)
	}
	slog.Info("mcp-server stopped")
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
