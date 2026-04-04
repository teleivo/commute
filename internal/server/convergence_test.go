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

func newCluster(t *testing.T, n int, clocks ...func() time.Time) *cluster {
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
		cfg := server.Config{
			NodeID:         fmt.Sprintf("node-%d", i),
			Peers:          strings.Join(peers, ","),
			GossipInterval: 1 * time.Second,
			Client:         client,
			Rng:            rand.New(rand.NewPCG(uint64(i), 0)),
			Stderr:         io.Discard,
		}
		if i < len(clocks) {
			cfg.Clock = clocks[i]
		}
		srv, err := server.New(cfg)
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

func (c *cluster) decrement(node int, key string, value uint64) {
	c.t.Helper()
	body := fmt.Sprintf(`{"decrement": %d}`, value)
	req := httptest.NewRequest(http.MethodPost, "/types/counters/keys/"+key, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.nodes[node].ServeHTTP(rec, req)
	assert.EqualValues(c.t, rec.Code, http.StatusOK)
}

func (c *cluster) getValue(node int, key string) int64 {
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
	return int64(resp["value"].(float64))
}

func (c *cluster) setRegister(node int, key, value string) {
	c.t.Helper()
	body := fmt.Sprintf(`{"value": %q}`, value)
	req := httptest.NewRequest(http.MethodPut, "/types/registers/keys/"+key, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.nodes[node].ServeHTTP(rec, req)
	assert.EqualValues(c.t, rec.Code, http.StatusOK)
}

func (c *cluster) getRegister(node int, key string) string {
	c.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/types/registers/keys/"+key, nil)
	rec := httptest.NewRecorder()
	c.nodes[node].ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		return ""
	}
	require.EqualValues(c.t, rec.Code, http.StatusOK)
	var resp map[string]any
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(c.t, err)
	return resp["value"].(string)
}

func TestCounterConvergence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t, 3)
		c.startGossip(t.Context())

		c.increment(0, "visitors", 5)

		// Before gossip, only node 0 has the value.
		assert.EqualValues(t, c.getValue(0, "visitors"), int64(5))
		assert.EqualValues(t, c.getValue(1, "visitors"), int64(0))
		assert.EqualValues(t, c.getValue(2, "visitors"), int64(0))

		// Each node picks one random peer per tick, so it may take up to
		// n-1 rounds for all 3 nodes to converge.
		time.Sleep(3 * time.Second)
		synctest.Wait()

		// All nodes should now agree.
		assert.EqualValues(t, c.getValue(0, "visitors"), int64(5))
		assert.EqualValues(t, c.getValue(1, "visitors"), int64(5))
		assert.EqualValues(t, c.getValue(2, "visitors"), int64(5))

		// Increment again on node 0 to verify gossip is continuous.
		c.increment(0, "visitors", 3)

		time.Sleep(3 * time.Second)
		synctest.Wait()

		assert.EqualValues(t, c.getValue(0, "visitors"), int64(8))
		assert.EqualValues(t, c.getValue(1, "visitors"), int64(8))
		assert.EqualValues(t, c.getValue(2, "visitors"), int64(8))

		// Extra rounds should not change values (merge is idempotent).
		time.Sleep(3 * time.Second)
		synctest.Wait()

		assert.EqualValues(t, c.getValue(0, "visitors"), int64(8))
		assert.EqualValues(t, c.getValue(1, "visitors"), int64(8))
		assert.EqualValues(t, c.getValue(2, "visitors"), int64(8))
	})
}

func TestCounterConvergenceMultipleNodes(t *testing.T) {
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
		assert.EqualValues(t, c.getValue(0, "visitors"), int64(12))
		assert.EqualValues(t, c.getValue(1, "visitors"), int64(12))
		assert.EqualValues(t, c.getValue(2, "visitors"), int64(12))

		// Keys from other nodes should have propagated.
		assert.EqualValues(t, c.getValue(0, "likes"), int64(10))
		assert.EqualValues(t, c.getValue(1, "likes"), int64(10))
		assert.EqualValues(t, c.getValue(2, "likes"), int64(10))
		assert.EqualValues(t, c.getValue(0, "shares"), int64(4))
		assert.EqualValues(t, c.getValue(1, "shares"), int64(4))
		assert.EqualValues(t, c.getValue(2, "shares"), int64(4))
	})
}

func TestCounterConvergenceIncrementReceivedKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t, 3)
		c.startGossip(t.Context())

		// Node 0 increments a key.
		c.increment(0, "visitors", 5)

		time.Sleep(3 * time.Second)
		synctest.Wait()

		// Node 1 received the key via gossip and now increments it.
		// This tests that a counter received via gossip retains the
		// correct local nodeID so that the increment lands in node-1's
		// slot, not an empty one.
		c.increment(1, "visitors", 3)

		time.Sleep(3 * time.Second)
		synctest.Wait()

		assert.EqualValues(t, c.getValue(0, "visitors"), int64(8))
		assert.EqualValues(t, c.getValue(1, "visitors"), int64(8))
		assert.EqualValues(t, c.getValue(2, "visitors"), int64(8))

		// Node 0 also increments, testing a third round of gossip.
		c.increment(0, "visitors", 2)

		time.Sleep(3 * time.Second)
		synctest.Wait()

		assert.EqualValues(t, c.getValue(0, "visitors"), int64(10))
		assert.EqualValues(t, c.getValue(1, "visitors"), int64(10))
		assert.EqualValues(t, c.getValue(2, "visitors"), int64(10))

		// Both node-1 and node-2 received "likes" via gossip from
		// node-0. If their counters have an empty nodeID, both will
		// write to counters[""] and merge will take max instead of
		// sum, losing one node's increment.
		c.increment(0, "likes", 10)

		time.Sleep(3 * time.Second)
		synctest.Wait()

		c.increment(1, "likes", 3)
		c.increment(2, "likes", 7)

		time.Sleep(3 * time.Second)
		synctest.Wait()

		// Should be 10+3+7=20, not 10+max(3,7)=17.
		assert.EqualValues(t, c.getValue(0, "likes"), int64(20))
		assert.EqualValues(t, c.getValue(1, "likes"), int64(20))
		assert.EqualValues(t, c.getValue(2, "likes"), int64(20))
	})
}

func TestCounterConvergenceWithDecrement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t, 3)
		c.startGossip(t.Context())

		// Increment and decrement on different nodes.
		c.increment(0, "score", 10)
		c.decrement(1, "score", 3)
		c.increment(2, "score", 5)

		time.Sleep(3 * time.Second)
		synctest.Wait()

		// All nodes should converge to 10-3+5=12.
		assert.EqualValues(t, c.getValue(0, "score"), int64(12))
		assert.EqualValues(t, c.getValue(1, "score"), int64(12))
		assert.EqualValues(t, c.getValue(2, "score"), int64(12))

		// Decrement below zero.
		c.decrement(0, "score", 20)

		time.Sleep(3 * time.Second)
		synctest.Wait()

		// All nodes should converge to 12-20=-8.
		assert.EqualValues(t, c.getValue(0, "score"), int64(-8))
		assert.EqualValues(t, c.getValue(1, "score"), int64(-8))
		assert.EqualValues(t, c.getValue(2, "score"), int64(-8))
	})
}

func TestRegisterConvergence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t, 3)
		c.startGossip(t.Context())

		// Set on node 0, verify it propagates.
		c.setRegister(0, "config", "v1")

		time.Sleep(3 * time.Second)
		synctest.Wait()

		assert.EqualValues(t, c.getRegister(0, "config"), "v1")
		assert.EqualValues(t, c.getRegister(1, "config"), "v1")
		assert.EqualValues(t, c.getRegister(2, "config"), "v1")

		// Overwrite on node 2, verify it propagates.
		c.setRegister(2, "config", "v2")

		time.Sleep(3 * time.Second)
		synctest.Wait()

		assert.EqualValues(t, c.getRegister(0, "config"), "v2")
		assert.EqualValues(t, c.getRegister(1, "config"), "v2")
		assert.EqualValues(t, c.getRegister(2, "config"), "v2")
	})
}

func TestRegisterConvergenceLastWriterWins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Each node gets a clock the test controls.
		baseTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		clocks := make([]func() time.Time, 3)
		timestamps := make([]time.Time, 3)
		for i := range 3 {
			timestamps[i] = baseTime
			i := i
			clocks[i] = func() time.Time { return timestamps[i] }
		}

		c := newCluster(t, 3, clocks...)
		c.startGossip(t.Context())

		// Both nodes write concurrently. Node 1 writes at a later
		// timestamp, so its value should win.
		timestamps[0] = baseTime.Add(1 * time.Millisecond)
		c.setRegister(0, "leader", "node-0")

		timestamps[1] = baseTime.Add(2 * time.Millisecond)
		c.setRegister(1, "leader", "node-1")

		time.Sleep(3 * time.Second)
		synctest.Wait()

		// Node 1's write had the higher timestamp, so it wins.
		assert.EqualValues(t, c.getRegister(0, "leader"), "node-1")
		assert.EqualValues(t, c.getRegister(1, "leader"), "node-1")
		assert.EqualValues(t, c.getRegister(2, "leader"), "node-1")
	})
}

func TestRegisterConvergenceTiebreakByNodeID(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// All nodes write at the exact same timestamp. The node with
		// the highest ID should win as a tiebreaker.
		ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		clock := func() time.Time { return ts }

		c := newCluster(t, 3, clock, clock, clock)
		c.startGossip(t.Context())

		c.setRegister(0, "leader", "node-0")
		c.setRegister(1, "leader", "node-1")
		c.setRegister(2, "leader", "node-2")

		time.Sleep(3 * time.Second)
		synctest.Wait()

		// node-2 has the highest node ID, so it wins the tiebreak.
		assert.EqualValues(t, c.getRegister(0, "leader"), "node-2")
		assert.EqualValues(t, c.getRegister(1, "leader"), "node-2")
		assert.EqualValues(t, c.getRegister(2, "leader"), "node-2")
	})
}
