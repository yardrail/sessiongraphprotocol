package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/restrukt-ai/sessiongraphprotocol/gen/sgp/v1/sgpv1connect"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/store/pg"
)

const readHeaderTimeout = 30 * time.Second

func main() {
	err := newRootCmd().Execute()
	if err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "sgpd",
		Short:        "Session Graph Protocol daemon",
		RunE:         runServe,
		SilenceUsage: true,
	}

	f := cmd.Flags()
	f.String("database-url", "", "PostgreSQL connection URL")
	f.String("harness-addr", ":9090", "Harness server listen address")
	f.String("harness-token", "", "Bearer token for harness auth")
	f.String("management-addr", ":9091", "Management server listen address")
	f.String("management-token", "", "Bearer token for management auth")
	f.String("tls-cert", "", "TLS certificate file")
	f.String("tls-key", "", "TLS key file")

	return cmd
}

func runServe(cmd *cobra.Command, _ []string) error {
	v := viper.New()
	v.SetEnvPrefix("SGPD")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	v.AutomaticEnv()

	err := v.BindPFlags(cmd.Flags())
	if err != nil {
		return fmt.Errorf("bind flags: %w", err)
	}

	cfg, err := loadConfig(v)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	return run(cfg)
}

func run(cfg config) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("create pool: %w", err)
	}
	defer pool.Close()

	err = pg.Migrate(ctx, cfg.DatabaseURL)
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

	err = hServer.Shutdown(context.Background())
	if err != nil {
		slog.Error("harness server shutdown", "err", err)
	}

	err = mServer.Shutdown(context.Background())
	if err != nil {
		slog.Error("management server shutdown", "err", err)
	}

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

	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)

	return &http.Server{
		Addr:              cfg.HarnessAddr,
		Handler:           hMux,
		Protocols:         &p,
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
