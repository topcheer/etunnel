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
	"log"
	"net/http"
	"os"
	"sort"
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

func (s *Server) telegramAPI(ctx context.Context, method string, payload any) (map[string]any, error) {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tg.apiBase+"/bot"+s.tg.token+"/"+method, strings.NewReader(string(b)))
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

func (s *Server) tgSend(ctx context.Context, text string) {
	s.tgSendKB(ctx, text, nil)
}

// tgSendKB sends text with an optional custom reply keyboard; nil keeps the
// standard main menu (#tap-commands: parameter-less admin commands answer
// with tappable full-command rows).
func (s *Server) tgSendKB(ctx context.Context, text string, kb [][]string) {
	markup := any(tgMainMenu())
	if kb != nil {
		markup = map[string]any{"keyboard": kb, "resize_keyboard": true}
	}
	if _, err := s.telegramAPI(ctx, "sendMessage", map[string]any{
		"chat_id":      s.tg.chatID,
		"text":         text,
		"reply_markup": markup,
	}); err != nil {
		// #353: silent drops made bot outages invisible.
		log.Printf("telegram: sendMessage failed: %v", err)
	}
}

// tgMainMenu is the tap-only reply keyboard shown on every bot reply.
func tgMainMenu() map[string]any {
	return map[string]any{
		"keyboard": [][]string{
			{"📊 Status", "🚇 Tunnels"},
			{"👥 Accounts", "🔍 Whois"},
			{"⬆️ Upgrade tier", "⛔ Kick"},
			{"🔑 API key", "🗑 Delete account"},
			{"❓ Help"},
		},
		"resize_keyboard": true,
	}
}

// menuAliases maps button labels to commands.
var menuAliases = map[string]string{
	"📊 Status": "/status", "🚇 Tunnels": "/tunnels",
	"👥 Accounts": "/accounts", "🔍 Whois": "/whois",
	"⬆️ Upgrade tier": "/tier", "⛔ Kick": "/kick",
	"🔑 API key": "/key", "🗑 Delete account": "/delete",
	"❓ Help": "/help",
}

func (s *Server) telegramPollLoop(ctx context.Context) {
	offset := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		out, err := s.telegramAPI(ctx, "getUpdates", map[string]any{
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
			if next := s.dispatchTelegramUpdate(ctx, u); next > offset {
				offset = next
			}
		}
	}
}

// dispatchTelegramUpdate processes one getUpdates entry: advances past its
// update_id, drops non-admin chats silently, and answers admin commands.
// Extracted from telegramPollLoop so the whitelist/offset behavior is
// directly testable without a live Telegram API (#323 M2).
func (s *Server) dispatchTelegramUpdate(ctx context.Context, u any) int {
	m, _ := u.(map[string]any)
	id, _ := m["update_id"].(float64)
	next := int(id) + 1
	msg, _ := m["message"].(map[string]any)
	if msg == nil {
		return next
	}
	chat, _ := msg["chat"].(map[string]any)
	chatID, _ := chat["id"].(float64)
	// Whitelist: non-admin chats are dropped silently.
	if int64(chatID) != s.tg.chatID {
		return next
	}
	text, _ := msg["text"].(string)
	if reply, kb := s.handleAdminCommandKB(text); reply != "" {
		s.tgSendKB(ctx, reply, kb)
	}
	return next
}

// maxTappableRows bounds the reply keyboard of parameter-less list commands
// so a large registry cannot blow up the Telegram client UI.
const maxTappableRows = 24

// telegramMsgLimit is Telegram's hard message length cap.
const telegramMsgLimit = 4096

// tapRows renders rows as a single-column keyboard and returns the TEXT
// body to send alongside it, truncated to the Telegram 4096-char message
// budget (#367: rows can be long - a 254-char email - so the cap is byte
// based, not row-count based). The tail reports the true omitted count and
// points at /tunnels//accounts for the full lists.
func tapRows(rows []string) ([][]string, string) {
	return tapRowsWith("", rows, nil)
}

