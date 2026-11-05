package storage

import (
	"apphub-rewards/internal/security"
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// Award credits the amount chosen by the employee. Key is persisted before input.
func (s *Store) Award(ctx context.Context, actor, customer, amount int64, key string) error {
	if !security.Amount(amount) || len(key) != 32 {
		return security.Fail("Введите целое количество бонусов от 1 до 100000000")
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
	e = tx.QueryRow(ctx, "SELECT customer_id,amount,created_by FROM bonus_transactions WHERE operation_key=$1", key).Scan(&c, &a, &owner)
	if e == nil {
		if c != customer || a != amount || owner != actor {
			return ErrState
		}
		return nil
	}
	if e != pgx.ErrNoRows {
		return e
	}
	if _, e = tx.Exec(ctx, "UPDATE loyalty_accounts SET balance=balance+$2,updated_at=now() WHERE customer_id=$1", customer, amount); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO bonus_transactions(customer_id,amount,type,reason,created_by,operation_key) VALUES($1,$2,'award','Начисление продавцом',$3,$4)", customer, amount, actor, key); e != nil {
		return e
	}
	if e = audit(ctx, tx, actor, "bonuses_awarded", fmt.Sprintf("customer=%d amount=%d operation=%s", customer, amount, key)); e != nil {
		return e
	}
	if e = notify(ctx, tx, customer, fmt.Sprintf("🎁 Вам начислено %d бонусов. Используйте их для оплаты аксессуаров.", amount)); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
