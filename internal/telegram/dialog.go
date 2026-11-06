package telegram

import (
	"apphub-rewards/internal/security"
	"apphub-rewards/internal/storage"
	"context"

	"fmt"
	tg "github.com/go-telegram/bot"
	"strconv"
	"strings"
)

func (a *App) input(ctx context.Context, b *tg.Bot, id int64, text string, d storage.Dialog, role string) error {
	if d.Step == "contact" || d.Step == "consent" {
		return security.Fail("Для регистрации нажмите «📱 Поделиться номером». Номер, введённый текстом, не подтверждает владение телефоном")
	}
	if role == "" {
		return storage.ErrDenied
	}
	data := d.Data
	switch d.Step {
	case "find":
		c, e := a.S.Find(ctx, id, strings.TrimSpace(text))
		if e != nil {
			return security.Fail("Клиент не найден. Проверьте номер телефона или QR-код")
		}
		data["customer"] = strconv.FormatInt(c.ID, 10)
		a.send(ctx, b, id, fmt.Sprintf("Клиент: %s\nДоступно: %d бонусов", security.Mask(c.Phone), c.Balance-c.Reserved), nil)
		switch data["mode"] {
		case "find", "history":
			if e = a.S.SetDialog(ctx, id, "client", map[string]string{"customer": data["customer"]}); e != nil {
				return e
			}
			a.send(ctx, b, id, "Клиент выбран. Что сделать?", keyboard([]string{"➕ Начислить бонусы", "➖ Списать бонусы"}, []string{"📚 История клиента", "🔎 Найти клиента"}, []string{"❌ Отмена действия"}))
			if data["mode"] == "history" {
				return a.history(ctx, b, id, c.ID, 0)
			}
			return nil
		case "adjust":
			return storage.ErrState
		case "earn":
			data["key"] = security.Token()
			return a.prompt(ctx, b, id, "award", data, "Сколько бонусов начислить клиенту?")
		case "redeem":
			data["key"] = security.Token()
			return a.prompt(ctx, b, id, "debit", data, "Сколько бонусов списать у клиента?")
		default:
			return storage.ErrState
		}
	case "award":
		amount, e := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if e != nil || !security.Amount(amount) {
			return security.Fail("Введите положительное целое количество бонусов")
		}
		if e = a.S.Award(ctx, id, num(data["customer"]), amount, data["key"]); e != nil {
			return e
		}
		a.S.Clear(ctx, id)
		a.send(ctx, b, id, fmt.Sprintf("✅ Начислено %d бонусов.", amount), nil)
		a.menu(ctx, b, id)
		return nil
	case "debit":
		amount, e := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if e != nil || amount <= 0 {
			return security.Fail("Введите положительное целое количество бонусов")
		}
		if e = a.S.Debit(ctx, id, num(data["customer"]), amount, data["key"]); e != nil {
			return e
		}
		a.S.Clear(ctx, id)
		a.send(ctx, b, id, fmt.Sprintf("✅ Списано %d бонусов.", amount), nil)
		a.menu(ctx, b, id)
		return nil
	case "amount", "redeem", "waiting", "sale_confirm":
		return storage.ErrState
	case "employee", "return":
		if role != "ADMIN" {
			return storage.ErrDenied
		}
		parts := strings.Fields(text)
		switch d.Step {
		case "employee":
			if len(parts) != 2 || num(parts[0]) <= 0 || (parts[1] != "0" && parts[1] != "1") {
				return security.Fail("Формат: Telegram_ID 1 или Telegram_ID 0")
			}
			data["kind"] = "employee"
			data["target"] = parts[0]
			data["active"] = parts[1]
		case "return":
			p := strings.SplitN(strings.TrimSpace(text), " ", 2)
			if len(p) != 2 {
				return security.Fail("Введите значение и причину через пробел")
			}
			data["kind"] = d.Step
			data["reason"] = p[1]
			data["purchase"] = p[0]
		}
		data["nonce"] = security.Token()
		if e := a.S.SetDialog(ctx, id, "admin_confirm", data); e != nil {
			return e
		}
		a.send(ctx, b, id, "Проверьте действие:\n"+text+"\n\nИзменение будет сохранено в журнале.", inline("✅ Подтвердить", "admin_apply:"+data["nonce"]))
		return nil
	case "admin_confirm":
		return security.Fail("Подтвердите действие кнопкой или нажмите /cancel")
	}
	return storage.ErrState
}
