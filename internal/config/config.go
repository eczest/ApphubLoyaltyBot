package config

import (
	"errors"
	"os"
	"strconv"
)

type Config struct {
	Token, Database, Contacts, Store, Register string
	Admin                                      int64
}

func Load() (Config, error) {
	c := Config{Token: os.Getenv("BOT_TOKEN"), Database: os.Getenv("DATABASE_URL"), Contacts: get("STORE_CONTACTS", "https://t.me/apphub_store"), Store: get("STORE_ID", "main"), Register: get("REGISTER_ID", "main")}
	c.Admin, _ = strconv.ParseInt(os.Getenv("ADMIN_TELEGRAM_ID"), 10, 64)
	if c.Database == "" || c.Admin <= 0 {
		return c, errors.New("задайте DATABASE_URL и ADMIN_TELEGRAM_ID")
	}
	return c, nil
}
func get(k, d string) string {
	if s := os.Getenv(k); s != "" {
		return s
	}
	return d
}
