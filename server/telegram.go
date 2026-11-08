package server

// Telegram admin bot: manage the tunnel server from a whitelisted chat.
//
// Env (either empty -> module fully disabled, like billing):
//   TELEGRAM_BOT_TOKEN    from @BotFather
//   TELEGRAM_ADMIN_CHAT_ID  numeric chat id allowed to send commands
//
// Zero dependencies: stdlib long-polling against the Bot API.
// Security: every update is checked against the admin chat id before any
// handling; other chats are dropped without a reply.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type telegramConfig struct {
	token   string
	chatID  int64
	apiBase string // override for tests
}

func telegramFromEnv() telegramConfig {
	cfg := telegramConfig{
		token:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		apiBase: "https://api.telegram.org",
	}
	if v := os.Getenv("TELEGRAM_ADMIN_CHAT_ID"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.chatID = n
		}
	}
	return cfg
}

func (c telegramConfig) enabled() bool { return c.token != "" && c.chatID != 0 }

// startTelegram launches the long-polling loop when configured.
func (s *Server) startTelegram(ctx context.Context) {
	s.tg = telegramFromEnv()
	if !s.tg.enabled() {
		return
	}
	go s.telegramPollLoop(ctx)
	go s.telegramAlertPatrol(ctx)
	s.logf("telegram: admin bot enabled (chat %d)", s.tg.chatID)
}

func (s *Server) telegramAPI(method string, payload any) (map[string]any, error) {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, s.tg.apiBase+"/bot"+s.tg.token+"/"+method, strings.NewReader(string(b)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if ok, _ := out["ok"].(bool); !ok {
		return nil, fmt.Errorf("telegram %s: %v", method, out["description"])
	}
	return out, nil
}

func (s *Server) tgSend(text string) {
	_, _ = s.telegramAPI("sendMessage", map[string]any{
		"chat_id": s.tg.chatID,
		"text":    text,
	})
}

func (s *Server) telegramPollLoop(ctx context.Context) {
	offset := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		out, err := s.telegramAPI("getUpdates", map[string]any{
			"offset":          offset,
			"timeout":         25,
			"allowed_updates": []string{"message"},
		})
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		updates, _ := out["result"].([]any)
		for _, u := range updates {
			m, _ := u.(map[string]any)
			id, _ := m["update_id"].(float64)
			offset = int(id) + 1
			msg, _ := m["message"].(map[string]any)
			if msg == nil {
				continue
			}
			chat, _ := msg["chat"].(map[string]any)
			chatID, _ := chat["id"].(float64)
			// Whitelist: non-admin chats are dropped silently.
			if int64(chatID) != s.tg.chatID {
				continue
			}
			text, _ := msg["text"].(string)
			if reply := s.handleAdminCommand(text); reply != "" {
				s.tgSend(reply)
			}
		}
	}
}

