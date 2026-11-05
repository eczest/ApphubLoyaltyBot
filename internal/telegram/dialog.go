package telegram

import (
	"apphub-rewards/internal/security"
	"apphub-rewards/internal/storage"
	"context"

	"fmt"
	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
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
		default:
			return a.prompt(ctx, b, id, "amount", data, "Введите стоимость аксессуаров, чтобы рассчитать лимит списания до 50%.")
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
	case "amount":
		if data["mode"] != "redeem" {
			return storage.ErrState
		}
		amount, e := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if e != nil || !security.Amount(amount) {
			return security.Fail("Введите целую сумму от 1 до 100000000 ₽")
		}
		data["amount"] = text
		if data["mode"] == "redeem" {
			var balance int64
			e = a.S.Pool.QueryRow(ctx, "SELECT balance-reserved_balance FROM loyalty_accounts WHERE customer_id=$1", num(data["customer"])).Scan(&balance)
			if e != nil {
				return e
			}
			return a.prompt(ctx, b, id, "redeem", data, fmt.Sprintf("Доступно: %d. Можно списать до %d бонусов.\nСколько списать?", balance, security.MaxRedeem(balance, amount, 50)))
		}
		return storage.ErrState
	case "redeem":
		redeem, e := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if e != nil || redeem <= 0 {
			return security.Fail("Введите положительное целое количество бонусов")
		}
		p, e := a.S.CreatePurchase(ctx, id, num(data["customer"]), num(data["amount"]), redeem)
		if e != nil {
			return e
		}
		data["purchase"] = p.ID
		var customerTG int64
		e = a.S.Pool.QueryRow(ctx, "SELECT telegram_id FROM customers WHERE id=$1", p.Customer).Scan(&customerTG)
		if e != nil {
			return e
		}
		_, e = b.SendMessage(ctx, &tg.SendMessageParams{ChatID: customerTG, Text: fmt.Sprintf("Подтвердите списание\n\nАксессуары: %d ₽\nБонусами: %d\nК оплате рублями: %d ₽\nЗапрос действует 5 минут. После подтверждения резерв — 15 минут.", p.Amount, p.Redeem, p.Amount-p.Redeem), ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "✅ Подтвердить", CallbackData: "approve:" + p.ID}, {Text: "Отказать", CallbackData: "cancel:" + p.ID}}}}})
		if e != nil {
			_ = a.S.Cancel(ctx, id, p.ID, false)
			return security.Fail("Не удалось доставить запрос покупателю. Покупка отменена")
		}
		if e = a.S.SetDialog(ctx, id, "waiting", data); e != nil {
			return e
		}
		a.send(ctx, b, id, "Запрос отправлен покупателю. После его подтверждения и оплаты нажмите «✅ Завершить списание».", keyboard([]string{"✅ Завершить списание"}, []string{"❌ Отмена действия"}))
		return nil
	case "waiting":
		return security.Fail("Дождитесь подтверждения покупателя. После оплаты нажмите «✅ Завершить списание».")
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
	case "sale_confirm", "admin_confirm":
		return security.Fail("Подтвердите действие кнопкой или нажмите /cancel")
	}
	return storage.ErrState
}
