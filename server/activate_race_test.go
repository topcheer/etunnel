package server

// #17 (slice 3): the activate path writes the shared *Account (tier +
// expiry) while a session handshake or /me read snapshots it from another
// goroutine. The original review listed zero coverage for that
// interleaving (#4 was the bug class); this fence drives the real handler
// and activate concurrently under -race.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestActivateConcurrentWithSnapshot(t *testing.T) {
	s := newBillingTestServer(t)
	if _, err := s.activate("race@x.com", "starter"); err != nil {
		t.Fatal(err)
	}
	a, ok := s.accounts.LookupEmail("race@x.com")
	if !ok {
		t.Fatal("account missing after activate")
	}
	key := a.APIKey

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// 4 writers: renew/upgrade cycles across two tiers (the #4 bug class)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tier := "starter"
			if i%2 == 1 {
				tier = "pro"
			}
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := s.activate("race@x.com", tier); err != nil {
					t.Errorf("activate failed: %v", err)
					return
				}
			}
		}(i)
	}

	// 4 readers: real HTTP snapshot of the account through the API key
	// (what the /me page and the session handshake do between frames).
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
				req.Header.Set("Authorization", "Bearer "+key)
				rec := httptest.NewRecorder()
				s.handleMe(rec, req)
				if rec.Code >= 500 {
					t.Errorf("snapshot 5xx during concurrent activate: %d %s", rec.Code, rec.Body.String())
					return
				}
				if rec.Code == 200 && strings.Count(rec.Body.String(), `"email"`) != 1 {
					t.Errorf("torn JSON snapshot: %.120s", rec.Body.String())
					return
				}
			}
		}()
	}

	// bounded interleave window, then drain
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}
