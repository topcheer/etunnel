package server

// Offline geolocation for Telegram pushes (ip2region xdb).
//
// The formatting and the "lookup happens only in the push path, never on
// the handshake path" contract are tested with a fake; the real searcher is
// exercised by TestGeoIPRealXDB when ETUNNEL_TEST_XDB_V4 points at an xdb.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeGeo struct{ regions map[string]string }

func (f *fakeGeo) lookup(ip string) string { return f.regions[ip] }

func TestFormatGeoRegion(t *testing.T) {
	cases := map[string]string{
		"沙特阿拉伯|0|东部省|0|Mobily": "沙特阿拉伯 · 东部省 · Mobily",
		"中国|0|广东省|深圳市|电信":      "中国 · 广东省 · 深圳市 · 电信",
		"美国|0|加利福尼亚|加利福尼亚|0":   "美国 · 加利福尼亚", // repeated province/city collapses
		"0|0|0|0|0": "",
		"United States|California|0|Google LLC|US": "United States · California · Google LLC", // v3 ISO suffix dropped
		"GB|0|0|0":  "GB", // a lone code is the only field: kept
		"":          "",
		" 内联 | 1 |": "内联 · 1",
	}
	for in, want := range cases {
		if got := formatGeoRegion(in); got != want {
			t.Errorf("formatGeoRegion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGeoSuffixDisabledAndUnknown(t *testing.T) {
	s := &Server{} // geo nil
	if s.geoSuffix("94.192.76.3") != "" {
		t.Fatal("disabled geolocation must add nothing")
	}
	s.geo = &fakeGeo{regions: map[string]string{"94.192.76.3": "沙特阿拉伯|0|东部省|0|Mobily"}}
	if got := s.geoSuffix("94.192.76.3"); got != " (沙特阿拉伯 · 东部省 · Mobily)" {
		t.Fatalf("geoSuffix = %q", got)
	}
	if got := s.geoSuffix("203.0.113.9"); got != "" {
		t.Fatalf("unknown ip produced %q", got)
	}
}

// newTGServer wires a fake Bot API and returns the server plus the sent texts.
func newTGServer(t *testing.T, geo geoLookup) (*Server, chan string) {
	t.Helper()
	sent := make(chan string, 8)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent <- body.Text
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(fake.Close)
	s := &Server{
		cfg:     Config{MaxTunnelsPerIP24PerMin: 2},
		counter: newMemCounter(),
		tg:      telegramConfig{token: "TEST:TOKEN", chatID: 1, apiBase: fake.URL},
		geo:     geo,
	}
	return s, sent
}

func waitText(t *testing.T, sent chan string) string {
	t.Helper()
	select {
	case m := <-sent:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("no telegram message delivered")
		return ""
	}
}

func TestNotifyTunnelOpenCarriesGeo(t *testing.T) {
	clearBursts()
	s, sent := newTGServer(t, &fakeGeo{regions: map[string]string{
		"94.192.76.3": "沙特阿拉伯|0|东部省|0|Mobily",
	}})
	sess := &Session{sub: "demo", url: "https://demo.t.test", ip: "94.192.76.3"}
	s.notifyTunnelOpen(sess, "Free")
	msg := waitText(t, sent)
	for _, want := range []string{"ip 94.192.76.3 (沙特阿拉伯 · 东部省 · Mobily)", "🚇 tunnel open: demo"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("notify %q lacks %q", msg, want)
		}
	}
}

func TestBurstAlertCarriesGeo(t *testing.T) {
	clearBursts()
	s, sent := newTGServer(t, &fakeGeo{regions: map[string]string{
		"94.192.76.3": "沙特阿拉伯|0|东部省|0|Mobily",
	}})
	ctx := t.Context()
	for _, ip := range []string{"94.192.76.1", "94.192.76.2"} {
		if !s.takeIP24Slot(ctx, ip) {
			t.Fatalf("%s rejected under the limit", ip)
		}
	}
	if s.takeIP24Slot(ctx, "94.192.76.3") {
		t.Fatal("over the limit granted")
	}
	msg := waitText(t, sent)
	if !strings.Contains(msg, "last ip 94.192.76.3 (沙特阿拉伯 · 东部省 · Mobily)") {
		t.Fatalf("burst alert %q lacks geo label", msg)
	}
}

func TestWhoisCarriesGeo(t *testing.T) {
	s, _ := newTGServer(t, &fakeGeo{regions: map[string]string{"198.51.100.7": "中国|0|广东省|深圳市|电信"}})
	s.reg = newRegistry()
	sess := &Session{sub: "whois-geo", ip: "198.51.100.7"}
	if err := s.reg.Acquire("whois-geo", sess); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	out := s.handleAdminCommand("/whois whois-geo")
	if !strings.Contains(out, "anonymous (free plan) · ip 198.51.100.7 (中国 · 广东省 · 深圳市 · 电信)") {
		t.Fatalf("whois = %q", out)
	}
}

func TestNewGeoIPEmptyIsOffAndBadFileIsFatal(t *testing.T) {
	g, err := newGeoIP("", "")
	if err != nil || g != nil {
		t.Fatalf("both empty must be off, got g=%v err=%v", g, err)
	}
	if _, err := newGeoIP("/nonexistent/ip2region.xdb", ""); err == nil {
		t.Fatal("a configured but missing xdb must fail startup, not silently disable")
	}
}

// TestGeoIPRealXDB runs against a real ip2region v4 file when one is
// provided, so the integration (version constant, Search call, region
// shape) is proven end to end at least once.
//
//	ETUNNEL_TEST_XDB_V4=/path/ip2region_v4.xdb go test ./server -run TestGeoIPRealXDB -v
func TestGeoIPRealXDB(t *testing.T) {
	path := os.Getenv("ETUNNEL_TEST_XDB_V4")
	if path == "" {
		t.Skip("set ETUNNEL_TEST_XDB_V4 to run against a real ip2region xdb")
	}
	g, err := newGeoIP(path, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer g.Close()
	for _, ip := range []string{"94.192.76.3", "114.114.114.114", "8.8.8.8"} {
		region := g.lookup(ip)
		t.Logf("%s -> %q / %q", ip, region, formatGeoRegion(region))
		if region == "" {
			t.Errorf("%s: no region", ip)
		}
	}
	if g.lookup("240e:3b7::1") != "" { // v6 with no v6 file
		t.Fatal("v6 lookup must be empty without a v6 xdb")
	}
	if g.lookup("not-an-ip") != "" || g.lookup("::ffff:94.192.76.3") != g.lookup("94.192.76.3") {
		t.Fatal("garbage / v4-mapped handling broken")
	}
}
