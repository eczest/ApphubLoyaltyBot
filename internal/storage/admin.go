package storage

import (
	"apphub-rewards/internal/security"
	"context"

	"fmt"
	"strings"
)

func (s *Store) SetEmployee(ctx context.Context, actor, target int64, active bool) error {
	if target <= 0 || target == actor {
		return security.Fail("Некорректный Telegram ID или попытка изменить себя")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = role(ctx, tx, actor, true); e != nil {
		return e
	}
	var existing string
	tx.QueryRow(ctx, "SELECT role FROM employees WHERE telegram_id=$1", target).Scan(&existing)
	if existing == "ADMIN" {
		return security.Fail("Администраторы через меню сотрудников не изменяются")
	}
	_, e = tx.Exec(ctx, "INSERT INTO employees(telegram_id,role,is_active) VALUES($1,'EMPLOYEE',$2) ON CONFLICT(telegram_id) DO UPDATE SET is_active=$2", target, active)
	if e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, "employee_changed", fmt.Sprintf("%d active=%t", target, active)); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) SetSettings(ctx context.Context, actor int64, v Settings) error {
	if v.Cashback < 0 || v.Cashback > 100 || v.Redemption < 0 || v.Redemption > 100 || v.Reward < 0 || v.Reward > 1000000 {
		return security.Fail("Проценты: 0–100; награда: 0–1000000")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = role(ctx, tx, actor, true); e != nil {
		return e
	}
	var old Settings
	e = tx.QueryRow(ctx, "SELECT cashback_percent,max_redemption_percent,referral_reward,referral_enabled FROM loyalty_settings WHERE id=1 FOR UPDATE").Scan(&old.Cashback, &old.Redemption, &old.Reward, &old.Enabled)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "UPDATE loyalty_settings SET cashback_percent=$1,max_redemption_percent=$2,referral_reward=$3,referral_enabled=$4,updated_at=now() WHERE id=1", v.Cashback, v.Redemption, v.Reward, v.Enabled)
	if e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, "settings_changed", fmt.Sprintf("%+v -> %+v", old, v)); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Adjust(ctx context.Context, actor, c, amount int64, reason, key string) error {
	if amount == 0 || amount > 100000000 || amount < -100000000 || len(reason) < 5 || len(reason) > 500 {
		return security.Fail("Укажите сумму и причину (5–500 символов)")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = role(ctx, tx, actor, true); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", key)
	if e != nil {
		return e
	}
	var done bool
	e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM audit_log WHERE action='adjustment' AND details=$1)", key).Scan(&done)
	if e != nil {
		return e
	}
	if done {
		return nil
	}
	if e = ledger(ctx, tx, c, "", amount, "adjustment", reason, actor); e != nil {
		return security.Fail("Корректировка невозможна: недостаточно доступных бонусов")
	}
	if e = audit(ctx, tx, actor, "adjustment", key); e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, "balance_adjusted", fmt.Sprintf("customer=%d amount=%d reason=%s", c, amount, reason)); e != nil {
		return e
	}
	if e = notify(ctx, tx, c, fmt.Sprintf("Корректировка баланса: %+d бонусов. Причина: %s", amount, reason)); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Report(ctx context.Context, actor int64, auditMode bool) (string, error) {
	if s.Role(ctx, actor) != "ADMIN" {
		return "", ErrDenied
	}
	if !auditMode {
		var clients, earned, spent, refs int64
		e := s.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM customers),COALESCE(sum(amount) FILTER(WHERE amount>0),0),COALESCE(-sum(amount) FILTER(WHERE amount<0),0),(SELECT count(*) FROM referrals) FROM bonus_transactions").Scan(&clients, &earned, &spent, &refs)
		return fmt.Sprintf("📊 Клиентов: %d\nНачислено (включая корректировки): %d\nСписано (включая возвраты): %d\nПриглашено: %d", clients, earned, spent, refs), e
	}
	rows, e := s.Pool.Query(ctx, "SELECT to_char(created_at,'DD.MM HH24:MI'),actor,action,details FROM audit_log ORDER BY id DESC LIMIT 15")
	if e != nil {
		return "", e
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var t, a, d string
		var id int64
		rows.Scan(&t, &id, &a, &d)
		fmt.Fprintf(&b, "%s · %d · %s\n%s\n", t, id, a, d)
	}
	return b.String(), rows.Err()
}

// Recovery changes only the Telegram binding, preserving phone, ledger, consent and referrals.
func (s *Store) Recover(ctx context.Context, actor, c, newTG int64, reason string) error {
	if newTG <= 0 || len(reason) < 10 || len(reason) > 500 {
		return security.Fail("Нужны Telegram ID и причина проверки личности (10–500 символов)")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = role(ctx, tx, actor, true); e != nil {
		return e
	}
	var old int64
	e = tx.QueryRow(ctx, "SELECT telegram_id FROM customers WHERE id=$1 FOR UPDATE", c).Scan(&old)
	if e != nil {
		return e
	}
	var pending bool
	tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM purchases WHERE customer_id=$1 AND status IN ('draft','requested','reserved'))", c).Scan(&pending)
	if pending {
		return security.Fail("Сначала отмените активные покупки клиента")
	}
	_, e = tx.Exec(ctx, "UPDATE customers SET telegram_id=$2 WHERE id=$1", c, newTG)
	if e != nil {
		return security.Fail("Новый Telegram ID уже связан с другим клиентом")
	}
	_, e = tx.Exec(ctx, "UPDATE outbox SET telegram_id=$2 WHERE telegram_id=$1 AND sent_at IS NULL", old, newTG)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "DELETE FROM dialogs WHERE telegram_id=$1 OR telegram_id=$2", old, newTG)
	if e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, "account_recovery", fmt.Sprintf("customer=%d old=%d new=%d reason=%s", c, old, newTG, reason)); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO outbox(telegram_id,message) VALUES($1,'Привязка вашей бонусной карты изменена администратором. Если вы не запрашивали восстановление, свяжитесь с магазином.'),($2,'Доступ к вашей бонусной карте восстановлен. Откройте /start.')", old, newTG)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
