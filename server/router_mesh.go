package server

// Mesh router (cluster mode B): instances discover each other via memberlist
// gossip and forward visitor requests to the peer that owns the subdomain.
//
// Enable with ETUNNEL_CLUSTER=mesh. Required env:
//
//	ETUNNEL_ADVERTISE  host:port other peers can reach us on (internal)
//	ETUNNEL_JOIN       comma-separated seed peer addresses (empty = first node)
//
// The memberlist protocol (SWIM) handles failure detection and retransmit;
// subdomain ownership is gossiped as user events, which memberlist delivers
// unreliably-by-default - we register a compound listener and, on a Claim
// for an unknown state, pull a full state snapshot from the sender, so a
// missed event self-heals.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/memberlist"
)

// meshMaxOwnerEntries bounds the owner map (#186): gossip events are
// application-level data, so even an authenticated cluster member could flood
// claims; a runaway owner map must not grow without limit.
const meshMaxOwnerEntries = 1 << 16

type meshRouter struct {
	self   string // our advertise address
	ml     *memberlist.Memberlist
	hc     *http.Client // for Forward
	fwdHdr string       // header marking an already-forwarded request
	// stopCh releases the claim-broadcast goroutine in Close; stopOnce
	// guards it against a double Close panic (agent teardown can race the
	// embedder's explicit Close).
	stopCh   chan struct{}
	stopOnce sync.Once

	mu    sync.RWMutex
	owner map[string]string // subdomain -> advertise addr ("" = us)
	ours  map[string]bool   // subdomains we hold

	// peerIPs caches resolved advertise hosts for knownPeer (#186): every
	// gossip event's From must map to an address the memberlist actually
	// knows before it may touch the owner map.
	peerMu        sync.Mutex
	peerIPs       map[string][]net.IP
	peerIPsTTL    map[string]time.Time
	peerResolving map[string]bool // hosts with an in-flight lookup (#219)
}

// meshEvent is the gossip payload.
type meshEvent struct {
	Kind string   `json:"k"` // "claim" | "release" | "snapshot"
	From string   `json:"f"` // advertise addr of the sender
	Subs []string `json:"s"` // affected subdomains (snapshot = everything)
}

