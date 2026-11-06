package telegram

import (
	"apphub-rewards/internal/config"
	"apphub-rewards/internal/security"
	"apphub-rewards/internal/storage"
	"context"
	"errors"
	"fmt"
	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const welcome = "👋 Добро пожаловать в AppHub!\n\nВаш магазин техники и аксессуаров. У нас вы найдёте iPhone, PlayStation 5, Dyson, Samsung и многое другое."

type App struct {
	S        *storage.Store
	C        config.Config
	Username string
}

func New(s *storage.Store, c config.Config) (*App, error) {
	return &App{S: s, C: c}, nil
}
func keyboard(rows ...[]string) *models.ReplyKeyboardMarkup {
	k := &models.ReplyKeyboardMarkup{ResizeKeyboard: true}
	for _, r := range rows {
		var row []models.KeyboardButton
		for _, t := range r {
			row = append(row, models.KeyboardButton{Text: t})
		}
		k.Keyboard = append(k.Keyboard, row)
	}
	return k
}
func inline(label, data string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: label, CallbackData: data}}}}
}
func (a *App) send(ctx context.Context, b *tg.Bot, id int64, text string, k any) {
	if len(text) > 3900 {
		text = text[:3900]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	if _, e := b.SendMessage(ctx, &tg.SendMessageParams{ChatID: id, Text: text, ReplyMarkup: k}); e != nil {
		slog.Warn("telegram delivery failed")
	}
}
func (a *App) menu(ctx context.Context, b *tg.Bot, id int64) {
	r := a.S.Role(ctx, id)
	rows := [][]string{{"🎁 Мои бонусы"}, {"👥 Приведи друга", "📜 История операций"}, {"📞 Контакты"}}
	if _, e := a.S.Customer(ctx, id); errors.Is(e, pgx.ErrNoRows) {
		rows = append([][]string{{"📱 Регистрация по номеру"}}, rows...)
	}
	if r != "" {
		rows = append(rows, []string{"🔎 Найти клиента"}, []string{"➕ Начислить бонусы", "➖ Списать бонусы"}, []string{"📚 История клиента"}, []string{"❌ Отмена действия"})
	}
	if r == "ADMIN" {
		rows = append(rows, []string{"👔 Сотрудники"}, []string{"📊 Статистика", "🧾 Журнал"}, []string{"↩️ Возврат покупки"})
	}
	a.send(ctx, b, id, welcome+"\n\n🎁 Получайте бонусы за покупки аксессуаров и приглашайте друзей.\nВыберите действие ниже.", keyboard(rows...))
}
func (a *App) Handle(ctx context.Context, b *tg.Bot, u *models.Update) {
	var id int64
	var text string
	var contact *models.Contact
	cb := u.CallbackQuery
	if cb != nil {
		if cb.Message.Message == nil || cb.Message.Message.Chat.Type != "private" {
			return
		}
		id = cb.From.ID
		text = cb.Data
		b.AnswerCallbackQuery(ctx, &tg.AnswerCallbackQueryParams{CallbackQueryID: cb.ID})
	} else if u.Message != nil {
		m := u.Message
		if m.From == nil || m.Chat.Type != "private" {
			return
		}
		id = m.From.ID
		text = m.Text
		contact = m.Contact
	} else {
		return
	}
	if !a.S.Allow(ctx, id) {
		a.send(ctx, b, id, "Слишком много действий. Подождите минуту.", nil)
		return
	}
	if e := a.handle(ctx, b, id, text, contact, cb != nil); e != nil {
		message := "Не удалось выполнить действие. Попробуйте ещё раз. Если ошибка повторяется, обратитесь к сотруднику магазина."
		var public security.UserError
		if errors.As(e, &public) {
			message = public.Error()
		} else {
			slog.Warn("operation failed")
		}
		a.send(ctx, b, id, message, inline("В главное меню", "menu"))
	}
}
func (a *App) handle(ctx context.Context, b *tg.Bot, id int64, text string, contact *models.Contact, callback bool) error {
	d := a.S.Dialog(ctx, id)
	role := a.S.Role(ctx, id)
	if text == "/cancel" || text == "❌ Отмена действия" || text == "menu" || text == "Назад" {
		if p := d.Data["purchase"]; p != "" {
			_ = a.S.Cancel(ctx, id, p, false)
		}
		a.S.Clear(ctx, id)
		a.menu(ctx, b, id)
		return nil
	}
	if strings.HasPrefix(text, "/start") {
		if _, e := a.S.Customer(ctx, id); e == nil {
			a.S.Clear(ctx, id)
			a.menu(ctx, b, id)
			return nil
		}
		if role != "" {
			a.menu(ctx, b, id)
			a.send(ctx, b, id, "Для личной бонусной карты нажмите «📱 Регистрация по номеру». Работа с клиентами уже доступна в меню.", nil)
			return nil
		}
		ref := strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(text, "/start")), "ref_")
		if len(ref) != 32 {
			ref = ""
		}
		return a.registration(ctx, b, id, ref)
	}
	if text == "/register" || text == "📱 Регистрация по номеру" {
		if _, e := a.S.Customer(ctx, id); e == nil {
			a.menu(ctx, b, id)
			return nil
		}
		return a.registration(ctx, b, id, "")
	}
	if text == "privacy" || text == "consent" {
		return storage.ErrState
	}
	if contact != nil {
		if d.Step != "contact" {
			return security.Fail("Для регистрации нажмите /register и поделитесь своим номером")
		}
		_, e := a.S.Register(ctx, id, contact.UserID, contact.PhoneNumber, "", d.Data["ref"])
		if e != nil {
			return e
		}
		a.S.Clear(ctx, id)
		a.send(ctx, b, id, "✅ Регистрация завершена! Теперь вы можете получать и тратить бонусы.", nil)
		a.menu(ctx, b, id)
		return nil
	}
	if strings.HasPrefix(text, "approve:") || strings.HasPrefix(text, "sale:") || strings.HasPrefix(text, "/finish ") || text == "✅ Завершить списание" {
		return storage.ErrState
	}
	if strings.HasPrefix(text, "cancel:") {
		return a.S.Cancel(ctx, id, strings.TrimPrefix(text, "cancel:"), false)
	}
	if strings.HasPrefix(text, "hist:") {
		parts := strings.Split(text, ":")
		if len(parts) != 3 {
			return storage.ErrState
		}
		c, _ := strconv.ParseInt(parts[1], 10, 64)
		p, _ := strconv.Atoi(parts[2])
		return a.history(ctx, b, id, c, p)
	}
	if strings.HasPrefix(text, "admin_apply:") {
		if role != "ADMIN" {
			return storage.ErrDenied
		}
		if d.Step != "admin_confirm" || strings.TrimPrefix(text, "admin_apply:") != d.Data["nonce"] {
			return storage.ErrState
		}
		var e error
		switch d.Data["kind"] {
		case "employee":
			e = a.S.SetEmployee(ctx, id, num(d.Data["target"]), d.Data["active"] == "1")
		case "settings", "recover", "adjust":
			return storage.ErrState

		case "return":
			e = a.S.Return(ctx, id, d.Data["purchase"], d.Data["reason"])

		default:
			return storage.ErrState
		}
		if e != nil {
			return e
		}
		a.S.Clear(ctx, id)
		a.send(ctx, b, id, "✅ Изменения сохранены.", inline("В меню", "menu"))
		return nil
	}
	// Menu buttons interrupt an unfinished input dialog without creating financial effects.
	switch text {
	case "🎁 Мои бонусы":
		c, e := a.S.Customer(ctx, id)
		if e != nil {
			return security.Fail("Для регистрации нажмите /register")
		}
		a.send(ctx, b, id, fmt.Sprintf("🎁 Ваши бонусы\n\nДоступно: %d бонусов", c.Balance-c.Reserved), nil)
		return nil
	case "👥 Приведи друга":
		c, e := a.S.Customer(ctx, id)
		if e != nil {
			return security.Fail("Сначала /register")
		}
		link := "https://t.me/" + a.Username + "?start=ref_" + c.Referral
		a.send(ctx, b, id, fmt.Sprintf("👥 Приведи друга\n\nДруг регистрируется по вашей ссылке — вы оба сразу получаете по 500 бонусов. Бонусы можно потратить на аксессуары.\n\n%s", link), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Поделиться ссылкой", URL: "https://t.me/share/url?url=" + url.QueryEscape(link)}}}})
		return nil
	case "📜 История операций":
		c, e := a.S.Customer(ctx, id)
		if e != nil {
			return security.Fail("Сначала /register")
		}
		return a.history(ctx, b, id, c.ID, 0)
	case "ℹ️ Правила программы", "⚙️ Настройки", "🔐 Восстановление", "💳 Моя карта", "🛠 Корректировка":
		return storage.ErrState
	case "📞 Контакты":
		a.send(ctx, b, id, a.C.Contacts, nil)
		return nil
	case "🔎 Найти клиента", "➕ Начислить бонусы", "➖ Списать бонусы", "📚 История клиента":
		if role == "" {
			return storage.ErrDenied
		}
		mode := "find"
		if text == "➕ Начислить бонусы" {
			mode = "earn"
		}
		if text == "➖ Списать бонусы" {
			mode = "redeem"
		}
		if text == "📚 История клиента" {
			mode = "history"
		}
		if text != "🔎 Найти клиента" && d.Step == "client" && d.Data["customer"] != "" {
			data := map[string]string{"customer": d.Data["customer"], "mode": mode}
			if mode == "earn" {
				data["key"] = security.Token()
				return a.prompt(ctx, b, id, "award", data, "Сколько бонусов начислить клиенту?")
			}
			if mode == "history" {
				return a.history(ctx, b, id, num(data["customer"]), 0)
			}
			data["key"] = security.Token()
			return a.prompt(ctx, b, id, "debit", data, "Сколько бонусов списать у клиента?")
		}
		return a.prompt(ctx, b, id, "find", map[string]string{"mode": mode}, "Введите номер телефона клиента или отсканированный QR-код.")
	case "↩️ Возврат покупки":
		if role != "ADMIN" {
			return security.Fail("Возврат проводит администратор после проверки чека. Обратитесь к нему с ID покупки")
		}
		return a.prompt(ctx, b, id, "return", map[string]string{}, "Введите ID покупки и причину возврата через пробел.")
	case "👔 Сотрудники":
		if role != "ADMIN" {
			return storage.ErrDenied
		}
		return a.prompt(ctx, b, id, "employee", map[string]string{}, "Введите Telegram ID сотрудника и 1 (добавить/включить) или 0 (заблокировать). Например: 123456789 1")
	case "📊 Статистика", "🧾 Журнал":
		v, e := a.S.Report(ctx, id, text == "🧾 Журнал")
		if e != nil {
			return e
		}
		a.send(ctx, b, id, v, nil)
		return nil
	}
	if callback {
		return storage.ErrState
	}
	if d.Step == "" {
		a.send(ctx, b, id, "Выберите пункт меню. Регистрация: /register. Отмена: /cancel", inline("В меню", "menu"))
		return nil
	}
	return a.input(ctx, b, id, text, d, role)
}
func num(s string) int64 { v, _ := strconv.ParseInt(s, 10, 64); return v }
func (a *App) registration(ctx context.Context, b *tg.Bot, id int64, ref string) error {
	if e := a.S.SetDialog(ctx, id, "contact", map[string]string{"ref": ref}); e != nil {
		return e
	}
	a.send(ctx, b, id, welcome+"\n\n🎁 Регистрируйтесь, получайте бонусы и тратьте их на аксессуары.\n\nДля регистрации поделитесь своим номером телефона кнопкой ниже.", &models.ReplyKeyboardMarkup{ResizeKeyboard: true, Keyboard: [][]models.KeyboardButton{{{Text: "📱 Поделиться номером", RequestContact: true}}, {{Text: "❌ Отмена действия"}}}})
	return nil
}
func (a *App) prompt(ctx context.Context, b *tg.Bot, id int64, step string, data map[string]string, msg string) error {
	if e := a.S.SetDialog(ctx, id, step, data); e != nil {
		return e
	}
	a.send(ctx, b, id, msg, keyboard([]string{"Назад", "❌ Отмена действия"}))
	return nil
}
func (a *App) history(ctx context.Context, b *tg.Bot, id, c int64, p int) error {
	t, e := a.S.History(ctx, id, c, p)
	if e != nil {
		return e
	}
	buttons := []models.InlineKeyboardButton{}
	if p > 0 {
		buttons = append(buttons, models.InlineKeyboardButton{Text: "← Новее", CallbackData: fmt.Sprintf("hist:%d:%d", c, p-1)})
	}
	buttons = append(buttons, models.InlineKeyboardButton{Text: "Старее →", CallbackData: fmt.Sprintf("hist:%d:%d", c, p+1)})
	a.send(ctx, b, id, fmt.Sprintf("📜 История · страница %d\n\n%s", p+1, t), &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{buttons, {{Text: "В меню", CallbackData: "menu"}}}})
	return nil
}
