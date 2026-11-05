package security

import (
	"crypto/rand"
	"encoding/hex"

	"strings"
	"unicode"
)

func Token() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func Phone(raw string) (string, error) {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if r != '+' && r != '-' && r != '(' && r != ')' && !unicode.IsSpace(r) {
			return "", Fail("Некорректный телефон")
		}
	}
	s := b.String()
	if len(s) == 11 && s[0] == '8' {
		s = "7" + s[1:]
	}
	if len(s) < 10 || len(s) > 15 || s[0] == '0' {
		return "", Fail("Нужен международный номер телефона")
	}
	return "+" + s, nil
}
func Contact(sender, owner int64, phone string) (string, error) {
	if sender <= 0 || sender != owner {
		return "", Fail("Отправьте свой контакт кнопкой Telegram")
	}
	return Phone(phone)
}
func Mask(p string) string {
	if len(p) < 5 {
		return "***"
	}
	return p[:2] + "••••••" + p[len(p)-4:]
}
func Amount(a int64) bool                            { return a > 0 && a <= 100000000 }
func MaxRedeem(balance, amount, percent int64) int64 { return min(balance, amount*percent/100) }

// UserError contains a deliberately public message, never a database or API error.
type UserError string

func (e UserError) Error() string { return string(e) }
func Fail(s string) error         { return UserError(s) }
