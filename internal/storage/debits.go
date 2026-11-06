package storage

import (
	"apphub-rewards/internal/security"
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// Debit immediately subtracts the employee's chosen amount without a purchase
// or customer confirmation. A persisted key makes retries idempotent.
func (s *Store) Debit(ctx context.Context, actor, customer, amount int64, key string) error {
	if amount <= 0 || len(key) != 32 {
		return security.Fail("Введите положительное целое количество бонусов")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = role(ctx, tx, actor, false); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", key); e != nil {
		return e
	}
	var c, a, owner int64
	var kind string
	e = tx.QueryRow(ctx, "SELECT customer_id,amount,created_by,type FROM bonus_transactions WHERE operation_key=$1", key).Scan(&c, &a, &owner, &kind)
	if e == nil {
		if c != customer || a != -amount || owner != actor || kind != "redemption" {
			return ErrState
		}
		return nil
	}
	if e != pgx.ErrNoRows {
		return e
	}
	tag, e := tx.Exec(ctx, "UPDATE loyalty_accounts SET balance=balance-$2,updated_at=now() WHERE customer_id=$1 AND balance-reserved_balance>=$2", customer, amount)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return security.Fail("Недостаточно бонусов на балансе клиента")
	}
	if _, e = tx.Exec(ctx, "INSERT INTO bonus_transactions(customer_id,amount,type,reason,created_by,operation_key) VALUES($1,-$2::bigint,'redemption','Списание сотрудником',$3,$4)", customer, amount, actor, key); e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, "bonuses_debited", fmt.Sprintf("customer=%d amount=%d operation=%s", customer, amount, key)); e != nil {
		return e
	}
	if e = notify(ctx, tx, customer, fmt.Sprintf("➖ С вашего баланса списано %d бонусов.", amount)); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
