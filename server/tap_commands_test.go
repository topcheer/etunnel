package server

// #tap-commands regressions: parameter-less /whois, /kick, /delete, /key,
// /tier answer with tappable full-command rows on a dynamic reply keyboard;
// an empty registry falls back to the usage text; with-arg forms are
// unchanged.

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// tapDispatch routes one admin update through the real dispatcher and
// returns the reply text plus the reply keyboard payload.
func tapDispatch(t *testing.T, s *Server, fake *fakeTelegram, text string) (string, any) {
	t.Helper()
	update := map[string]any{
		"update_id": 1.0,
		"message":   map[string]any{"chat": map[string]any{"id": 42.0}, "text": text},
	}
	s.dispatchTelegramUpdate(context.Background(), update)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sent) == 0 {
		t.Fatalf("no reply for %q", text)
	}
	textOut := fake.sent[len(fake.sent)-1]
	mu := fake.markups[len(fake.markups)-1]
	return textOut, mu
}

func seedTunnel(t *testing.T, s *Server, sub, email string) {
	t.Helper()
	sess := &Session{
		srv:     s,
		id:      "sess-" + sub,
		sub:     sub,
		url:     "https://" + sub + ".t.test",
		ip:      "203.0.113.9:4444",
		account: &Account{Email: email, Tier: "pro"},
		closed:  make(chan struct{}),
	}
	if err := s.reg.Acquire(sub, sess); err != nil {
		t.Fatalf("seed tunnel %s: %v", sub, err)
	}
}

func markupRows(mu any) [][]string {
	m, _ := mu.(map[string]any)
	if m == nil {
		return nil
	}
	kb, _ := m["keyboard"].([]any)
	rows := make([][]string, 0, len(kb))
	for _, r := range kb {
		row, _ := r.([]any)
		out := make([]string, 0, len(row))
		for _, c := range row {
			cs, _ := c.(string)
			out = append(out, cs)
		}
		rows = append(rows, out)
	}
	return rows
}

func TestTapKickListsLiveTunnels(t *testing.T) {
	fake, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 42)
	seedTunnel(t, s, "tap-otter-x1", "dev1@x.com")
	seedTunnel(t, s, "tap-frog-x2", "dev2@x.com")

	text, mu := tapDispatch(t, s, fake, "/kick")
	if !strings.Contains(text, "/kick tap-otter-x1") || !strings.Contains(text, "/kick tap-frog-x2") {
		t.Fatalf("kick list missing tap rows: %q", text)
	}
	if !strings.Contains(text, "dev1@x.com") {
		t.Fatal("owner summary missing from the kick list")
	}
	rows := markupRows(mu)
	if rows == nil {
		t.Fatal("dynamic reply keyboard missing (#tap-commands)")
	}
	joined := fmt.Sprint(rows)
	if !strings.Contains(joined, "/kick tap-otter-x1") || !strings.Contains(joined, "/kick tap-frog-x2") {
		t.Fatalf("keyboard rows missing kick commands: %v", rows)
	}
	// Tapping actually executes: dispatch a full kick row.
	text2, _ := tapDispatch(t, s, fake, "/kick tap-otter-x1")
	if !strings.Contains(text2, "kicked tap-otter-x1") {
		t.Fatalf("tapped kick row did not execute: %q", text2)
	}
}

func TestTapKeyListsAccounts(t *testing.T) {
	fake, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 42)
	if _, err := s.accounts.Create("ka@x.com", "free"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.accounts.Create("kb@x.com", "free"); err != nil {
		t.Fatal(err)
	}

	text, mu := tapDispatch(t, s, fake, "/key")
	if !strings.Contains(text, "/key ka@x.com") || !strings.Contains(text, "/key kb@x.com") {
		t.Fatalf("key list missing per-account rows: %q", text)
	}
	rows := markupRows(mu)
	if len(rows) == 0 {
		t.Fatal("dynamic reply keyboard missing")
	}
	joined := fmt.Sprint(rows)
	if !strings.Contains(joined, "/key ka@x.com") {
		t.Fatalf("keyboard rows missing /key rows: %v", rows)
	}
}

func TestTapTierListsUpgradeTiers(t *testing.T) {
	fake, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 42)
	if _, err := s.accounts.Create("tt@x.com", "free"); err != nil {
		t.Fatal(err)
	}

	text, mu := tapDispatch(t, s, fake, "/tier")
	if !strings.Contains(text, "/tier tt@x.com starter") || !strings.Contains(text, "/tier tt@x.com pro") {
		t.Fatalf("tier list missing upgrade rows: %q", text)
	}
	rows := markupRows(mu)
	joined := fmt.Sprint(rows)
	if strings.Contains(joined, "free") {
		t.Fatalf("free must not be an upgrade target: %v", rows)
	}
}

func TestTapEmptyRegistryFallsBackToUsage(t *testing.T) {
	fake, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 42)

	for _, cmd := range []string{"/whois", "/kick", "/delete", "/key", "/tier"} {
		text, mu := tapDispatch(t, s, fake, cmd)
		if !strings.Contains(text, "usage: "+cmd) {
			t.Fatalf("%s empty-registry reply = %q, want usage fallback", cmd, text)
		}
		// the standard main-menu keyboard is fine; only the tap-list must
		// be absent.
		for _, row := range markupRows(mu) {
			if len(row) > 0 && strings.HasPrefix(row[0], cmd+" ") {
				t.Fatalf("%s empty registry shipped a tap row: %v", cmd, row)
			}
		}
	}
}

func TestTapWithArgUnchanged(t *testing.T) {
	fake, api := newFakeTelegram(t)
	s := newTGTestServer(t, api, 42)
	seedTunnel(t, s, "arg-otter-x9", "arg@x.com")

	// with-arg whois: plain detail, keyboard = main menu (nil custom).
	text, mu := tapDispatch(t, s, fake, "/whois arg-otter-x9")
	if !strings.Contains(text, "arg@x.com") {
		t.Fatalf("whois detail lost: %q", text)
	}
	// with-arg keeps the standard main menu (unchanged behavior) - no tap
	// rows may appear.
	for _, row := range markupRows(mu) {
		if len(row) > 0 && strings.HasPrefix(row[0], "/whois ") {
			t.Fatal("with-arg command must keep the main menu keyboard")
		}
	}
	// unknown target keeps the exact old usage text.
	text2, _ := tapDispatch(t, s, fake, "/whois nope")
	if !strings.Contains(text2, "no such tunnel: nope") {
		t.Fatalf("with-arg whois miss changed: %q", text2)
	}
	// direct string API unchanged (compat with any external caller).
	if got := s.handleAdminCommand("/kick"); !strings.HasPrefix(got, "usage: /kick") && !strings.Contains(got, "usage") {
		t.Fatalf("handleAdminCommand compat broken: %q", got)
	}
}
