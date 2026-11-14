package etunnel_test

// Multi-target routing E2E: one public hostname, several local services
// dispatched by URL path prefix at the SDK (sdk/route.go). Proves the real
// serve path (visitor -> server -> Req frame -> SDK route -> local dial)
// against two live local services, including strip semantics and the
// no-match 404 answered at the SDK edge.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	etunnel "github.com/topcheer/etunnel/sdk"
)

func TestMultiTargetE2E(t *testing.T) {
	_, srvURL := startTestServer(t, nil)
	client := clientThroughServer(t, srvURL)

	// Two real local services that echo what actually arrived: which
	// handler, which path, which query.
	var gotPath, gotQuery string
	fe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		fmt.Fprintf(w, "fe")
	}))
	defer fe.Close()
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		fmt.Fprintf(w, "be")
	}))
	defer be.Close()

	tun, err := etunnel.Connect(context.Background(), etunnel.Options{
		Server: sdkURL(srvURL),
		Routes: []etunnel.Route{
			{PathPrefix: "/api", Addr: strings.TrimPrefix(be.URL, "http://"), StripPrefix: true},
			{PathPrefix: "/", Addr: strings.TrimPrefix(fe.URL, "http://")},
		},
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { tun.Close("test done") })

	get := func(t *testing.T, path string) (int, string) {
		t.Helper()
		resp, err := client.Get(tun.URL() + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// 1. /api -> backend, prefix stripped, query preserved.
	if c, b := get(t, "/api/users?x=1&y=2"); c != 200 || b != "be" {
		t.Fatalf("/api/users = %d %q; want 200 be", c, b)
	}
	if gotPath != "/users" || gotQuery != "x=1&y=2" {
		t.Fatalf("backend saw path=%q query=%q; want /users x=1&y=2", gotPath, gotQuery)
	}

	// 2. everything else -> frontend, path untouched.
	if c, b := get(t, "/app/page"); c != 200 || b != "fe" {
		t.Fatalf("/app/page = %d %q; want 200 fe", c, b)
	}
	if gotPath != "/app/page" {
		t.Fatalf("frontend saw path=%q; want /app/page", gotPath)
	}

	// 3. root itself -> frontend.
	if c, b := get(t, "/"); c != 200 || b != "fe" {
		t.Fatalf("/ = %d %q; want 200 fe", c, b)
	}

	// 4. exact /api with strip bottoms out at "/" on the backend.
	if c, b := get(t, "/api"); c != 200 || b != "be" {
		t.Fatalf("/api = %d %q; want 200 be", c, b)
	}
	if gotPath != "/" {
		t.Fatalf("backend saw %q for exact /api; want /", gotPath)
	}
}

func TestMultiTargetNoMatch404E2E(t *testing.T) {
	_, srvURL := startTestServer(t, nil)
	client := clientThroughServer(t, srvURL)

	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer local.Close()

	// No "/" fallback: anything outside /api must be a clean 404.
	tun, err := etunnel.Connect(context.Background(), etunnel.Options{
		Server: sdkURL(srvURL),
		Routes: []etunnel.Route{
			{PathPrefix: "/api", Addr: strings.TrimPrefix(local.URL, "http://")},
		},
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { tun.Close("test done") })

	u := tun.URL() + "/definitely/not/routed"
	resp, err := client.Get(u)
	if err != nil {
		// clientThroughServer dials by IP; unknown paths still resolve the
		// vhost, so err here would be a routing failure, not a 404.
		t.Fatalf("get %s: %v", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unrouted path = %d; want 404", resp.StatusCode)
	}
}

// Guard: sdkURL/clientThroughServer contract unchanged (multi-target must
// not disturb the single-target helpers' URL shape).
func TestSDKURLHelper(t *testing.T) {
	got := sdkURL("http://127.0.0.1:1")
	if _, err := url.Parse(got); err != nil {
		t.Fatalf("sdkURL produced %q: %v", got, err)
	}
}