// tapRowsWith is tapRows with the two review-batch-B refinements: header is
// the caller's lead line and counts against the byte budget (the truncated
// body must leave room for text the caller unconditionally prepends,
// #389), and notes carries same-length human-readable body lines - the
// keyboard buttons stay EXACTLY rows[i] because a tap re-sends the button
// text as a command (#381: an owner suffix like "/kick sub (a@x.com)" or an
// unseparated "/kick subanonymous" fails reg.Lookup and the button is
// dead). maxTappableRows bounds the keyboard (#389: the cap existed only as
// a dead constant; a huge registry must not flood the client UI).
func tapRowsWith(header string, rows []string, notes []string) ([][]string, string) {
	if notes == nil {
		notes = rows
	}
	if len(notes) != len(rows) {
		panic("tapRowsWith: notes/rows length mismatch")
	}
	kept := 0
	var b strings.Builder
	b.WriteString(header)
	if header != "" {
		b.WriteByte('\n')
	}
	// Reserve room for the tail line (~80 bytes covers the widest count).
	const tailAllowance = 96
	for i := range rows {
		if kept == maxTappableRows {
			break
		}
		if b.Len()+len(notes[i])+1 > telegramMsgLimit-tailAllowance {
			break
		}
		b.WriteString(notes[i])
		b.WriteByte('\n')
		kept++
	}
	omitted := len(rows) - kept
	if omitted > 0 {
		fmt.Fprintf(&b, "…and %d more — see /tunnels or /accounts for the full lists", omitted)
	}
	kb := make([][]string, 0, kept+1)
	for _, r := range rows[:kept] {
		kb = append(kb, []string{r})
	}
	kb = append(kb, []string{"📊 Status", "❓ Help"}) // back to the main menu
	return kb, b.String()
}

// handleAdminCommandKB executes one admin command and returns the reply plus
// an optional tappable keyboard. Parameter-less list commands (/whois, /kick,
// /delete, /key, /tier) are intercepted here and answer with ready-to-tap
// full-command rows built from the live registry and account store; an empty
// source falls back to the plain usage text. Everything else - all with-arg
// forms and the remaining commands - delegates to handleAdminCommand
// unchanged (#tap-commands).
func (s *Server) handleAdminCommandKB(text string) (string, [][]string) {
	text = strings.TrimSpace(text)
	if cmd, ok := menuAliases[text]; ok {
		text = cmd
	}
	cmd, rest, _ := strings.Cut(text, " ")
	rest = strings.TrimSpace(rest)
	if rest != "" {
		return s.handleAdminCommand(text), nil // with-arg form: unchanged
	}

	switch cmd {
	case "/whois", "/kick":
		sessions := s.reg.Snapshot()
		if len(sessions) == 0 {
			return "usage: " + cmd + " <subdomain> (no live tunnels)", nil
		}
		verb := strings.TrimPrefix(cmd, "/") // whois | kick
		rows := make([]string, 0, len(sessions))
		notes := make([]string, 0, len(sessions))
		for _, sess := range sessions {
			// #381: the button text is the bare command - a tapped row is
			// re-sent verbatim, so any suffix breaks reg.Lookup. The owner
			// summary lives on the body line, which is not clickable.
			owner := "anonymous"
			if sess.account != nil {
				owner = sess.account.Email
			}
			rows = append(rows, cmd+" "+sess.Subdomain())
			notes = append(notes, cmd+" "+sess.Subdomain()+" ("+owner+")")
		}
		kb, body := tapRowsWith("tap a tunnel to "+verb+":", rows, notes)
		return body, kb
	case "/delete", "/key":
		accts := s.accounts.List()
		if len(accts) == 0 {
			return "usage: " + cmd + " <email> (no accounts)", nil
		}
		verb := "delete"
		if cmd == "/key" {
			verb = "show the API key of"
		}
		rows := make([]string, 0, len(accts))
		for _, a := range accts {
			rows = append(rows, cmd+" "+a.Email)
		}
		kb, body := tapRowsWith("tap an account to "+verb+":", rows, nil)
		return body, kb
	case "/tier":
		accts := s.accounts.List()
		upgrades := make([]string, 0, len(s.cfg.Tiers))
		for name := range s.cfg.Tiers {
			if name != "free" {
				upgrades = append(upgrades, name)
			}
		}
		sort.Strings(upgrades)
		if len(accts) == 0 || len(upgrades) == 0 {
			return "usage: /tier <email> <tier> (no accounts or no upgradeable tiers)", nil
		}
		rows := make([]string, 0, len(accts)*len(upgrades))
		for _, a := range accts {
			for _, up := range upgrades {
				rows = append(rows, "/tier "+a.Email+" "+up)
			}
		}
		kb, body := tapRowsWith("tap an account+tier to upgrade:", rows, nil)
		return body, kb
	case "/bursts":
		bursts := listBursts()
		if len(bursts) == 0 {
			return "no network is throttled right now (burst guard: " + fmt.Sprint(s.cfg.MaxTunnelsPerIP24PerMin) + "/min per /24)", nil
		}
		rows := make([]string, 0, len(bursts))
		notes := make([]string, 0, len(bursts))
		for _, b := range bursts {
			rows = append(rows, "/unthrottle "+b.Prefix)
			notes = append(notes, fmt.Sprintf("%s · %d in window · last %s · %s ago",
				b.Prefix, b.Count, b.LastIP, time.Since(b.Last).Round(time.Second)))
		}
		kb, body := tapRowsWith("tap a network to unthrottle:", rows, notes)
		return body, kb
	}
	return s.handleAdminCommand(text), nil
}

