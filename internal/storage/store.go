package storage

import (
	"apphub-rewards/internal/security"
	"apphub-rewards/migrations"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"time"
)

var schema = migrations.Schema
var ErrDenied = security.Fail("Недостаточно прав или сотрудник заблокирован")
var ErrState = security.Fail("Действие устарело, отменено или недоступно. Начните заново")

type Store struct {
	Pool                *pgxpool.Pool
	StoreID, RegisterID string
}
type Customer struct {
	ID, TG, Balance, Reserved int64
	Phone, Card, Referral     string
}
type Settings struct {
	Cashback, Redemption, Reward int64
	Enabled                      bool
}
type Purchase struct {
	ID                                           string
	Customer, Employee, Amount, Redeem, Cashback int64
	Status                                       string
	Expires                                      time.Time
}
type Dialog struct {
	Step string
	Data map[string]string
}

func Open(ctx context.Context, url, store, register string) (*Store, error) {
	p, e := pgxpool.New(ctx, url)
	if e != nil {
		return nil, e
	}
	return &Store{p, store, register}, nil
}
func (s *Store) Migrate(ctx context.Context) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7812391)"); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations(version int PRIMARY KEY)")
	if e != nil {
		return e
	}
	var exists bool
	e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=1)").Scan(&exists)
	if e != nil {
		return e
	}
	if !exists {
		if _, e = tx.Exec(ctx, schema); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO schema_migrations VALUES(1)"); e != nil {
			return e
		}
	}
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=2)").Scan(&exists); e != nil {
		return e
	}
	if !exists {
		if _, e = tx.Exec(ctx, migrations.SimpleRewards); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO schema_migrations VALUES(2)"); e != nil {
			return e
		}
	}
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=3)").Scan(&exists); e != nil {
		return e
	}
	if !exists {
		if _, e = tx.Exec(ctx, migrations.RegistrationWithoutConsent); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO schema_migrations VALUES(3)"); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func (s *Store) Bootstrap(ctx context.Context, id int64) error {
	_, e := s.Pool.Exec(ctx, "INSERT INTO employees(telegram_id,role) VALUES($1,'ADMIN') ON CONFLICT(telegram_id) DO UPDATE SET role='ADMIN',is_active=true", id)
	return e
}
func role(ctx context.Context, tx pgx.Tx, id int64, admin bool) error {
	var r string
	e := tx.QueryRow(ctx, "SELECT role FROM employees WHERE telegram_id=$1 AND is_active FOR SHARE", id).Scan(&r)
	if e != nil || (admin && r != "ADMIN") {
		return ErrDenied
	}
	return nil
}
func (s *Store) Role(ctx context.Context, id int64) string {
	var r string
	s.Pool.QueryRow(ctx, "SELECT role FROM employees WHERE telegram_id=$1 AND is_active", id).Scan(&r)
	return r
}
func (s *Store) Settings(ctx context.Context) (v Settings, e error) {
	e = s.Pool.QueryRow(ctx, "SELECT cashback_percent,max_redemption_percent,referral_reward,referral_enabled FROM loyalty_settings WHERE id=1").Scan(&v.Cashback, &v.Redemption, &v.Reward, &v.Enabled)
	return
}

const customerSelect = "SELECT c.id,c.telegram_id,c.phone,c.card_token,c.referral_token,a.balance,a.reserved_balance FROM customers c JOIN loyalty_accounts a ON a.customer_id=c.id "