func newMeshRouter(advertise, join, nodeID string) (*meshRouter, error) {
	r := &meshRouter{
		self:       advertise,
		hc:         &http.Client{Timeout: 30 * time.Second},
		fwdHdr:     "X-Etunnel-Forward",
		owner:      map[string]string{},
		ours:       map[string]bool{},
		peerIPs:    map[string][]net.IP{},
		peerIPsTTL: map[string]time.Time{},
	}
	// #186: mesh gossip decides where visitor traffic (cookies and
	// Authorization headers included) is forwarded, so it is fail-closed on
	// a shared secret - without it any reachable node can impersonate an
	// owner and hijack tunnels. The secret is hashed to 32 bytes because
	// memberlist keyrings only accept AES key sizes.
	secret := os.Getenv("ETUNNEL_MESH_SECRET")
	if secret == "" {
		return nil, errors.New("etunnel: ETUNNEL_CLUSTER=mesh requires ETUNNEL_MESH_SECRET (shared cluster key; gossip would be unauthenticated without it)")
	}
	key := sha256.Sum256([]byte(secret))
	kr, kerr := memberlist.NewKeyring([][]byte{key[:]}, key[:])
	if kerr != nil {
		return nil, fmt.Errorf("etunnel: mesh keyring: %w", kerr)
	}
	cfg := memberlist.DefaultLANConfig()
	cfg.Keyring = kr
	cfg.Name = nodeID
	_, port, _ := net.SplitHostPort(advertise)
	var p int
	fmt.Sscanf(port, "%d", &p)
	// memberlist needs an IP for AdvertiseAddr; resolve the hostname part.
	advIP := hostOf(advertise)
	if ips, err := net.LookupIP(advIP); err == nil && len(ips) > 0 {
		advIP = ips[0].String()
	}
	cfg.AdvertiseAddr = advIP
	cfg.AdvertisePort = p
	// Gossip transport lives on its own port: the advertise port carries
	// HTTP (tunnels/forwarding), so gossip uses ETUNNEL_GOSSIP_PORT or
	// advertise+100. Peers learn it from the join address / node meta.
	gossipPort := p + 100
	if gossipPort > 65535 {
		// #237: an advertise port in 65436..65535 is legal but leaves no room
		// for gossip (+100); fail with the reason instead of memberlist's
		// opaque "bind: invalid argument".
		return nil, fmt.Errorf("etunnel: advertise port %d leaves no room for the gossip port (+100 > 65535); set ETUNNEL_GOSSIP_PORT", p)
	}
	if v := os.Getenv("ETUNNEL_GOSSIP_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &gossipPort)
	}
	cfg.BindPort = gossipPort
	cfg.AdvertisePort = gossipPort // memberlist advertises its own port
	cfg.BindAddr = "0.0.0.0"
	cfg.PushPullInterval = 15 * time.Second
	cfg.Delegate = &meshDelegate{r: r}
	ml, err := memberlist.Create(cfg)
	if err != nil {
		return nil, fmt.Errorf("etunnel: memberlist create: %w", err)
	}
	r.ml = ml
	// Re-broadcast our ownership every 10s: SendReliable is best-effort and
	// NodeMeta rides periodic probes, so a periodic refresh guarantees
	// convergence without waiting for push/pull.
	r.stopCh = make(chan struct{})
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if subs := r.ourSubs(); len(subs) > 0 {
					r.broadcast(meshEvent{Kind: "claim", From: r.self, Subs: subs})
				}
			case <-r.stopCh:
				return
			}
		}
	}()
	if join != "" {
		seeds := strings.Split(join, ",")
		// Retry the join: peers start concurrently and DNS may lag.
		var joined int
		for i := 0; i < 10 && joined == 0; i++ {
			joined, _ = ml.Join(seeds)
			if joined == 0 {
				time.Sleep(2 * time.Second)
			}
		}
		if joined == 0 {
			// Start anyway as a lone node; peers will find us via the
			// probe/anti-entropy path once they come up. Release the
			// claim-refresh goroutine first (#191): it started above and
			// would otherwise spin forever against the shut-down memberlist.
			r.stopOnce.Do(func() { close(r.stopCh) })
			ml.Shutdown()
			return nil, fmt.Errorf("etunnel: memberlist join %v: no peer reachable", seeds)
		}
	}
	return r, nil
}

// meshDelegate implements memberlist.Delegate: state rides the gossip
// payload (NodeMeta), claims/releases ride direct reliable messages.
type meshDelegate struct{ r *meshRouter }

// NodeMeta returns our ownership snapshot (piggybacked on every probe).
func (d *meshDelegate) NodeMeta(limit int) []byte {
	subs := d.r.ourSubs()
	if len(subs) == 0 {
		return nil
	}
	b, _ := json.Marshal(meshEvent{Kind: "snapshot", From: d.r.self, Subs: subs})
	if len(b) > limit {
		return nil // too many subs for meta; peers pull via merge
	}
	return b
}

func (d *meshDelegate) NotifyMsg(b []byte) {
	if len(b) == 0 {
		return
	}
	d.r.handleMsg(b)
}

func (d *meshDelegate) GetBroadcasts(overhead, limit int) [][]byte { return nil }

func (d *meshDelegate) LocalState(join bool) []byte {
	subs := d.r.ourSubs()
	b, _ := json.Marshal(meshEvent{Kind: "snapshot", From: d.r.self, Subs: subs})
	return b
}

func (d *meshDelegate) MergeRemoteState(buf []byte, join bool) {
	d.r.handleMsg(buf)
}

// gossip message dispatch: memberlist.SendReliable to a specific node.
func (r *meshRouter) broadcast(ev meshEvent) {
	b, _ := json.Marshal(ev)
	for _, n := range r.ml.Members() {
		if n.Name == r.ml.LocalNode().Name {
			continue
		}
		_ = r.ml.SendReliable(n, b)
	}
}

