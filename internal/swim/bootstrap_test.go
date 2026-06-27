package swim_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/teleivo/assertive/assert"
	"github.com/teleivo/assertive/require"
	"github.com/teleivo/commute/internal/swim"
)

// TestBootstrapStartsWithNoSeeds verifies that New succeeds and the node runs
// as a cluster of one when the seed list is empty.
func TestBootstrapStartsWithNoSeeds(t *testing.T) {
	t.Skip()
	synctest.Test(t, func(t *testing.T) {
		network := newNetwork(t, []string{"machine-0"})
		m, err := swim.New(swim.Config{
			NodeID:         "node-0",
			AdvertiseHost:  "machine-0",
			Conn:           network.conn(0),
			Listener:       newFakeListener(),
			Seeds:          "",
			Resolve:        network.resolve,
			ProtocolPeriod: 1 * time.Second,
			AckTimeout:     500 * time.Millisecond,
			SubgroupSize:   1,
			HTTPClient:     &http.Client{Transport: &nodeTransport{network: network, hostAddr: network.conns[0].hostAddr}},
		})
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		go func() {
			if err := m.Start(ctx); err != nil {
				t.Errorf("Start: %v", err)
			}
		}()

		time.Sleep(3 * time.Second)
		synctest.Wait()

		members := joinMembers(t, m, "node-99:7946")

		assert.True(t, slices.Contains(members, "node-99:7946"), "node-99 should be registered as a peer")
		assert.True(t, slices.Contains(members, m.UDPAddr()), "node-0 should include itself in the response")
	})
}

// TestBootstrapSeedUnresolvableAtStartup verifies that New succeeds even when
// seeds cannot be resolved, and that unresolvable seeds are never added as peers.
func TestBootstrapSeedUnresolvableAtStartup(t *testing.T) {
	t.Skip()
	synctest.Test(t, func(t *testing.T) {
		// network has 1 node. node-1 is not registered so resolve returns an error.
		network := newNetwork(t, []string{"machine-0"})
		m, err := swim.New(swim.Config{
			NodeID:         "node-0",
			AdvertiseHost:  "machine-0",
			Conn:           network.conn(0),
			Listener:       newFakeListener(),
			Seeds:          "node-1:8080",
			Resolve:        network.resolve,
			ProtocolPeriod: 1 * time.Second,
			AckTimeout:     500 * time.Millisecond,
			SubgroupSize:   1,
			HTTPClient:     &http.Client{Transport: &nodeTransport{network: network, hostAddr: network.conns[0].hostAddr}},
		})
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		go func() {
			if err := m.Start(ctx); err != nil {
				t.Errorf("Start: %v", err)
			}
		}()

		time.Sleep(3 * time.Second)
		synctest.Wait()

		members := joinMembers(t, m, "node-99:7946")

		assert.True(t, slices.Contains(members, "node-99:7946"), "node-99 should be registered as a peer")
		assert.True(t, slices.Contains(members, m.UDPAddr()), "node-0 should include itself in the response")
	})
}

// TestBootstrapJoinsWhenSeedBecomesResolvable verifies that the bootstrap loop
// retries and adds a peer once its seed becomes reachable.
func TestBootstrapJoinsWhenSeedBecomesResolvable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		network := newNetwork(t, []string{"machine-0", "machine-1"})

		m0, err := swim.New(swim.Config{
			NodeID:         "node-0",
			AdvertiseHost:  "machine-0",
			Conn:           network.conn(0),
			Listener:       newFakeListener(),
			Seeds:          "node-1:8080",
			Resolve:        network.resolve,
			ProtocolPeriod: 1 * time.Second,
			AckTimeout:     500 * time.Millisecond,
			SubgroupSize:   1,
			HTTPClient:     &http.Client{Transport: &nodeTransport{network: network, hostAddr: network.conns[0].hostAddr}},
		})
		require.NoError(t, err)

		m1, err := swim.New(swim.Config{
			NodeID:         "node-1",
			AdvertiseHost:  "machine-1",
			Conn:           network.conn(1),
			Listener:       newFakeListener(),
			Seeds:          "",
			Resolve:        network.resolve,
			ProtocolPeriod: 1 * time.Second,
			AckTimeout:     500 * time.Millisecond,
			SubgroupSize:   1,
			HTTPClient:     &http.Client{Transport: &nodeTransport{network: network, hostAddr: network.conns[1].hostAddr}},
		})
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		go func() {
			if err := m0.Start(ctx); err != nil {
				t.Errorf("m0 Start: %v", err)
			}
		}()
		go func() {
			if err := m1.Start(ctx); err != nil {
				t.Errorf("m1 Start: %v", err)
			}
		}()

		// Bootstrap fires but node-1 is not yet registered. node-0 has no bootstrap peers yet.
		time.Sleep(1 * time.Second)
		synctest.Wait()

		// node-1's join handler becomes reachable.
		network.register("node-1:8080", m1, network.conns[1].hostAddr)

		// Bootstrap loop retries and join succeeds. First retry is after 5s so wait 6s.
		time.Sleep(6 * time.Second)
		synctest.Wait()

		members := joinMembers(t, m0, "node-99:7946")

		assert.True(t, slices.Contains(members, m1.UDPAddr()), "node-0 should have node-1 as peer after it becomes reachable")
	})
}

