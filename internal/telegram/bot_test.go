package telegram

import (
	"apphub-rewards/internal/config"
	"apphub-rewards/internal/storage"
	"context"
	"fmt"
	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTelegramFlows(t *testing.T) {
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, e := pgxpool.New(ctx, u)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("telegram_%d", time.Now().UnixNano())
	if _, e = pool.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(u)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	p, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { p.Close(); pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); pool.Close() }()
	s := &storage.Store{Pool: p, StoreID: "main", RegisterID: "main"}
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(s.Migrate(ctx))
	must(s.Bootstrap(ctx, 1))
	must(s.SetEmployee(ctx, 1, 2, true))
	var messages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		must(r.ParseMultipartForm(1024 * 1024))
		messages = append(messages, r.FormValue("text"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":10,"type":"private"}}}`)
	}))
	defer server.Close()
	b, e := tg.New("test", tg.WithSkipGetMe(), tg.WithServerURL(server.URL))
	must(e)
	a := &App{S: s, C: config.Config{Contacts: "https://t.me/apphub_store"}, Username: "apphub_test"}
	send := func(id int64, text string) { t.Helper(); must(a.handle(ctx, b, id, text, nil, false)) }
	send(10, "/start")
	if s.Dialog(ctx, 10).Step != "contact" {
		t.Fatal("phone registration must be available immediately")
	}
	if e = a.handle(ctx, b, 10, "+79990000010", nil, false); e == nil {
		t.Fatal("unverified text phone accepted")
	}
	if e = a.handle(ctx, b, 10, "", &models.Contact{UserID: 11, PhoneNumber: "+79990000010"}, false); e == nil {
		t.Fatal("foreign contact")
	}
	must(a.handle(ctx, b, 10, "", &models.Contact{UserID: 10, PhoneNumber: "+79990000010"}, false))
	var hasConsent bool
	must(s.Pool.QueryRow(ctx, "SELECT consent_version IS NOT NULL OR consent_at IS NOT NULL FROM customers WHERE telegram_id=10").Scan(&hasConsent))
	if hasConsent {
		t.Fatal("phone sharing must not be recorded as consent")
	}
	if e = a.handle(ctx, b, 10, "privacy", nil, true); e == nil {
		t.Fatal("removed conditions still available")
	}
	c, e := s.Customer(ctx, 10)
	must(e)
	for _, button := range []string{"🎁 Мои бонусы", "👥 Приведи друга", "📜 История операций", "📞 Контакты"} {
		send(10, button)
	}
	if e = a.handle(ctx, b, 10, "⚙️ Настройки", nil, false); e == nil {
		t.Fatal("customer admin access")
	}
	send(1, "/start")
	send(1, "📱 Регистрация по номеру")
	if s.Dialog(ctx, 1).Step != "contact" {
		t.Fatal("admin phone registration unavailable")
	}
	must(a.handle(ctx, b, 1, "", &models.Contact{UserID: 1, PhoneNumber: "+79990000001"}, false))
	send(2, "🔎 Найти клиента")
	send(2, c.Phone)
	send(2, "➕ Начислить бонусы")
	if s.Dialog(ctx, 2).Step != "award" {
		t.Fatal("amount of bonuses should be requested immediately")
	}
	send(2, "400")
	if e = a.handle(ctx, b, 2, "400", nil, false); e != nil {
		t.Fatal(e)
	} // repeated message cannot grant again
	c, e = s.Customer(ctx, 10)
	must(e)
	if c.Balance != 400 {
		t.Fatal(c.Balance)
	}
	send(2, "➖ Списать бонусы")
	send(2, c.Phone)
	send(2, "1000")
	send(2, "300")
	d := s.Dialog(ctx, 2)
	purchase := d.Data["purchase"]
	send(10, "approve:"+purchase)
	send(2, "✅ Завершить списание")
	send(2, "sale:"+purchase)
	c, e = s.Customer(ctx, 10)
	must(e)
	if c.Balance != 100 || c.Reserved != 0 {
		t.Fatal(c)
	}
	for _, removed := range []string{"⚙️ Настройки", "ℹ️ Правила программы", "🔐 Восстановление", "💳 Моя карта", "🛠 Корректировка"} {
		if e = a.handle(ctx, b, 1, removed, nil, false); e == nil {
			t.Fatal("removed action still active", removed)
		}
	}
	send(1, "👔 Сотрудники")
	send(1, "12345 1")
	first := s.Dialog(ctx, 1).Data["nonce"]
	send(1, "/cancel")
	send(1, "👔 Сотрудники")
	send(1, "12346 1")
	if e = a.handle(ctx, b, 1, "admin_apply:"+first, nil, true); e == nil {
		t.Fatal("stale confirmation accepted")
	}
	send(1, "admin_apply:"+s.Dialog(ctx, 1).Data["nonce"])
	before := len(messages)
	a.Handle(ctx, b, &models.Update{Message: &models.Message{From: &models.User{ID: 10}, Chat: models.Chat{ID: -100, Type: "group"}, Text: "🎁 Мои бонусы"}})
	if len(messages) != before {
		t.Fatal("group response leaks private data")
	}
	send(10, "🎁 Мои бонусы")
	last := messages[len(messages)-1]
	for _, hidden := range []string{"Кешбэк", "В резерве", "Ожидают"} {
		if strings.Contains(last, hidden) {
			t.Fatal("balance contains removed fields", last)
		}
	}
	if !strings.Contains(last, "Доступно: 100") || !strings.Contains(last, "50%") {
		t.Fatal(last)
	}
}
