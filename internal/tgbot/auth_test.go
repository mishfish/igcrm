package tgbot

import "testing"

func TestParseAllowlist(t *testing.T) {
	a, err := ParseAllowlist(" 111, 222 ,333")
	if err != nil || !a.Allowed(111) || !a.Allowed(222) || a.Allowed(444) {
		t.Fatalf("allowlist=%v err=%v", a, err)
	}
}

func TestParseAllowlist_RejectsGarbage(t *testing.T) {
	if _, err := ParseAllowlist("111,abc"); err == nil {
		t.Fatal("expected error for non-numeric id")
	}
}
