package server

// Three routers, claim on A must reach B and C via gossip alone.
import (
	"fmt"
	"testing"
	"time"
)

func TestMeshThreeNodeGossip(t *testing.T) {
	a, ap := newTestMesh(t, "a3")
	b, _ := newTestMesh(t, "b3")
	c, _ := newTestMesh(t, "c3")
	b.ml.Join([]string{fmt.Sprintf("127.0.0.1:%d", ap+100)})
	c.ml.Join([]string{fmt.Sprintf("127.0.0.1:%d", ap+100)})
	time.Sleep(500 * time.Millisecond)
	a.Claim("tri-sub")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b.Lookup("tri-sub") == a.self && c.Lookup("tri-sub") == a.self {
			t.Log("gossip reached both peers")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("b=%q c=%q want=%q", b.Lookup("tri-sub"), c.Lookup("tri-sub"), a.self)
}