// TestBootstrapJoinPushPull verifies the symmetric push/pull exchange on join:
// the joiner pushes its peer list to the seed, and the seed returns its own
// list (pull). node-0 seeds only node-1; node-1 has no seeds so the only way
// it learns node-0 is via the push in node-0's join request. node-0 discovers
// node-2 via the pull response from node-1 (which seeds node-2 directly).
func TestBootstrapJoinPushPull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		network := newNetwork(t, []string{"machine-0", "machine-1", "machine-2"})

		m0, err := swim.New(swim.Config{
			NodeID:         "node-0",
			AdvertiseHost:  "machine-0",
			Conn:           network.conn(0),
			Listener:       newFakeListener(),
			Seeds:          "node-1:8080",
			Resolve:        network.resolve,
			ProtocolPeriod: 1 * time.Second,
			AckTimeout:     500 * time.Millisecond,
			SubgroupSize:   1,
			HTTPClient:     &http.Client{Transport: &nodeTransport{network: network, hostAddr: network.conns[0].hostAddr}},
		})
		require.NoError(t, err)

		// node-1 has no seeds: it can only learn node-0 via push from node-0's join request.
		m1, err := swim.New(swim.Config{
			NodeID:         "node-1",
			AdvertiseHost:  "machine-1",
			Conn:           network.conn(1),
			Listener:       newFakeListener(),
			Seeds:          "",
			Resolve:        network.resolve,
			ProtocolPeriod: 1 * time.Second,
			AckTimeout:     500 * time.Millisecond,
			SubgroupSize:   1,
			HTTPClient:     &http.Client{Transport: &nodeTransport{network: network, hostAddr: network.conns[1].hostAddr}},
		})
		require.NoError(t, err)

		m2, err := swim.New(swim.Config{
			NodeID:         "node-2",
			AdvertiseHost:  "machine-2",
			Conn:           network.conn(2),
			Listener:       newFakeListener(),
			Seeds:          "node-1:8080",
			Resolve:        network.resolve,
			ProtocolPeriod: 1 * time.Second,
			AckTimeout:     500 * time.Millisecond,
			SubgroupSize:   1,
			HTTPClient:     &http.Client{Transport: &nodeTransport{network: network, hostAddr: network.conns[2].hostAddr}},
		})
		require.NoError(t, err)

		network.register("node-1:8080", m1, network.conns[1].hostAddr)
		network.register("node-2:8080", m2, network.conns[2].hostAddr)

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		go func() {
			if err := m0.Start(ctx); err != nil {
				t.Errorf("m0 Start: %v", err)
			}
		}()
		go func() {
			if err := m1.Start(ctx); err != nil {
				t.Errorf("m1 Start: %v", err)
			}
		}()
		go func() {
			if err := m2.Start(ctx); err != nil {
				t.Errorf("m2 Start: %v", err)
			}
		}()

		// Wait for the first bootstrap attempt (immediate) plus the first retry.
		// The retry fires after 5s + jitter, where jitter < retry/6 ~= 833ms.
		// 7s gives enough headroom for the retry to have completed.
		time.Sleep(7 * time.Second)
		synctest.Wait()

		members0 := joinMembers(t, m0, "node-99:7946")

		assert.True(t, slices.Contains(members0, m1.UDPAddr()), "node-0 should have node-1 as peer")
		assert.True(t, slices.Contains(members0, m2.UDPAddr()), "node-0 should discover node-2 via pull from node-1")

		// node-1 has no seeds; it learns node-0 only via push from node-0's join request.
		members1 := joinMembers(t, m1, "node-99:7946")

		assert.True(t, slices.Contains(members1, m0.UDPAddr()), "node-1 should learn node-0 via push")
		assert.True(t, slices.Contains(members1, m2.UDPAddr()), "node-1 should learn node-2 via push from node-2's join request")
		assert.True(t, slices.Contains(members1, "node-99:7946"), "node-99 should be registered as a peer")

		members2 := joinMembers(t, m2, "node-99:7946")

		assert.True(t, slices.Contains(members2, m1.UDPAddr()), "node-2 should have node-1 as peer")
		assert.True(t, slices.Contains(members2, "node-99:7946"), "node-99 should be registered as a peer")
	})
}

