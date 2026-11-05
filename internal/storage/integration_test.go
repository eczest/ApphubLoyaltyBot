package storage

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store { return newTestStore(t, true) }
func newTestStore(t *testing.T, migrate bool) *Store {
	t.Helper()
	u := os.Getenv("TEST_DATABASE_URL")
	if u == "" {
		t.Skip("TEST_DATABASE_URL not set (requires disposable PostgreSQL database)")
	}
	ctx := context.Background()
	pool, e := pgxpool.New(ctx, u)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
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
	s := &Store{p, "main", "main"}
	t.Cleanup(func() { p.Close(); pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); pool.Close() })
	if !migrate {
		return s
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal("migration idempotence", e)
	}
	if e = s.Bootstrap(ctx, 1); e != nil {
		t.Fatal(e)
	}
	if e = s.SetEmployee(ctx, 1, 2, true); e != nil {
		t.Fatal(e)
	}
	return s
}
func reg(t *testing.T, s *Store, id int64, ref string) Customer {
	t.Helper()
	c, e := s.Register(context.Background(), id, id, fmt.Sprintf("+7999%07d", id), "v1", ref)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func balance(t *testing.T, s *Store, tg int64, want int64) {
	t.Helper()
	c, e := s.Customer(context.Background(), tg)
	must(t, e)
	if c.Balance != want {
		t.Fatalf("balance %d want %d", c.Balance, want)
	}
}
func TestRegistrationAndRoles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := reg(t, s, 10, "")
	again := reg(t, s, 10, "")
	if c.ID != again.ID {
		t.Fatal("duplicate")
	}
	if _, e := s.Register(ctx, 11, 10, "+79990000011", "v1", ""); e == nil {
		t.Fatal("foreign contact")
	}
	if _, e := s.Register(ctx, 11, 11, c.Phone, "v1", ""); e == nil {
		t.Fatal("phone reused")
	}
	if s.Role(ctx, 10) != "" || s.Role(ctx, 2) != "EMPLOYEE" || s.Role(ctx, 1) != "ADMIN" {
		t.Fatal("roles")
	}
	if _, e := s.CreatePurchase(ctx, 10, c.ID, 100, 0); e == nil {
		t.Fatal("customer sale")
	}
	if e := s.SetEmployee(ctx, 2, 20, true); e == nil {
		t.Fatal("employee grants roles")
	}
	if e := s.SetSettings(ctx, 2, Settings{5, 50, 500, true}); e == nil {
		t.Fatal("employee settings")
	}
	must(t, s.SetEmployee(ctx, 1, 2, false))
	if _, e := s.CreatePurchase(ctx, 2, c.ID, 100, 0); e == nil {
		t.Fatal("blocked employee")
	}
	must(t, s.SetEmployee(ctx, 1, 2, true))
	must(t, s.SetDialog(ctx, 10, "contact", map[string]string{"version": "v1"}))
	reopened, e := Open(ctx, os.Getenv("TEST_DATABASE_URL"), "main", "main")
	must(t, e)
	reopened.Pool.Close()
	cfg := s.Pool.Config()
	second, e := pgxpool.NewWithConfig(ctx, cfg)
	must(t, e)
	defer second.Close()
	other := &Store{second, "main", "main"}
	got, e := other.Customer(ctx, 10)
	must(t, e)
	if got.Card != c.Card || other.Dialog(ctx, 10).Step != "contact" {
		t.Fatal("restart persistence")
	}
	must(t, s.Recover(ctx, 1, c.ID, 12, "Паспорт и чек проверены лично"))
	if _, e = s.Customer(ctx, 10); e == nil {
		t.Fatal("old owner")
	}
	if _, e = s.Customer(ctx, 12); e != nil {
		t.Fatal(e)
	}
}
func TestPurchaseIdempotenceWithoutCashback(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := reg(t, s, 10, "")
	p, e := s.CreatePurchase(ctx, 2, c.ID, 8000, 0)
	must(t, e)
	must(t, s.Confirm(ctx, 2, p.ID, "r1"))
	must(t, s.Confirm(ctx, 2, p.ID, "r1"))
	balance(t, s, 10, 0)
	duplicate, e := s.CreatePurchase(ctx, 2, c.ID, 8000, 0)
	must(t, e)
	if e = s.Confirm(ctx, 2, duplicate.ID, "r1"); e == nil {
		t.Fatal("duplicate receipt")
	}
	balance(t, s, 10, 0)
	if e = s.Return(ctx, 2, p.ID, "Возврат по чеку"); e == nil {
		t.Fatal("employee return")
	}
	must(t, s.Return(ctx, 1, p.ID, "Возврат по чеку"))
	must(t, s.Return(ctx, 1, p.ID, "Возврат по чеку"))
	balance(t, s, 10, 0)
}
func TestConcurrentReservations(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := reg(t, s, 10, "")
	must(t, s.Adjust(ctx, 1, c.ID, 3000, "Стартовая корректировка", "seed"))
	if _, e := s.CreatePurchase(ctx, 2, c.ID, 4000, 2001); e == nil {
		t.Fatal("over 50 percent")
	}
	if _, e := s.CreatePurchase(ctx, 2, c.ID, 10000, 3001); e == nil {
		t.Fatal("over balance")
	}
	p, e := s.CreatePurchase(ctx, 2, c.ID, 4000, 2000)
	must(t, e)
	q, e := s.CreatePurchase(ctx, 2, c.ID, 4000, 2000)
	must(t, e)
	if e = s.Approve(ctx, 11, p.ID); e == nil {
		t.Fatal("wrong customer")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{p.ID, q.ID} {
		wg.Add(1)
		go func(id string) { defer wg.Done(); results <- s.Approve(ctx, 10, id) }(id)
	}
	wg.Wait()
	close(results)
	ok := 0
	for e := range results {
		if e == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("approved %d", ok)
	}
	var winner string
	must(t, s.Pool.QueryRow(ctx, "SELECT id FROM purchases WHERE status='reserved'").Scan(&winner))
	must(t, s.Approve(ctx, 10, winner))
	c, e = s.Customer(ctx, 10)
	must(t, e)
	if c.Reserved != 2000 {
		t.Fatal("double reserve")
	}
	must(t, s.Confirm(ctx, 2, winner, "redeem-r1"))
	must(t, s.Confirm(ctx, 2, winner, "redeem-r1"))
	balance(t, s, 10, 1000)
	c, e = s.Customer(ctx, 10)
	must(t, e)
	if c.Reserved != 0 {
		t.Fatal("reserve not cleared")
	}
	must(t, s.Return(ctx, 1, winner, "Полный возврат покупки"))
	balance(t, s, 10, 3000)
}
func TestReservationCancellationAndExpiry(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := reg(t, s, 10, "")
	must(t, s.Adjust(ctx, 1, c.ID, 1000, "Корректировка для теста", "seed"))
	for _, expired := range []bool{false, true} {
		p, e := s.CreatePurchase(ctx, 2, c.ID, 2000, 1000)
		must(t, e)
		must(t, s.Approve(ctx, 10, p.ID))
		if expired {
			_, e = s.Pool.Exec(ctx, "UPDATE purchases SET expires_at=now()-interval '1 second' WHERE id=$1", p.ID)
			must(t, e)
			if e = s.Confirm(ctx, 2, p.ID, "expired"); e == nil {
				t.Fatal("expired reservation confirmed")
			}
			must(t, s.Expire(ctx))
		} else {
			must(t, s.Cancel(ctx, 10, p.ID, false))
			must(t, s.Cancel(ctx, 10, p.ID, false))
		}
		c, e = s.Customer(ctx, 10)
		must(t, e)
		if c.Reserved != 0 || c.Balance != 1000 {
			t.Fatal("reserve leak")
		}
	}
	p, e := s.CreatePurchase(ctx, 2, c.ID, 2000, 1000)
	must(t, e)
	_, e = s.Pool.Exec(ctx, "UPDATE purchases SET expires_at=now()-interval '1 second' WHERE id=$1", p.ID)
	must(t, e)
	if e = s.Approve(ctx, 10, p.ID); e == nil {
		t.Fatal("expired request")
	}
	must(t, s.Expire(ctx))
}
func TestReferralRegistrationAndReturns(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ref := reg(t, s, 10, "")
	c := reg(t, s, 11, ref.Referral)
	balance(t, s, 10, 500)
	balance(t, s, 11, 500)
	reg(t, s, 11, ref.Referral)
	reg(t, s, 10, ref.Referral)
	balance(t, s, 10, 500)
	balance(t, s, 11, 500)
	if _, e := s.Register(ctx, 12, 12, c.Phone, "v1", ref.Referral); e == nil {
		t.Fatal("duplicate phone rewarded")
	}
	balance(t, s, 10, 500)
	p, e := s.CreatePurchase(ctx, 2, c.ID, 1000, 300)
	must(t, e)
	must(t, s.Approve(ctx, 11, p.ID))
	must(t, s.Confirm(ctx, 2, p.ID, "ref-sale"))
	balance(t, s, 11, 200)
	balance(t, s, 10, 500)
	must(t, s.Return(ctx, 1, p.ID, "Возврат аксессуаров"))
	balance(t, s, 10, 500)
	balance(t, s, 11, 500)
	var qualified *string
	var status string
	must(t, s.Pool.QueryRow(ctx, "SELECT status,qualified_purchase_id FROM referrals WHERE referred_id=$1", c.ID).Scan(&status, &qualified))
	if status != "rewarded" || qualified != nil {
		t.Fatal("registration reward tied to purchase")
	}
}
func TestConcurrentReferralRegistration(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ref := reg(t, s, 10, "")
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.Register(ctx, 11, 11, "+79990000011", "v1", ref.Referral); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	balance(t, s, 10, 500)
	balance(t, s, 11, 500)
	var n int
	must(t, s.Pool.QueryRow(ctx, "SELECT count(*) FROM bonus_transactions WHERE type='referral'").Scan(&n))
	if n != 2 {
		t.Fatal("duplicate rewards", n)
	}
	var self bool
	must(t, s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM referrals WHERE referrer_id=referred_id)").Scan(&self))
	if self {
		t.Fatal("self referral")
	}
}
func TestEmployeeAwardIdempotenceAndPermissions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := reg(t, s, 10, "")
	key := "0123456789abcdef0123456789abcdef"
	if e := s.Award(ctx, 10, c.ID, 700, key); e == nil {
		t.Fatal("customer award")
	}
	if e := s.Award(ctx, 2, c.ID, -1, key); e == nil {
		t.Fatal("negative award")
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := s.Award(ctx, 2, c.ID, 700, key); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	balance(t, s, 10, 700)
	if e := s.Award(ctx, 2, c.ID, 800, key); e == nil {
		t.Fatal("idempotency key reused with another amount")
	}
	balance(t, s, 10, 700)
	must(t, s.SetEmployee(ctx, 1, 2, false))
	if e := s.Award(ctx, 2, c.ID, 100, "abcdef0123456789abcdef0123456789"); e == nil {
		t.Fatal("blocked employee")
	}
	var awards, audits int
	must(t, s.Pool.QueryRow(ctx, "SELECT count(*) FROM bonus_transactions WHERE type='award'").Scan(&awards))
	must(t, s.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action='bonuses_awarded'").Scan(&audits))
	if awards != 1 || audits != 1 {
		t.Fatal("duplicate ledger/audit", awards, audits)
	}
}

// Expiry must use wall-clock time after acquiring locks, not the transaction start time.
func TestExpiryWhileWaitingForLock(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := reg(t, s, 10, "")
	must(t, s.Adjust(ctx, 1, c.ID, 1000, "Тест истечения под блокировкой", "seed"))
	p, e := s.CreatePurchase(ctx, 2, c.ID, 2000, 1000)
	must(t, e)
	tx, e := s.Pool.Begin(ctx)
	must(t, e)
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, "UPDATE purchases SET expires_at=clock_timestamp()+interval '300 milliseconds' WHERE id=$1", p.ID)
	must(t, e)
	done := make(chan error, 1)
	go func() { done <- s.Approve(ctx, 10, p.ID) }()
	// Hold the purchase row until after the request expires.
	time.Sleep(500 * time.Millisecond)
	must(t, tx.Commit(ctx))
	if e = <-done; e == nil {
		t.Fatal("request expired while waiting, but was approved")
	}
	c, e = s.Customer(ctx, 10)
	must(t, e)
	if c.Reserved != 0 {
		t.Fatal("expired reserve created")
	}
}

func TestUpgradePreservesExistingBalancesAndReferralHistory(t *testing.T) {
	s := newTestStore(t, false)
	ctx := context.Background()
	_, e := s.Pool.Exec(ctx, schema)
	must(t, e)
	_, e = s.Pool.Exec(ctx, "INSERT INTO schema_migrations VALUES(1)")
	must(t, e)
	must(t, s.Bootstrap(ctx, 1))
	must(t, s.SetEmployee(ctx, 1, 2, true))
	_, e = s.Pool.Exec(ctx, `INSERT INTO customers(telegram_id,phone,card_token,referral_token,consent_version) VALUES(10,'+79990000010','old-card','old-ref','v1');
 INSERT INTO loyalty_accounts(customer_id,balance) SELECT id,750 FROM customers WHERE telegram_id=10;
 INSERT INTO bonus_transactions(customer_id,amount,type,reason,created_by) SELECT id,750,'adjustment','Предыдущее начисление',1 FROM customers WHERE telegram_id=10;
 INSERT INTO customers(telegram_id,phone,card_token,referral_token,consent_version,referred_by) SELECT 11,'+79990000011','old-card-2','old-ref-2','v1',id FROM customers WHERE telegram_id=10;
 INSERT INTO loyalty_accounts(customer_id) SELECT id FROM customers WHERE telegram_id=11;
 INSERT INTO referrals(referrer_id,referred_id) SELECT referred_by,id FROM customers WHERE telegram_id=11;`)
	must(t, e)
	must(t, s.Migrate(ctx))
	must(t, s.Migrate(ctx))
	balance(t, s, 10, 750)
	balance(t, s, 11, 0)
	var status string
	must(t, s.Pool.QueryRow(ctx, "SELECT status FROM referrals").Scan(&status))
	if status != "pending" {
		t.Fatal("unexpected retroactive reward")
	}
	c, e := s.Customer(ctx, 10)
	must(t, e)
	must(t, s.Award(ctx, 2, c.ID, 250, "abcdef0123456789abcdef0123456789"))
	balance(t, s, 10, 1000)
}