// handleAdminCommand executes one whitelisted command and returns the reply.
func (s *Server) handleAdminCommand(text string) string {
	text = strings.TrimSpace(text)
	if cmd, ok := menuAliases[text]; ok {
		text = cmd
	}
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
			"/delete <email> - remove account\n" +
			"/bursts - networks throttled by the tunnel burst guard\n" +
			"/unthrottle <network> - clear a network's burst window\n" +
			"/history [5|10|30] - tunnels opened in the last N minutes"
	case "/history":
		mins := 10
		if rest != "" {
			switch rest {
			case "5", "10", "30":
				mins, _ = strconv.Atoi(rest)
			default:
				return "usage: /history [5|10|30]"
			}
		}
		if s.ledger == nil {
			return "history needs ETUNNEL_PG_DSN (session ledger)"
		}
		total, byIP, newest, err := s.ledger.recentOpens(time.Duration(mins) * time.Minute)
		if err != nil {
			return "history: " + err.Error()
		}
		var b strings.Builder
		fmt.Fprintf(&b, "tunnels opened in last %dm: %d\n", mins, total)
		for ip, c := range byIP {
			fmt.Fprintf(&b, "  %dx %s\n", c, ip)
		}
		for _, row := range newest {
			fmt.Fprintf(&b, "  %s\n", row)
		}
		return b.String()
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
	case "/bursts":
		bursts := listBursts()
		if len(bursts) == 0 {
			return "no network is throttled right now"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "throttled networks (burst guard %d/min per /24):\n", s.cfg.MaxTunnelsPerIP24PerMin)
		for _, r := range bursts {
			fmt.Fprintf(&b, "%s · %d in window · last %s · %s ago\n", r.Prefix, r.Count, r.LastIP, time.Since(r.Last).Round(time.Second))
		}
		return strings.TrimRight(b.String(), "\n")
	case "/unthrottle":
		if rest == "" || !validIP24Prefix(rest) {
			return "usage: /unthrottle <network>  (e.g. 94.192.76 - as listed by /bursts)"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cleared, err := s.resetIP24(ctx, rest)
		if err != nil {
			return fmt.Sprintf("unthrottle %s failed: %v", rest, err)
		}
		return fmt.Sprintf("unthrottled %s (cleared %d counted handshakes)", rest, cleared)
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
		s.kickSessionsByAccount(acct.APIKey, "account deleted by admin")
		if err := s.accounts.DeleteWithError(acct.APIKey); err != nil {
			// #366: the account may resurrect after restart - tell the admin.
			return "delete failed: " + err.Error()
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
func (s *Server) notifyPayment(email, tier string) {
	if !s.tg.enabled() {
		return
	}
	s.tgSend(context.Background(), fmt.Sprintf("💰 %s purchased %s", email, tier))
}

// notifyPaymentFailure reports declined/failed payment attempts.
func (s *Server) notifyPaymentFailure(email, tier, reason string) {
	if !s.tg.enabled() {
		return
	}
	s.tgSend(context.Background(), fmt.Sprintf("⚠️ payment FAILED for %s (%s)%s", email, tier, prefixIfSet(reason)))
}

// tunnelOpenCooldown suppresses repeat tunnel-open notifications for the
// same subdomain: resume reconnects walk the same handshake path as fresh
// tunnels, and a flapping supervisor would otherwise spam the admin chat.
// Tunable is a var so tests can shrink the window.
var tunnelOpenCooldown = 5 * time.Minute

// tunnelOpenSeen maps subdomain -> time of the last tunnel-open push. Plain
// sync.Map: no new dependencies, entries are tiny, and stale keys for dead
// subdomains are harmless.
var tunnelOpenSeen sync.Map

// notifyTunnelOpen pushes a tunnel-open event to the admin chat (no-op when
// the bot is disabled). The same subdomain inside tunnelOpenCooldown is
// suppressed — resume reconnects walk this same path — while different
// subdomains are independent (#notify tunnel-open).
func (s *Server) notifyTunnelOpen(sess *Session, plan string) {
	if !s.tg.enabled() {
		return
	}
	now := time.Now()
	// #323 LOW3: LoadOrStore makes the first-delivery claim atomic (two
	// concurrent handshakes for the same fresh subdomain yield exactly one
	// notification), and CompareAndSwap makes the post-cooldown renewal a
	// single-winner claim as well.
	if prev, loaded := tunnelOpenSeen.LoadOrStore(sess.sub, now); loaded {
		t0, _ := prev.(time.Time)
		if now.Sub(t0) < tunnelOpenCooldown {
			return
		}
		// #331 LOW4: a CAS loss against the sweep (which deletes the expired
		// entry) must not silently swallow the post-cooldown notification -
		// retry the LoadOrStore claim instead of returning.
		if !tunnelOpenSeen.CompareAndSwap(sess.sub, prev, now) {
			if _, loaded := tunnelOpenSeen.LoadOrStore(sess.sub, now); loaded {
				return // yet another handshake holds a fresh window
			}
		}
	}
	owner := sess.accountEmail()
	if owner == "" {
		owner = "anonymous"
	}
	text := fmt.Sprintf("🚇 tunnel open: %s (%s) — plan %s · owner %s · ip %s",
		sess.sub, sess.url, plan, owner, sess.ip)
	// #review: send asynchronously - tgSend's HTTP client has a 40s
	// timeout, and this runs on the handshake path before readLoop starts,
	// so a slow or hung Telegram API must not delay a new tunnel from
	// serving. The synchronous cooldown decision above already prevents
	// concurrent double-sends.
	go s.tgSend(context.Background(), text)
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
	armed := true
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// #323 M1: the tunnel-open cooldown map is swept on every tick
			// regardless of the alert threshold - anonymous tunnels mint a
			// fresh random subdomain per connect, so entries accumulate.
			sweepTunnelOpenSeen(time.Now())
			if threshold > 0 {
				n := s.reg.Count()
				if armed && n >= threshold {
					s.tgSend(ctx, fmt.Sprintf("🚨 tunnel threshold: %d live (>= %d)", n, threshold))
					armed = false
				} else if !armed && n < threshold {
					armed = true // re-arm so the next spike alerts again
				}
			}
		}
	}
}

// sweepTunnelOpenSeen removes tunnel-open cooldown entries older than the
// cooldown window (#323 M1: anonymous tunnels mint a fresh subdomain per
// connect, so the map grows without a periodic sweep). Returns the number
// of removed entries. Inject `now` for tests.
func sweepTunnelOpenSeen(now time.Time) int {
	removed := 0
	tunnelOpenSeen.Range(func(k, v any) bool {
		if t0, ok := v.(time.Time); ok && now.Sub(t0) >= tunnelOpenCooldown {
			tunnelOpenSeen.Delete(k)
			removed++
		}
		return true
	})
	return removed
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
