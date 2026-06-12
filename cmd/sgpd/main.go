package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c" //nolint:staticcheck // required for ConnectRPC h2c interop

	"github.com/restrukt-ai/sessiongraphprotocol/gen/sgp/v1/sgpv1connect"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/store/pg"
)

const readHeaderTimeout = 30 * time.Second

func main() {
	err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sgpd: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Pool: every connection installs AGE.
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("parse database url: %w", err)
	}

	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `LOAD 'age'; SET search_path = ag_catalog, "$user", public`)

		return err
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return fmt.Errorf("create pool: %w", err)
	}
	defer pool.Close()

	// Migrations (SQL via goose + AGE graph init via pool).
	err = pg.Migrate(ctx, cfg.DatabaseURL, pool)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// Notify broker + store.
	broker, err := pg.NewNotifyBroker(ctx, cfg.DatabaseURL, pool)
	if err != nil {
		return fmt.Errorf("notify broker: %w", err)
	}
	defer broker.Close(context.Background())

	go func() {
		err := broker.Run(ctx)
		if err != nil {
			slog.Error("notify broker exited", "err", err)
		}
	}()

	store := pg.NewStore(pool, broker)

	hServer := buildHarnessServer(cfg, store)
	mServer := buildManagementServer(cfg, store)

	go startHarnessServer(hServer, cfg.HarnessAddr)
	go startManagementServer(mServer, cfg)

	<-ctx.Done()
	slog.Info("shutting down")
	hServer.Shutdown(context.Background()) //nolint:errcheck
	mServer.Shutdown(context.Background()) //nolint:errcheck

	return nil
}

func buildHarnessServer(cfg config, store *pg.Store) *http.Server {
	harnessOpts := []connect.HandlerOption{
		connect.WithInterceptors(newBearerInterceptor(cfg.HarnessToken)),
	}
	hMux := http.NewServeMux()
	hMux.Handle(
		sgpv1connect.NewSGPHarnessServiceHandler(&harnessHandler{store: store}, harnessOpts...),
	)

	return &http.Server{
		Addr:              cfg.HarnessAddr,
		Handler:           h2c.NewHandler(hMux, &http2.Server{}), //nolint:staticcheck
		ReadHeaderTimeout: readHeaderTimeout,
	}
}

func buildManagementServer(cfg config, store *pg.Store) *http.Server {
	mgmtOpts := []connect.HandlerOption{
		connect.WithInterceptors(newBearerInterceptor(cfg.ManagementToken)),
	}
	mMux := http.NewServeMux()
	mMux.Handle(
		sgpv1connect.NewSGPManagementServiceHandler(&managementHandler{store: store}, mgmtOpts...),
	)

	return &http.Server{
		Addr:              cfg.ManagementAddr,
		Handler:           mMux,
		ReadHeaderTimeout: readHeaderTimeout,
	}
}

func startHarnessServer(srv *http.Server, addr string) {
	slog.Info("harness listener", "addr", addr)

	err := srv.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("harness server", "err", err)
	}
}

func startManagementServer(srv *http.Server, cfg config) {
	slog.Info("management listener", "addr", cfg.ManagementAddr, "tls", cfg.TLSCert != "")

	var err error
	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		err = srv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
	} else {
		err = srv.ListenAndServe()
	}

	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("management server", "err", err)
	}
}