func scanCustomer(r pgx.Row) (c Customer, e error) {
	e = r.Scan(&c.ID, &c.TG, &c.Phone, &c.Card, &c.Referral, &c.Balance, &c.Reserved)
	return
}
func (s *Store) Customer(ctx context.Context, tg int64) (Customer, error) {
	return scanCustomer(s.Pool.QueryRow(ctx, customerSelect+"WHERE c.telegram_id=$1", tg))
}
func (s *Store) Find(ctx context.Context, actor int64, key string) (Customer, error) {
	if s.Role(ctx, actor) == "" {
		return Customer{}, ErrDenied
	}
	p, _ := security.Phone(key)
	return scanCustomer(s.Pool.QueryRow(ctx, customerSelect+"WHERE c.phone=$1 OR c.card_token=$2", p, key))
}
func (s *Store) Register(ctx context.Context, tg, owner int64, phone, version, ref string) (Customer, error) {
	p, e := security.Contact(tg, owner, phone)
	if e != nil {
		return Customer{}, e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return Customer{}, e
	}
	defer tx.Rollback(ctx)
	var id int64
	e = tx.QueryRow(ctx, "INSERT INTO customers(telegram_id,phone,card_token,referral_token,consent_version,consent_at,referred_by) VALUES($1,$2,$3,$4,NULLIF($5,''),CASE WHEN $5='' THEN NULL ELSE now() END,(SELECT id FROM customers WHERE referral_token=$6 AND telegram_id<>$1)) ON CONFLICT(telegram_id) DO NOTHING RETURNING id", tg, p, security.Token(), security.Token(), version, ref).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return s.Customer(ctx, tg)
	}
	if e != nil {
		return Customer{}, security.Fail("Номер уже зарегистрирован. Для восстановления обратитесь к администратору магазина")
	}
	_, e = tx.Exec(ctx, "INSERT INTO loyalty_accounts(customer_id) VALUES($1)", id)
	if e != nil {
		return Customer{}, e
	}
	_, e = tx.Exec(ctx, "INSERT INTO referrals(referrer_id,referred_id) SELECT referred_by,id FROM customers WHERE id=$1 AND referred_by IS NOT NULL", id)
	if e != nil {
		return Customer{}, e
	}
	var referrer int64
	if e = tx.QueryRow(ctx, "SELECT COALESCE(referred_by,0) FROM customers WHERE id=$1", id).Scan(&referrer); e != nil {
		return Customer{}, e
	}
	if referrer > 0 {
		// Lock all affected accounts in a stable order before crediting either side.
		rows, err := tx.Query(ctx, "SELECT customer_id FROM loyalty_accounts WHERE customer_id=$1 OR customer_id=$2 ORDER BY customer_id FOR UPDATE", id, referrer)
		if err != nil {
			return Customer{}, err
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return Customer{}, err
		}
		for _, c := range []int64{referrer, id} {
			if e = ledger(ctx, tx, c, "", 500, "referral", "Приведи друга: регистрация по ссылке", 0); e != nil {
				return Customer{}, e
			}
			if e = notify(ctx, tx, c, "👥 +500 бонусов за регистрацию друга по ссылке. Бонусы можно потратить на аксессуары."); e != nil {
				return Customer{}, e
			}
		}
		if _, e = tx.Exec(ctx, "UPDATE referrals SET status='rewarded',rewarded_at=now() WHERE referred_id=$1", id); e != nil {
			return Customer{}, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return Customer{}, e
	}
	return s.Customer(ctx, tg)
}
func (s *Store) Dialog(ctx context.Context, tg int64) (d Dialog) {
	d.Data = map[string]string{}
	var b []byte
	e := s.Pool.QueryRow(ctx, "SELECT step,data FROM dialogs WHERE telegram_id=$1 AND updated_at>now()-interval '24 hours'", tg).Scan(&d.Step, &b)
	if e == nil {
		json.Unmarshal(b, &d.Data)
	}
	return
}
func (s *Store) SetDialog(ctx context.Context, tg int64, step string, data map[string]string) error {
	b, _ := json.Marshal(data)
	_, e := s.Pool.Exec(ctx, "INSERT INTO dialogs(telegram_id,step,data) VALUES($1,$2,$3) ON CONFLICT(telegram_id) DO UPDATE SET step=$2,data=$3,updated_at=now()", tg, step, b)
	return e
}
func (s *Store) Clear(ctx context.Context, tg int64) {
	s.Pool.Exec(ctx, "DELETE FROM dialogs WHERE telegram_id=$1", tg)
}
func (s *Store) Allow(ctx context.Context, tg int64) bool {
	var count int
	e := s.Pool.QueryRow(ctx, "INSERT INTO rate_limits(telegram_id) VALUES($1) ON CONFLICT(telegram_id) DO UPDATE SET count=CASE WHEN rate_limits.window_at<now()-interval '1 minute' THEN 1 ELSE rate_limits.count+1 END,window_at=CASE WHEN rate_limits.window_at<now()-interval '1 minute' THEN now() ELSE rate_limits.window_at END RETURNING count", tg).Scan(&count)
	return e == nil && count <= 40
}
func audit(ctx context.Context, tx pgx.Tx, actor int64, action, details string) error {
	_, e := tx.Exec(ctx, "INSERT INTO audit_log(actor,action,details) VALUES($1,$2,$3)", actor, action, details)
	return e
}
func notify(ctx context.Context, tx pgx.Tx, customer int64, msg string) error {
	_, e := tx.Exec(ctx, "INSERT INTO outbox(telegram_id,message) SELECT telegram_id,$2 FROM customers WHERE id=$1", customer, msg)
	return e
}
func ledger(ctx context.Context, tx pgx.Tx, c int64, p string, a int64, kind, reason string, actor int64) error {
	_, e := tx.Exec(ctx, "UPDATE loyalty_accounts SET balance=balance+$2,updated_at=now() WHERE customer_id=$1", c, a)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO bonus_transactions(customer_id,purchase_id,amount,type,reason,created_by) VALUES($1,NULLIF($2,''),$3,$4,$5,$6)", c, p, a, kind, reason, actor)
	return e
}
func (s *Store) History(ctx context.Context, actor, customer int64, page int) (string, error) {
	var owner int64
	if e := s.Pool.QueryRow(ctx, "SELECT telegram_id FROM customers WHERE id=$1", customer).Scan(&owner); e != nil {
		return "", e
	}
	if actor != owner && s.Role(ctx, actor) == "" {
		return "", ErrDenied
	}
	if page < 0 || page > 10000 {
		return "", ErrState
	}
	rows, e := s.Pool.Query(ctx, "SELECT created_at,amount,type,reason,status,COALESCE(purchase_id,'') FROM bonus_transactions WHERE customer_id=$1 ORDER BY id DESC LIMIT 10 OFFSET $2", customer, page*10)
	if e != nil {
		return "", e
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var t time.Time
		var a int64
		var k, r, st, purchase string
		if e := rows.Scan(&t, &a, &k, &r, &st, &purchase); e != nil {
			return "", e
		}
		k = map[string]string{"cashback": "Кешбэк", "redemption": "Списание", "referral": "Приведи друга", "reversal": "Отмена операции", "adjustment": "Корректировка", "award": "Начисление продавцом"}[k]
		if st == "posted" {
			st = "Проведено"
		} else if st == "reversed" {
			st = "Отменено компенсацией"
		}
		fmt.Fprintf(&b, "%s | %+d | %s\n%s · %s\n\n", t.In(time.Local).Format("02.01.2006 15:04"), a, k, r, st)
		if purchase != "" {
			fmt.Fprintf(&b, "Покупка: %s\n\n", purchase)
		}
	}
	if b.Len() == 0 {
		b.WriteString("Операций пока нет.")
	}
	return b.String(), rows.Err()
}