// handleMsg is called by the delegate channel for every received message.
func (r *meshRouter) handleMsg(b []byte) {
	var ev meshEvent
	if json.Unmarshal(b, &ev) != nil {
		return
	}
	// #186: an event may only rewrite ownership if its From address maps to
	// a member this memberlist actually knows; drops spoofed senders before
	// they can influence where visitor traffic is forwarded.
	if !r.knownPeer(ev.From) {
		logfMesh("dropping %s event from unknown peer %q", ev.Kind, ev.From)
		return
	}
	switch ev.Kind {
	case "claim":
		r.mu.Lock()
		for _, s := range ev.Subs {
			if !r.ours[s] { // we do not own it: record the peer
				if len(r.owner) >= meshMaxOwnerEntries {
					logfMesh("owner map at capacity (%d); dropping claim for %s from %s", meshMaxOwnerEntries, s, ev.From)
					break
				}
				r.owner[s] = ev.From
			}
			// If we DO own it, this is a genuine conflict (both instances
			// handed the same random subdomain). Random names make this
			// practically impossible; custom subdomains are claimed in
			// Postgres. Keep ours and let the visitor path decide.
		}
		r.mu.Unlock()
	case "release":
		r.mu.Lock()
		for _, s := range ev.Subs {
			// Trust the release regardless of the recorded value: the map
			// may hold a rewritten address (tests) or a stale entry.
			if !r.ours[s] {
				delete(r.owner, s)
			}
		}
		r.mu.Unlock()
	case "snapshot":
		r.mu.Lock()
		for _, s := range ev.Subs {
			if !r.ours[s] {
				if len(r.owner) >= meshMaxOwnerEntries {
					logfMesh("owner map at capacity (%d); dropping snapshot claim for %s from %s", meshMaxOwnerEntries, s, ev.From)
					break
				}
				r.owner[s] = ev.From
			}
		}
		r.mu.Unlock()
	}
}

// Lookup returns the owner's advertise address, or "" when we own it / unknown.
func (r *meshRouter) Lookup(sub string) string {
	if r.owns(sub) {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.owner[sub]
}

func (r *meshRouter) owns(sub string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ours[sub]
}

// Claim marks the subdomain ours and tells the peers. Gossip delivery is
// retried by memberlist and a 10s rebroadcast backs it up (#191), so there
// is no synchronous failure to report.
func (r *meshRouter) Claim(sub string) error {
	r.mu.Lock()
	r.ours[sub] = true
	delete(r.owner, sub)
	r.mu.Unlock()
	r.broadcast(meshEvent{Kind: "claim", From: r.self, Subs: []string{sub}})
	return nil
}

// Release drops ownership and tells the peers.
func (r *meshRouter) Release(sub string) {
	r.mu.Lock()
	delete(r.ours, sub)
	delete(r.owner, sub)
	r.mu.Unlock()
	r.broadcast(meshEvent{Kind: "release", From: r.self, Subs: []string{sub}})
}

// Forward proxies the request to the peer owning sub. Returns false when the
// request is not forwardable (we own it, nobody does, or it already bounced).
func (r *meshRouter) Forward(sub string, w http.ResponseWriter, req *http.Request) bool {
	if req.Header.Get(r.fwdHdr) != "" {
		return false // forwarding loop guard
	}
	target := r.Lookup(sub)
	if target == "" {
		logfMesh("forward %s: no owner known", sub)
		return false
	}
	if hasUpgrade(req.Header) {
		// #235: upgrades need the raw hijacked pipe; http.Client turns a
		// 101 into a one-way stream and corrupts the exchange.
		return forwardUpgraded(target, w, req, r.fwdHdr, r.self)
	}
	url := *req.URL
	url.Scheme = "http"
	url.Host = target
	out, err := http.NewRequestWithContext(req.Context(), req.Method, url.String(), req.Body)
	if err != nil {
		logfMesh("forward %s: build: %v", sub, err)
		return false
	}
	out.Host = req.Host // keep the visitor's Host; node1:8080 is just transport
	out.Header = req.Header.Clone()
	out.Header.Set(r.fwdHdr, r.self)
	resp, err := r.hc.Do(out)
	if err != nil {
		logfMesh("forward %s -> %s: %v", sub, target, err)
		return false
	}
	defer resp.Body.Close()
	// #235: never re-send hop-by-hop headers verbatim.
	tokens := connectionTokensFromMap(resp.Header)
	for k, vs := range resp.Header {
		if isHopHeader(k) || tokens[strings.ToLower(k)] {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return true
}

// Snapshot returns everything we own (for a newly joined peer's pull).
func (r *meshRouter) ourSubs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.ours))
	for s := range r.ours {
		out = append(out, s)
	}
	return out
}

