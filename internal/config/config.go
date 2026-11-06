package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Token, Database, Contacts, Store, Register string
	AdminIDs                                   []int64
}

func Load() (Config, error) {
	c := Config{Token: os.Getenv("BOT_TOKEN"), Database: os.Getenv("DATABASE_URL"), Contacts: get("STORE_CONTACTS", "https://t.me/apphub_store"), Store: get("STORE_ID", "main"), Register: get("REGISTER_ID", "main")}
	if c.Database == "" {
		return c, errors.New("задайте DATABASE_URL")
	}
	seen := make(map[int64]bool)
	for _, key := range []string{"ADMIN_TELEGRAM_ID", "ADMIN_TELEGRAM_IDS"} {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		for _, value := range strings.Split(raw, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil || id <= 0 {
				return c, errors.New(key + ": укажите положительные числовые Telegram ID через запятую")
			}
			if !seen[id] {
				c.AdminIDs = append(c.AdminIDs, id)
				seen[id] = true
			}
		}
	}
	if len(c.AdminIDs) == 0 {
		return c, errors.New("задайте ADMIN_TELEGRAM_IDS или ADMIN_TELEGRAM_ID")
	}
	return c, nil
}
func get(k, d string) string {
	if s := os.Getenv(k); s != "" {
		return s
	}
	return d
}