// TestBootstrapDeadPeerNotResurrectedByJoin verifies that a peer previously
// declared dead is not re-added to the member list when it appears in a later
// join request pushed by another node.
func TestBootstrapDeadPeerNotResurrectedByJoin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 2 nodes: node-0 probes node-1; node-1 is partitioned so node-0
		// declares it dead. An outsider (node-99) that doesn't know about the
		// death then joins node-0 and pushes node-1 in its peer list.
		// node-1 must not reappear in node-0's member list.
		c := newCluster(t, 2)

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		c.start(ctx)

		// Let bootstrap complete so the cluster is formed before the partition.
		synctest.Wait()
		c.partition(1)

		// Round-robin: node 1 is selected within 2(n-1)=2 periods.
		time.Sleep(c.protocolPeriod * 2)
		synctest.Wait()

		// node 0 must have declared node 1 dead before the join push
		c.assertFinalState(0, 1, swim.Dead)

		// node-99 is an outsider that still thinks node-1 is alive and pushes
		// it to node-0 in a join request.
		members := joinMembers(t, c.members[0], "node-99:7946", c.udpAddr(1))

		assert.True(t, slices.Contains(members, "node-99:7946"), "node-99 should be registered as a peer")
		assert.False(t, slices.Contains(members, c.udpAddr(1)), "dead node-1 must not be resurrected by a join push")
	})
}

// TestBootstrapDeadPeerRejoinsClearsDeadPeers verifies that a peer previously
// declared dead is re-added to the member list when it directly rejoins.
func TestBootstrapDeadPeerRejoinsClearsDeadPeers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 2 nodes: node-0 probes node-1; node-1 is partitioned so node-0
		// declares it dead. node-1 then directly rejoins node-0.
		// node-1 must reappear in node-0's member list.
		c := newCluster(t, 2)

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		c.start(ctx)

		// Let bootstrap complete so the cluster is formed before the partition.
		synctest.Wait()
		c.partition(1)

		// Wait for node-0 to declare node-1 dead.
		time.Sleep(c.protocolPeriod * 2)
		synctest.Wait()

		// node 0 must have declared node 1 dead before the rejoin
		c.assertFinalState(0, 1, swim.Dead)

		// node-1 directly rejoins node-0 with itself as Src.
		members := joinMembers(t, c.members[0], c.udpAddr(1))

		assert.True(t, slices.Contains(members, c.udpAddr(1)), "rejoining node-1 must be re-added to node-0's member list")
	})
}

// joinMembers calls m.JoinHandler directly with src as the caller and peers as
// the request body, and returns the member UDP addresses from the response.
// src is included in Peers, mirroring how the real bootstrap loop includes the
// joining node's own address alongside its known peers.
func joinMembers(t *testing.T, m *swim.Member, src string, peers ...string) []string {
	t.Helper()
	type joinPeer struct {
		UDPAddr  string `json:"udpAddr"`
		HTTPPort uint16 `json:"httpPort"`
	}
	all := append([]string{src}, peers...)
	jps := make([]joinPeer, len(all))
	for i, p := range all {
		jps[i] = joinPeer{UDPAddr: p}
	}
	body, err := json.Marshal(struct {
		Src   string     `json:"src"`
		Peers []joinPeer `json:"peers"`
	}{Src: src, Peers: jps})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/internal/swim/join", bytes.NewReader(body))
	m.JoinHandler(w, r)
	require.EqualValues(t, 200, w.Code)
	var resp struct {
		Peers []joinPeer `json:"peers"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	addrs := make([]string, len(resp.Peers))
	for i, p := range resp.Peers {
		addrs[i] = p.UDPAddr
	}
	return addrs
}
