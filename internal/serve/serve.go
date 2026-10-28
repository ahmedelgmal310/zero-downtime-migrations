// Package serve runs the API process: schema compatibility gate, fleet
// heartbeat and a graceful shutdown that does not drop in-flight requests.
package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/api"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/db"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/fleet"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/lockwatch"
	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/store"
)

// Config for the API process.
type Config struct {
	Addr              string
	Version           string
	DSN               string
	InstanceID        string
	MaxConns          int32
	StatementTimeout  time.Duration
	HeartbeatInterval time.Duration
	SchemaPoll        time.Duration
	DrainDelay        time.Duration // keep serving after SIGTERM, with keep-alives off
	ShutdownTimeout   time.Duration
}

// Defaults returns the configuration used in the chart.
func Defaults() Config {
	return Config{
		Addr:              ":8080",
		MaxConns:          10,
		StatementTimeout:  5 * time.Second,
		HeartbeatInterval: 5 * time.Second,
		SchemaPoll:        5 * time.Second,
		DrainDelay:        3 * time.Second,
		ShutdownTimeout:   15 * time.Second,
	}
}

// Run serves until ctx is cancelled (SIGTERM), then drains and exits.
// ln may be nil, in which case cfg.Addr is used.
func Run(ctx context.Context, cfg Config, ln net.Listener, log *slog.Logger) error {
	pool, err := db.Open(ctx, cfg.DSN, db.Options{
		AppName: lockwatch.AppName, StatementTimeout: cfg.StatementTimeout, MaxConns: cfg.MaxConns,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	st, err := store.New(cfg.Version, pool)
	if err != nil {
		return err
	}
	srv := &api.Server{Version: cfg.Version, Store: st, Log: log}

	bgCtx, stopBG := context.WithCancel(context.WithoutCancel(ctx))
	defer stopBG()
	go fleet.Run(bgCtx, pool, cfg.InstanceID, cfg.Version, cfg.HeartbeatInterval, log)
	go watchSchema(bgCtx, pool, srv, st.RequiredColumns(), cfg.SchemaPoll, log)

	if ln == nil {
		var lc net.ListenConfig
		ln, err = lc.Listen(ctx, "tcp", cfg.Addr)
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
	}
	hs := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	log.Info("serving", "addr", ln.Addr().String(), "version", cfg.Version, "instance", cfg.InstanceID)

	select {
	case err := <-errc:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	// Graceful shutdown. Kubernetes removes the pod from the Service endpoints
	// while the preStop sleep runs; requests still arriving on existing
	// keep-alive connections are served, and with keep-alives disabled each
	// response carries "Connection: close", so clients reconnect to a pod that
	// is not shutting down. Only then are idle connections closed.
	log.Info("draining", "delay", cfg.DrainDelay.String())
	srv.Drain()
	hs.SetKeepAlivesEnabled(false)
	time.Sleep(cfg.DrainDelay)
	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()
	if err := hs.Shutdown(shutCtx); err != nil {
		log.Error("shutdown", "err", err)
	}
	stopBG()
	if err := fleet.Leave(shutCtx, pool, cfg.InstanceID); err != nil {
		log.Warn("leave fleet registry", "err", err)
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("stopped")
	return nil
}

// watchSchema keeps the pod unready while the schema lacks a column this
// release needs. Deploying v2 before the expand migration therefore stalls
// the rollout instead of failing requests, and a pod whose column disappears
// stops receiving traffic.
func watchSchema(ctx context.Context, pool *pgxpool.Pool, srv *api.Server, required []string, every time.Duration, log *slog.Logger) {
	compatible := false
	for {
		missing, err := store.MissingColumns(ctx, pool, required)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				log.Warn("schema check failed", "err", err)
			}
		case len(missing) > 0:
			if compatible || !srv.Ready() {
				log.Warn("schema incompatible, not ready", "missing", missing)
			}
			compatible = false
			srv.SetReady(false)
		case !compatible:
			log.Info("schema compatible, ready", "required", required)
			compatible = true
			srv.SetReady(true)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