// handleAdminCommand executes one whitelisted command and returns the reply.
func (s *Server) handleAdminCommand(text string) string {
	text = strings.TrimSpace(text)
	cmd, rest, _ := strings.Cut(text, " ")
	rest = strings.TrimSpace(rest)
	switch cmd {
	case "/start", "/help":
		return "etunnel admin bot\n" +
			"/status - server overview\n" +
			"/tunnels - live tunnels\n" +
			"/tunnel <subdomain> - one tunnel in detail\n" +
			"/accounts - registered accounts\n" +
			"/kick <subdomain> - drop a tunnel\n" +
			"/tier <email> <tier> - change account tier\n" +
			"/whois <subdomain> - tunnel owner\n" +
			"/key <email> - show API key\n" +
			"/delete <email> - remove account"
	case "/status":
		var in, out int64
		if s.metrics != nil {
			in, out = s.metrics.bytesIn.Load(), s.metrics.bytesOut.Load()
		}
		billing := "off"
		if s.billing.StripeSecretKey != "" || s.billing.PayPalClientID != "" {
			billing = "on"
		}
		return fmt.Sprintf("etunnel %s\nuptime %s\ntunnels %d\naccounts %d\ntraffic in %s / out %s\nbilling %s",
			s.cfg.Domain,
			time.Since(s.metrics.startedAt).Round(time.Second),
			s.reg.Count(),
			len(s.accounts.List()),
			humanBytes(in), humanBytes(out),
			billing)
	case "/tunnels":
		sessions := s.reg.Snapshot()
		if len(sessions) == 0 {
			return "no live tunnels"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d live tunnels:\n", len(sessions))
		for _, sess := range sessions {
			acct := "-"
			if sess.account != nil {
				acct = sess.account.Email
			}
			exp := ""
			if !sess.expires.IsZero() {
				exp = " exp " + time.Until(sess.expires).Round(time.Minute).String()
			}
			fmt.Fprintf(&b, "%s [%s] %s ip %s streams %d%s\n",
				sess.Subdomain(), sess.Plan(), acct, sess.RemoteIP(), sess.streamCount(), exp)
		}
		return b.String()
	case "/accounts":
		accts := s.accounts.List()
		if len(accts) == 0 {
			return "no accounts"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d accounts:\n", len(accts))
		for _, a := range accts {
			fmt.Fprintf(&b, "%s [%s] since %s\n", a.Email, a.Tier, a.Created.Format("2006-01-02"))
		}
		return b.String()
	case "/kick":
		if rest == "" {
			return "usage: /kick <subdomain>"
		}
		sess := s.reg.Lookup(rest)
		if sess == nil {
			return "no such tunnel: " + rest
		}
		sess.Close("kicked by admin")
		return "kicked " + rest
	case "/tier":
		email, tier, _ := strings.Cut(rest, " ")
		if email == "" || tier == "" {
			return "usage: /tier <email> <tier>"
		}
		if _, ok := s.cfg.Tiers[tier]; !ok || tier == "free" {
			return "unknown tier: " + tier
		}
		email = strings.ToLower(strings.TrimSpace(email))
		if _, err := s.activate(email, tier); err != nil {
			return "error: " + err.Error()
		}
		return email + " -> " + tier
	case "/whois":
		if rest == "" {
			return "usage: /whois <subdomain>"
		}
		sess := s.reg.Lookup(rest)
		if sess == nil {
			return "no such tunnel: " + rest
		}
		if sess.account == nil {
			return rest + ": anonymous (free plan)"
		}
		return fmt.Sprintf("%s: %s [%s]", rest, sess.account.Email, sess.account.Tier)
	case "/key":
		if rest == "" {
			return "usage: /key <email>"
		}
		acct, ok := s.accounts.LookupEmail(strings.ToLower(rest))
		if !ok {
			return "no such account: " + rest
		}
		return rest + " API key:\n" + acct.APIKey
	case "/delete":
		if rest == "" {
			return "usage: /delete <email>"
		}
		acct, ok := s.accounts.LookupEmail(strings.ToLower(rest))
		if !ok {
			return "no such account: " + rest
		}
		if !s.accounts.Delete(acct.APIKey) {
			return "delete failed"
		}
		return "deleted " + acct.Email
	case "/tunnel":
		if rest == "" {
			return "usage: /tunnel <subdomain>"
		}
		sess := s.reg.Lookup(rest)
		if sess == nil {
			return "no such tunnel: " + rest
		}
		acct := "anonymous (free)"
		if sess.account != nil {
			acct = sess.account.Email + " [" + sess.account.Tier + "]"
		}
		exp := "unlimited"
		if !sess.expires.IsZero() {
			exp = time.Until(sess.expires).Round(time.Second).String()
		}
		return fmt.Sprintf("%s\nplan %s\nowner %s\nip %s\nstreams %d\nexpires in %s",
			sess.URL(), sess.Plan(), acct, sess.RemoteIP(), sess.streamCount(), exp)
	}
	return ""
}

// notifyPayment pushes a purchase event to the admin chat (no-op when the
// bot is disabled). Called from billing activation paths.
var _ = sync.Once{}

func (s *Server) notifyPayment(email, tier string) {
	if !s.tg.enabled() {
		return
	}
	s.tgSend(fmt.Sprintf("💰 %s purchased %s", email, tier))
}

// notifyPaymentFailure reports declined/failed payment attempts.
func (s *Server) notifyPaymentFailure(email, tier, reason string) {
	if !s.tg.enabled() {
		return
	}
	s.tgSend(fmt.Sprintf("⚠️ payment FAILED for %s (%s)%s", email, tier, prefixIfSet(reason)))
}

func prefixIfSet(reason string) string {
	if reason == "" {
		return ""
	}
	return ": " + reason
}

// telegramAlertPatrol pushes a one-shot alert each time the live-tunnel
// count crosses the configured threshold from below (and re-arms after
// dropping back). TELEGRAM_ALERT_TUNNELS=0 (default) disables it.
func (s *Server) telegramAlertPatrol(ctx context.Context) {
	threshold := 0
	if v := os.Getenv("TELEGRAM_ALERT_TUNNELS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			threshold = n
		}
	}
	if threshold == 0 {
		return
	}
	armed := true
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n := s.reg.Count()
			if armed && n >= threshold {
				s.tgSend(fmt.Sprintf("🚨 tunnel threshold: %d live (>= %d)", n, threshold))
				armed = false
			} else if !armed && n < threshold {
				armed = true // re-arm so the next spike alerts again
			}
		}
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

var _ = io.Discard
