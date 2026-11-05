package telegram

import (
	"context"
	tg "github.com/go-telegram/bot"
	"log/slog"
	"time"
)

func (a *App) Worker(ctx context.Context, b *tg.Bot) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if e := a.S.Expire(ctx); e != nil {
				slog.Warn("expiration failed")
			}
			a.deliver(ctx, b)
		}
	}
}
func (a *App) deliver(ctx context.Context, b *tg.Bot) {
	rows, e := a.S.Pool.Query(ctx, "SELECT id,telegram_id,message FROM outbox WHERE sent_at IS NULL AND next_attempt<=now() AND attempts<20 ORDER BY id LIMIT 20")
	if e != nil {
		return
	}
	type item struct {
		id, tg int64
		msg    string
	}
	var items []item
	for rows.Next() {
		var i item
		if rows.Scan(&i.id, &i.tg, &i.msg) == nil {
			items = append(items, i)
		}
	}
	rows.Close()
	for _, i := range items {
		_, err := b.SendMessage(ctx, &tg.SendMessageParams{ChatID: i.tg, Text: i.msg})
		if err == nil {
			a.S.Pool.Exec(ctx, "UPDATE outbox SET sent_at=now(),attempts=attempts+1 WHERE id=$1", i.id)
		} else {
			a.S.Pool.Exec(ctx, "UPDATE outbox SET attempts=attempts+1,next_attempt=now()+interval '5 minutes' WHERE id=$1", i.id)
		}
	}
}
