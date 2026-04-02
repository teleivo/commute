package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/teleivo/assertive/assert"
	"github.com/teleivo/assertive/require"
	"github.com/teleivo/commute/internal/server"
)

// cluster manages a group of nodes for testing. It implements http.RoundTripper
// to route gossip requests to the right node's ServeHTTP in-process, avoiding
// real network I/O.
type cluster struct {
	t      *testing.T
	nodes  []*server.Server
	routes map[string]*server.Server
}

func (c *cluster) RoundTrip(req *http.Request) (*http.Response, error) {
	node, ok := c.routes[req.URL.Host]
	if !ok {
		return nil, fmt.Errorf("no node for host %q", req.URL.Host)
	}
	rec := httptest.NewRecorder()
	node.ServeHTTP(rec, req)
	return rec.Result(), nil
}

func newCluster(t *testing.T, n int) *cluster {
	t.Helper()

	addrs := make([]string, n)
	for i := range n {
		addrs[i] = fmt.Sprintf("127.0.0.1:%d", 10000+i)
	}

	c := &cluster{
		t:      t,
		nodes:  make([]*server.Server, n),
		routes: make(map[string]*server.Server, n),
	}
	client := &http.Client{Transport: c}
	for i := range n {
		peers := make([]string, 0, n-1)
		for j := range n {
			if i != j {
				peers = append(peers, addrs[j])
			}
		}
		srv, err := server.New(server.Config{
			NodeID:         fmt.Sprintf("node-%d", i),
			Port:           "0",
			Peers:          strings.Join(peers, ","),
			GossipInterval: 1 * time.Second,
			Client:         client,
			Rng:            rand.New(rand.NewPCG(uint64(i), 0)),
			Stderr:         io.Discard,
		})
		require.NoError(t, err)
		c.nodes[i] = srv
		c.routes[addrs[i]] = srv
	}

	return c
}

func (c *cluster) startGossip(ctx context.Context) {
	c.t.Helper()
	for _, node := range c.nodes {
		go node.StartGossip(ctx)
	}
}

func (c *cluster) increment(node int, key string, value uint64) {
	c.t.Helper()
	body := fmt.Sprintf(`{"increment": %d}`, value)
	req := httptest.NewRequest(http.MethodPost, "/types/counters/keys/"+key, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.nodes[node].ServeHTTP(rec, req)
	assert.EqualValues(c.t, rec.Code, http.StatusOK)
}

func (c *cluster) getValue(node int, key string) uint64 {
	c.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/types/counters/keys/"+key, nil)
	rec := httptest.NewRecorder()
	c.nodes[node].ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		return 0
	}
	require.EqualValues(c.t, rec.Code, http.StatusOK)
	var resp map[string]any
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(c.t, err)
	return uint64(resp["value"].(float64))
}

func TestConvergence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t, 3)
		c.startGossip(t.Context())

		c.increment(0, "visitors", 5)

		// Before gossip, only node 0 has the value.
		assert.EqualValues(t, c.getValue(0, "visitors"), uint64(5))
		assert.EqualValues(t, c.getValue(1, "visitors"), uint64(0))
		assert.EqualValues(t, c.getValue(2, "visitors"), uint64(0))

		// Each node picks one random peer per tick, so it may take up to
		// n-1 rounds for all 3 nodes to converge.
		time.Sleep(3 * time.Second)
		synctest.Wait()

		// All nodes should now agree.
		assert.EqualValues(t, c.getValue(0, "visitors"), uint64(5))
		assert.EqualValues(t, c.getValue(1, "visitors"), uint64(5))
		assert.EqualValues(t, c.getValue(2, "visitors"), uint64(5))

		// Increment again on node 0 to verify gossip is continuous.
		c.increment(0, "visitors", 3)

		time.Sleep(3 * time.Second)
		synctest.Wait()

		assert.EqualValues(t, c.getValue(0, "visitors"), uint64(8))
		assert.EqualValues(t, c.getValue(1, "visitors"), uint64(8))
		assert.EqualValues(t, c.getValue(2, "visitors"), uint64(8))

		// Extra rounds should not change values (merge is idempotent).
		time.Sleep(3 * time.Second)
		synctest.Wait()

		assert.EqualValues(t, c.getValue(0, "visitors"), uint64(8))
		assert.EqualValues(t, c.getValue(1, "visitors"), uint64(8))
		assert.EqualValues(t, c.getValue(2, "visitors"), uint64(8))
	})
}

func TestConvergenceMultipleNodes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t, 3)
		c.startGossip(t.Context())

		// Increment same key on different nodes.
		c.increment(0, "visitors", 3)
		c.increment(1, "visitors", 7)
		c.increment(2, "visitors", 2)

		// Increment different keys on different nodes.
		c.increment(0, "likes", 10)
		c.increment(2, "shares", 4)

		time.Sleep(3 * time.Second)
		synctest.Wait()

		// All nodes should converge to 3+7+2=12.
		assert.EqualValues(t, c.getValue(0, "visitors"), uint64(12))
		assert.EqualValues(t, c.getValue(1, "visitors"), uint64(12))
		assert.EqualValues(t, c.getValue(2, "visitors"), uint64(12))

		// Keys from other nodes should have propagated.
		assert.EqualValues(t, c.getValue(0, "likes"), uint64(10))
		assert.EqualValues(t, c.getValue(1, "likes"), uint64(10))
		assert.EqualValues(t, c.getValue(2, "likes"), uint64(10))
		assert.EqualValues(t, c.getValue(0, "shares"), uint64(4))
		assert.EqualValues(t, c.getValue(1, "shares"), uint64(4))
		assert.EqualValues(t, c.getValue(2, "shares"), uint64(4))
	})
}
