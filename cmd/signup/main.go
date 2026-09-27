// Command signup runs the sign-up sheet server.
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

	"github.com/hews/signup/internal/config"
	"github.com/hews/signup/internal/db"
	"github.com/hews/signup/internal/logx"
	"github.com/hews/signup/internal/web"
)

// version is set at build time: -ldflags '-X main.version=v1.2.3'.
var version = "dev"

func main() {
	if wantsVersion(os.Args[1:]) {
		fmt.Println(version)
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "signup:", err)
		os.Exit(1)
	}
}

// wantsVersion reports whether the arguments ask only for the version.
func wantsVersion(args []string) bool {
	return len(args) == 1 && (args[0] == "-version" || args[0] == "--version")
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	logger := logx.New(os.Stdout, cfg.LogLevel)
	slog.SetDefault(logger)

	database, err := db.Open(cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	if err := db.Migrate(context.Background(), database); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           web.Handler(database, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Listen, "version", version)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
