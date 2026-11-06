package main

import (
	"apphub-rewards/internal/config"
	"apphub-rewards/internal/storage"
	"apphub-rewards/internal/telegram"
	"context"
	"fmt"
	tg "github.com/go-telegram/bot"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e := run(ctx); e != nil {
		slog.Error("application stopped", "error", e.Error())
		os.Exit(1)
	}
}
func run(parent context.Context) error {
	c, e := config.Load()
	if e != nil {
		return e
	}
	ctx, stop := context.WithCancel(parent)
	defer stop()
	s, e := storage.Open(ctx, c.Database, c.Store, c.Register)
	if e != nil {
		return fmt.Errorf("database configuration invalid")
	}
	defer s.Pool.Close()
	ready, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for {
		if e = s.Pool.Ping(ready); e == nil {
			break
		}
		select {
		case <-ready.Done():
			return fmt.Errorf("database unavailable")
		case <-time.After(2 * time.Second):
		}
	}
	if e = s.Migrate(ctx); e != nil {
		return fmt.Errorf("migration failed: %w", e)
	}
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		slog.Info("migrations applied")
		return nil
	}
	if c.Token == "" {
		return fmt.Errorf("BOT_TOKEN is required")
	}
	for _, adminID := range c.AdminIDs {
		if e = s.Bootstrap(ctx, adminID); e != nil {
			return e
		}
	}
	// Session lock is held on a dedicated connection; loss stops polling before reconnect.
	lock, e := s.Pool.Acquire(ctx)
	if e != nil {
		return e
	}
	defer lock.Release()
	var acquired bool
	e = lock.QueryRow(ctx, "SELECT pg_try_advisory_lock(748120021)").Scan(&acquired)
	if e != nil || !acquired {
		return fmt.Errorf("another polling instance holds database lock")
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		lock.Exec(releaseCtx, "SELECT pg_advisory_unlock(748120021)")
	}()
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				probe, cancel := context.WithTimeout(ctx, 2*time.Second)
				e := lock.Conn().Ping(probe)
				cancel()
				if e != nil {
					slog.Error("polling lock connection lost; stopping")
					stop()
					return
				}
			}
		}
	}()
	defer func() { stop(); <-monitorDone }()
	a, e := telegram.New(s, c)
	if e != nil {
		return e
	}
	b, e := tg.New(c.Token, tg.WithDefaultHandler(a.Handle), tg.WithWorkers(1), tg.WithNotAsyncHandlers(), tg.WithErrorsHandler(func(error) { slog.Warn("telegram request failed") }))
	if e != nil {
		return fmt.Errorf("Telegram initialization failed; check token and network")
	}
	me, e := b.GetMe(ctx)
	if e != nil {
		return fmt.Errorf("Telegram getMe failed")
	}
	a.Username = me.Username
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); a.Worker(ctx, b) }()
	slog.Info("AppHub started")
	b.Start(ctx)
	stop()
	<-workerDone
	return nil
}
