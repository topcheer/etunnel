package etunnel_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// #62 repro: fire requests through the tunnel IMMEDIATELY after welcome and
// time each. Production showed the first 1-3 deterministic 502s (~10s each),
// healing after ~30s.
func TestFirstRequestsNot502(t *testing.T) {
	_, srvURL := startTestServer(t, nil)
	local := newLocalService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	tun := connectTunnel(t, srvURL, local, nil)
	cli := clientThroughServer(t, srvURL)

	for i := 1; i <= 8; i++ {
		t0 := time.Now()
		resp, err := cli.Get(tun.URL() + fmt.Sprintf("/r%d", i))
		if err != nil {
			t.Fatalf("r%d: %v", i, err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		t.Logf("r%d: HTTP %d in %.2fs body=%q", i, resp.StatusCode, time.Since(t0).Seconds(), clip62(string(body)))
		if resp.StatusCode != http.StatusOK {
			t.Errorf("r%d: got %d, want 200", i, resp.StatusCode)
		}
	}
}

func clip62(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}
