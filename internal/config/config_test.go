package config

import (
	"reflect"
	"testing"
)

func TestAdministratorIDs(t *testing.T) {
	for _, tc := range []struct {
		name, legacy, multiple string
		want                   []int64
		invalid                bool
	}{
		{name: "legacy", legacy: "123", want: []int64{123}},
		{name: "multiple", multiple: "123, 456,789", want: []int64{123, 456, 789}},
		{name: "combine and deduplicate", legacy: "123", multiple: "456,123,456", want: []int64{123, 456}},
		{name: "empty", invalid: true},
		{name: "username", multiple: "123,@admin", invalid: true},
		{name: "zero", multiple: "123,0", invalid: true},
		{name: "negative", multiple: "-123", invalid: true},
		{name: "overflow", multiple: "9223372036854775808", invalid: true},
		{name: "missing entry", multiple: "123,", invalid: true},
		{name: "invalid legacy not ignored", legacy: "abc", multiple: "123", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://localhost/test")
			t.Setenv("ADMIN_TELEGRAM_ID", tc.legacy)
			t.Setenv("ADMIN_TELEGRAM_IDS", tc.multiple)
			c, err := Load()
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid admin configuration accepted")
				}
				return
			}
			if err != nil || !reflect.DeepEqual(c.AdminIDs, tc.want) {
				t.Fatalf("admins=%v error=%v, want %v", c.AdminIDs, err, tc.want)
			}
		})
	}
}