// OnPeerJoin sends our full snapshot to a new peer (called by the delegate).
func (r *meshRouter) sendSnapshotTo(name string) {
	subs := r.ourSubs()
	if len(subs) == 0 {
		return
	}
	b, _ := json.Marshal(meshEvent{Kind: "snapshot", From: r.self, Subs: subs})
	for _, n := range r.ml.Members() {
		if n.Name == name {
			_ = r.ml.SendReliable(n, b)
			return
		}
	}
}

// Close releases everything and leaves the cluster.
func (r *meshRouter) Close() error {
	r.stopOnce.Do(func() { close(r.stopCh) })
	subs := r.ourSubs()
	if len(subs) > 0 {
		r.broadcast(meshEvent{Kind: "release", From: r.self, Subs: subs})
	}
	time.Sleep(100 * time.Millisecond) // best-effort flush
	return r.ml.Shutdown()
}

// msgBuffer is the channel the memberlist delegate feeds; started by Server.
func (r *meshRouter) startPump(ctx context.Context, ch <-chan []byte) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-ch:
				r.handleMsg(b)
			}
		}
	}()
}

var _ = bytes.MinRead

// hostOf returns the host part of host:port.
func hostOf(hostPort string) string {
	h, _, _ := net.SplitHostPort(hostPort)
	return h
}

// knownPeer reports whether a gossip event's From address belongs to a node
// resolvePeer returns the cached IPs for a peer host, resolving when stale.
//
// #219: resolution happens OUTSIDE peerMu. This runs on memberlist's delegate
// goroutines (NotifyMsg / MergeRemoteState), and net.LookupIP has no deadline -
// holding the lock through it froze every concurrent gossip message for the
// duration of a slow lookup, stalling member probes and push/pull. The
// single-flight map keeps at most one in-flight lookup per host, so concurrent
// callers for the same host share the result instead of piling up.
func (r *meshRouter) resolvePeer(host string) []net.IP {
	r.peerMu.Lock()
	if ips, ok := r.peerIPs[host]; ok && time.Since(r.peerIPsTTL[host]) <= 30*time.Second {
		r.peerMu.Unlock()
		return ips
	}
	if _, busy := r.peerResolving[host]; busy {
		r.peerMu.Unlock()
		// Another goroutine is resolving; treat as unknown this round rather
		// than blocking the gossip goroutine. The next message after the
		// in-flight lookup completes gets the cached answer.
		return nil
	}
	if r.peerResolving == nil {
		r.peerResolving = map[string]bool{}
	}
	r.peerResolving[host] = true
	r.peerMu.Unlock()

	ips, rerr := net.LookupIP(host)
	if rerr != nil {
		ips = nil // resolution failure: treat as unknown peer
	}

	r.peerMu.Lock()
	delete(r.peerResolving, host)
	r.peerIPs[host] = ips
	r.peerIPsTTL[host] = time.Now()
	r.peerMu.Unlock()
	return ips
}

// the memberlist actually knows (#186). The shared keyring already keeps
// outsiders out; this is defense-in-depth against a compromised member
// claiming ownership for an address it does not run (which would redirect
// visitor traffic to it). Host resolution is cached for 30s per host so the
// gossip hot path stays free of repeated DNS lookups.
func (r *meshRouter) knownPeer(from string) bool {
	if from == "" || from == r.self {
		return false
	}
	host := hostOf(from)
	if host == "" {
		return false
	}
	ips := r.resolvePeer(host)
	if len(ips) == 0 {
		return false
	}
	if len(ips) == 0 {
		return false
	}
	for _, n := range r.ml.Members() {
		for _, ip := range ips {
			if n.Addr != nil && ip.Equal(n.Addr) {
				return true
			}
		}
	}
	return false
}

// logfMesh logs through the standard logger (visible in docker logs).
func logfMesh(format string, args ...any) {
	log.Printf("mesh: "+format, args...)
}
