package security

import "testing"

func TestContact(t *testing.T) {
	if _, e := Contact(1, 2, "89991234567"); e == nil {
		t.Fatal("foreign contact accepted")
	}
	p, e := Contact(1, 1, "8 (999) 123-45-67")
	if e != nil || p != "+79991234567" {
		t.Fatal(p, e)
	}
	for _, p := range []string{"123", "+00000000000", "abc"} {
		if _, e := Phone(p); e == nil {
			t.Fatal(p)
		}
	}
}
func TestArithmetic(t *testing.T) {
	if MaxRedeem(3000, 4000, 50) != 2000 {
		t.Fatal("limit")
	}
	if MaxRedeem(100, 4000, 50) != 100 {
		t.Fatal("balance")
	}
	if (8000 * 5 / 100) != 400 {
		t.Fatal("cashback")
	}
	if MaxRedeem(1000, 1001, 50) != 500 {
		t.Fatal("floor")
	}
}
func TestTokens(t *testing.T) {
	a, b := Token(), Token()
	if a == b || len(a) != 32 {
		t.Fatal("unsafe token")
	}
	if Mask("+79991234567") == "+79991234567" {
		t.Fatal("phone exposed")
	}
}
