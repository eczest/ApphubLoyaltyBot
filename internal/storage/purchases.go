package storage

import (
	"apphub-rewards/internal/security"
	"context"

	"fmt"
	"github.com/jackc/pgx/v5"
)

func (s *Store) CreatePurchase(ctx context.Context, actor, c, amount, redeem int64) (Purchase, error) {
	p := Purchase{ID: security.Token(), Customer: c, Employee: actor, Amount: amount, Redeem: redeem}
	if !security.Amount(amount) || redeem < 0 {
		return p, security.Fail("Некорректная сумма")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return p, e
	}
	defer tx.Rollback(ctx)
	if e = role(ctx, tx, actor, false); e != nil {
		return p, e
	}
	var balance, reserved int64
	const cash int64 = 0
	e = tx.QueryRow(ctx, "SELECT balance,reserved_balance FROM loyalty_accounts WHERE customer_id=$1 FOR UPDATE", c).Scan(&balance, &reserved)
	if e != nil {
		return p, e
	}
	if redeem > balance-reserved {
		return p, security.Fail("Недостаточно бонусов на балансе клиента")
	}
	if redeem > amount {
		return p, security.Fail("Списание превышает стоимость покупки")
	}
	p.Status = "draft"
	if redeem > 0 {
		p.Status = "requested"
	}
	p.Cashback = cash
	_, e = tx.Exec(ctx, "INSERT INTO purchases(id,customer_id,employee_id,accessories_amount,redeemed_amount,paid_amount,cashback_percent,status,expires_at,store_id,register_id) VALUES($1,$2,$3,$4,$5,$4::bigint-$5::bigint,$6,$7,clock_timestamp()+interval '5 minutes',$8,$9)", p.ID, c, actor, amount, redeem, cash, p.Status, s.StoreID, s.RegisterID)
	if e != nil {
		return p, e
	}
	if e = audit(ctx, tx, actor, "purchase_created", p.ID); e != nil {
		return p, e
	}
	return p, tx.Commit(ctx)
}
func loadPurchase(ctx context.Context, tx pgx.Tx, id string) (p Purchase, e error) {
	e = tx.QueryRow(ctx, "SELECT id,customer_id,employee_id,accessories_amount,redeemed_amount,cashback_percent,status,expires_at FROM purchases WHERE id=$1 FOR UPDATE", id).Scan(&p.ID, &p.Customer, &p.Employee, &p.Amount, &p.Redeem, &p.Cashback, &p.Status, &p.Expires)
	return
}
func (s *Store) Approve(ctx context.Context, tg int64, id string) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	p, e := loadPurchase(ctx, tx, id)
	if e != nil {
		return ErrState
	}
	var owner int64
	e = tx.QueryRow(ctx, "SELECT telegram_id FROM customers WHERE id=$1", p.Customer).Scan(&owner)
	if e != nil || owner != tg {
		return ErrDenied
	}
	var valid bool
	if e = tx.QueryRow(ctx, "SELECT expires_at>clock_timestamp() FROM purchases WHERE id=$1", id).Scan(&valid); e != nil {
		return e
	}
	if !valid {
		return ErrState
	}
	if p.Status == "reserved" {
		return nil
	}
	if p.Status != "requested" {
		return ErrState
	}
	if e = role(ctx, tx, p.Employee, false); e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, "UPDATE loyalty_accounts SET reserved_balance=reserved_balance+$2,updated_at=now() WHERE customer_id=$1 AND balance-reserved_balance>=$2", p.Customer, p.Redeem)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return security.Fail("Недостаточно доступных бонусов")
	}
	if e = tx.QueryRow(ctx, "SELECT expires_at>clock_timestamp() FROM purchases WHERE id=$1", id).Scan(&valid); e != nil {
		return e
	}
	if !valid {
		return ErrState
	}
	_, e = tx.Exec(ctx, "UPDATE purchases SET status='reserved',expires_at=clock_timestamp()+interval '15 minutes' WHERE id=$1", id)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO outbox(telegram_id,message) VALUES($1,$2)", p.Employee, fmt.Sprintf("✅ Покупатель подтвердил списание %d бонусов. После оплаты нажмите «✅ Завершить списание» в текущем диалоге. Подтверждение действует 15 минут.", p.Redeem))
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Confirm(ctx context.Context, actor int64, id, receipt string) error {
	if len(receipt) < 1 || len(receipt) > 100 {
		return security.Fail("Номер чека: от 1 до 100 символов")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = role(ctx, tx, actor, false); e != nil {
		return e
	}
	p, e := loadPurchase(ctx, tx, id)
	if e != nil {
		return ErrState
	}
	if p.Employee != actor && s.Role(ctx, actor) != "ADMIN" {
		return ErrDenied
	}
	if p.Status == "confirmed" {
		return nil
	}
	var valid bool
	tx.QueryRow(ctx, "SELECT expires_at>clock_timestamp() FROM purchases WHERE id=$1", id).Scan(&valid)
	if !valid || (p.Status != "draft" && p.Status != "reserved") {
		return ErrState
	}
	rows, e := tx.Query(ctx, "SELECT customer_id FROM loyalty_accounts WHERE customer_id=$1 FOR UPDATE", p.Customer)
	if e != nil {
		return e
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	if e = tx.QueryRow(ctx, "SELECT expires_at>clock_timestamp() FROM purchases WHERE id=$1", id).Scan(&valid); e != nil {
		return e
	}
	if !valid {
		return ErrState
	}
	_, e = tx.Exec(ctx, "UPDATE loyalty_accounts SET reserved_balance=reserved_balance-$2 WHERE customer_id=$1", p.Customer, p.Redeem)
	if e != nil {
		return e
	}
	if p.Redeem > 0 {
		if e = ledger(ctx, tx, p.Customer, id, -p.Redeem, "redemption", "Оплата аксессуаров бонусами", actor); e != nil {
			return e
		}
	}
	cash := (p.Amount - p.Redeem) * p.Cashback / 100
	if cash > 0 {
		if e = ledger(ctx, tx, p.Customer, id, cash, "cashback", "Кешбэк за аксессуары", actor); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, "UPDATE purchases SET status='confirmed',receipt_number=$2,confirmed_at=now() WHERE id=$1", id, receipt)
	if e != nil {
		return security.Fail("Не удалось подтвердить: возможно, чек уже использован")
	}
	if e = notify(ctx, tx, p.Customer, fmt.Sprintf("✅ Списано %d бонусов за аксессуары.", p.Redeem)); e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, "purchase_confirmed", id); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Cancel(ctx context.Context, actor int64, id string, expired bool) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	p, e := loadPurchase(ctx, tx, id)
	if e != nil {
		return ErrState
	}
	if expired {
		var due bool
		tx.QueryRow(ctx, "SELECT expires_at<=clock_timestamp() FROM purchases WHERE id=$1", id).Scan(&due)
		if !due {
			return nil
		}
	} else {
		var owner int64
		tx.QueryRow(ctx, "SELECT telegram_id FROM customers WHERE id=$1", p.Customer).Scan(&owner)
		if actor != owner {
			if e = role(ctx, tx, actor, false); e != nil {
				return e
			}
			if actor != p.Employee && s.Role(ctx, actor) != "ADMIN" {
				return ErrDenied
			}
		}
	}
	if p.Status == "cancelled" {
		return nil
	}
	if p.Status != "draft" && p.Status != "requested" && p.Status != "reserved" {
		return ErrState
	}
	if p.Status == "reserved" {
		_, e = tx.Exec(ctx, "UPDATE loyalty_accounts SET reserved_balance=reserved_balance-$2 WHERE customer_id=$1", p.Customer, p.Redeem)
		if e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, "UPDATE purchases SET status='cancelled' WHERE id=$1", id)
	if e != nil {
		return e
	}
	if e = notify(ctx, tx, p.Customer, "Запрос покупки отменён или истёк. Резерв бонусов освобождён."); e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, "purchase_cancelled", id); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Expire(ctx context.Context) error {
	rows, e := s.Pool.Query(ctx, "SELECT id FROM purchases WHERE status IN ('draft','requested','reserved') AND expires_at<=clock_timestamp() LIMIT 100")
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if e = s.Cancel(ctx, 0, id, true); e != nil {
			return e
		}
	}
	return rows.Err()
}
func (s *Store) Return(ctx context.Context, actor int64, id, reason string) error {
	if len(reason) < 5 || len(reason) > 500 {
		return security.Fail("Укажите причину возврата (5–500 символов)")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = role(ctx, tx, actor, true); e != nil {
		return e
	}
	p, e := loadPurchase(ctx, tx, id)
	if e != nil {
		return ErrState
	}
	if p.Status == "returned" {
		return nil
	}
	if p.Status != "confirmed" {
		return ErrState
	}
	rows, e := tx.Query(ctx, "SELECT a.customer_id FROM loyalty_accounts a WHERE a.customer_id IN (SELECT customer_id FROM bonus_transactions WHERE purchase_id=$1) ORDER BY a.customer_id FOR UPDATE", id)
	if e != nil {
		return e
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	rows, e = tx.Query(ctx, "SELECT id,customer_id,amount FROM bonus_transactions WHERE purchase_id=$1 AND type<>'reversal' ORDER BY id", id)
	if e != nil {
		return e
	}
	type entry struct{ id, c, a int64 }
	var entries []entry
	for rows.Next() {
		var v entry
		rows.Scan(&v.id, &v.c, &v.a)
		entries = append(entries, v)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	for _, v := range entries {
		tag, e := tx.Exec(ctx, "UPDATE loyalty_accounts SET balance=balance-$2,updated_at=now() WHERE customer_id=$1 AND balance-$2>=reserved_balance", v.c, v.a)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return security.Fail("Награда уже потрачена или зарезервирована. Сначала администратор должен урегулировать баланс; возврат не проведён")
		}
		_, e = tx.Exec(ctx, "INSERT INTO bonus_transactions(customer_id,purchase_id,amount,type,reason,created_by,reverses_id) VALUES($1,$2,$3,'reversal',$4,$5,$6)", v.c, id, -v.a, reason, actor, v.id)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "UPDATE bonus_transactions SET status='reversed' WHERE id=$1", v.id); e != nil {
			return e
		}
		if e = notify(ctx, tx, v.c, fmt.Sprintf("Возврат покупки: корректировка %+d бонусов. Причина: %s", -v.a, reason)); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, "UPDATE purchases SET status='returned' WHERE id=$1", id)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "UPDATE referrals SET status='reversed' WHERE qualified_purchase_id=$1", id)
	if e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, "purchase_return", id+": "+reason); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
