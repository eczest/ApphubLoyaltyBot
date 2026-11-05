package main

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestStartupSingletonAndShutdown(t *testing.T) {
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	p, e := pgxpool.New(ctx, u)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	schema := fmt.Sprintf("startup_%d", time.Now().UnixNano())
	if _, e = p.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	defer p.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	parsed, e := url.Parse(u)
	if e != nil {
		t.Fatal(e)
	}
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	t.Setenv("DATABASE_URL", parsed.String())
	t.Setenv("BOT_TOKEN", "local-test-token")
	t.Setenv("ADMIN_TELEGRAM_ID", "1")
	started := make(chan struct{}, 1)
	original := http.DefaultTransport
	http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			select {
			case started <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":123,"is_bot":true,"first_name":"AppHub","username":"apphub_test"}}`))}, nil
	})
	defer func() { http.DefaultTransport = original }()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(runCtx) }()
	select {
	case <-started:
	case e := <-done:
		t.Fatalf("startup: %v", e)
	case <-time.After(10 * time.Second):
		t.Fatal("startup timed out")
	}
	if e = run(ctx); e == nil || !strings.Contains(e.Error(), "another polling") {
		t.Fatal("singleton", e)
	}
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
}
