package server

// Cluster wiring: reads ETUNNEL_CLUSTER / ETUNNEL_PG_DSN / ETUNNEL_REDIS_URL /
// ETUNNEL_ADVERTISE / ETUNNEL_JOIN and assembles the Server's cluster
// components. Everything is optional; the default build is a single node
// with the JSON store, in-memory counters and no router - bit-for-bit the
// behaviour of the pre-cluster binary.

import (
	"context"
	"fmt"
	"net/http"
	"os"
)

// ClusterConfig holds the assembled components.
type ClusterConfig struct {
	Store   *pgAccountStore // nil = keep the JSON store
	Counter Counter         // never nil (mem or redis)
	Router  Router          // nil = single node
}

// setupCluster builds the cluster pieces from the environment. The returned
// closer list lets Run shut everything down with the server context.
func setupCluster(ctx context.Context) (*ClusterConfig, []func(), error) {
	cc := &ClusterConfig{Counter: newMemCounter()}
	var closers []func()

	if dsn := os.Getenv("ETUNNEL_PG_DSN"); dsn != "" {
		st, err := newPgAccountStore(dsn)
		if err != nil {
			for _, c := range closers {
				c()
			}
			return nil, nil, err
		}
		cc.Store = st
		closers = append(closers, func() { st.Close() })
	}
	if url := os.Getenv("ETUNNEL_REDIS_URL"); url != "" {
		rc, err := newRedisCounter(url)
		if err != nil {
			for _, c := range closers {
				c()
			}
			return nil, nil, err
		}
		cc.Counter = rc
		closers = append(closers, func() { rc.Close() })
	}
	switch mode := os.Getenv("ETUNNEL_CLUSTER"); mode {
	case "", "single":
		// no router
	case "mesh":
		adv := os.Getenv("ETUNNEL_ADVERTISE")
		if adv == "" {
			return nil, closers, fmt.Errorf("etunnel: ETUNNEL_CLUSTER=mesh requires ETUNNEL_ADVERTISE=host:port")
		}
		id := os.Getenv("ETUNNEL_NODE_ID")
		if id == "" {
			id = adv
		}
		mr, err := newMeshRouter(adv, os.Getenv("ETUNNEL_JOIN"), id)
		if err != nil {
			for _, c := range closers {
				c()
			}
			return nil, nil, err
		}
		cc.Router = mr
		closers = append(closers, func() { mr.Close() })
	case "sticky":
		url := os.Getenv("ETUNNEL_REDIS_URL")
		adv := os.Getenv("ETUNNEL_ADVERTISE")
		if url == "" || adv == "" {
			return nil, closers, fmt.Errorf("etunnel: ETUNNEL_CLUSTER=sticky requires ETUNNEL_REDIS_URL and ETUNNEL_ADVERTISE")
		}
		sr, err := newStickyRouter(url, adv)
		if err != nil {
			for _, c := range closers {
				c()
			}
			return nil, nil, err
		}
		cc.Router = sr
		closers = append(closers, func() { sr.Close() })
	default:
		return nil, closers, fmt.Errorf("etunnel: unknown ETUNNEL_CLUSTER %q (single|mesh|sticky)", mode)
	}
	return cc, closers, nil
}

// applyCluster installs the pieces onto a Server (called from Run, before
// the listeners start).
func (s *Server) applyCluster(ctx context.Context, cc *ClusterConfig, closers []func()) {
	if cc.Store != nil {
		s.accounts = cc.Store
	}
	s.counter = cc.Counter
	s.router = cc.Router
	go func() {
		<-ctx.Done()
		for _, c := range closers {
			c()
		}
	}()
}

// HealthzHandler serves /healthz on the main listener: 200 when the node is
// accepting tunnels, 503 while draining.
func (s *Server) HealthzHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.draining.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("draining\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
}

// Drain flips the node into draining state (healthz 503, no new tunnels) so
// the LB can remove it before the process stops. Called from Run on ctx
// cancellation (SIGTERM in systemd/k8s) before sessions are closed.
func (s *Server) Drain() {
	s.draining.Store(true)
}
