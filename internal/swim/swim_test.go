package swim_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/teleivo/assertive/assert"
	"github.com/teleivo/commute/internal/swim"
)

func TestNew(t *testing.T) {
	network := newNetwork(t, []string{"7811223344aabb"})
	validConfig := swim.Config{
		NodeID:         "node-0",
		AdvertiseHost:  "7811223344aabb",
		Conn:           network.conn(0),
		Listener:       newFakeListener(),
		Resolve:        network.resolve,
		ProtocolPeriod: 1 * time.Second,
		AckTimeout:     500 * time.Millisecond,
		SubgroupSize:   3,
	}

	tests := map[string]struct {
		cfg     swim.Config
		wantErr bool
	}{
		"Valid": {
			cfg: validConfig,
		},
		"MissingNodeID": {
			cfg:     func() swim.Config { c := validConfig; c.NodeID = ""; return c }(),
			wantErr: true,
		},
		"MissingAdvertiseHost": {
			cfg:     func() swim.Config { c := validConfig; c.AdvertiseHost = ""; return c }(),
			wantErr: true,
		},
		"MissingConn": {
			cfg:     func() swim.Config { c := validConfig; c.Conn = nil; return c }(),
			wantErr: true,
		},
		"InvalidSeed": {
			cfg:     func() swim.Config { c := validConfig; c.Seeds = "notahost"; return c }(),
			wantErr: true,
		},
		"SeedMissingHost": {
			cfg:     func() swim.Config { c := validConfig; c.Seeds = ":7947"; return c }(),
			wantErr: true,
		},
		"SeedMissingPort": {
			cfg:     func() swim.Config { c := validConfig; c.Seeds = "127.0.0.1"; return c }(),
			wantErr: true,
		},
		"ZeroProtocolPeriod": {
			cfg:     func() swim.Config { c := validConfig; c.ProtocolPeriod = 0; return c }(),
			wantErr: true,
		},
		"ZeroAckTimeout": {
			cfg:     func() swim.Config { c := validConfig; c.AckTimeout = 0; return c }(),
			wantErr: true,
		},
		"AckTimeoutEqualToProtocolPeriod": {
			cfg:     func() swim.Config { c := validConfig; c.AckTimeout = c.ProtocolPeriod; return c }(),
			wantErr: true,
		},
		"AckTimeoutGreaterThanProtocolPeriod": {
			cfg:     func() swim.Config { c := validConfig; c.AckTimeout = c.ProtocolPeriod + 1; return c }(),
			wantErr: true,
		},
		"ZeroSubgroupSize": {
			cfg:     func() swim.Config { c := validConfig; c.SubgroupSize = 0; return c }(),
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := swim.New(tc.cfg)

			if tc.wantErr {
				assert.NotNil(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestProbeDirectSuccess verifies that a peer that replies to a ping is not declared dead.
func TestProbeDirectSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t, 2)
		ctx, cancel := context.WithCancel(t.Context())
		c.start(ctx)

		// Round-robin: peer is selected within 2(n-1)=2 periods.
		time.Sleep(2 * c.protocolPeriod)
		synctest.Wait()

		c.assertFinalState(0, 1, swim.Alive)
		c.assertFinalState(1, 0, swim.Alive)
		cancel()
	})
}

// TestProbeIndirectSuccess verifies that a peer unreachable directly but reachable via an
// intermediary is not declared dead after indirect probing succeeds.
func TestProbeIndirectSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 3 nodes: node 0 probes node 1, node 2 is the intermediary.
		// node 1 is partitioned from node 0 but reachable from node 2.
		c := newCluster(t, 3)

		ctx, cancel := context.WithCancel(t.Context())
		c.start(ctx)

		// Let bootstrap complete so the cluster is formed before the partition.
		synctest.Wait()
		c.partitionBetween(0, 1)

		// Round-robin: node 1 is selected within 2(n-1)=4 periods. Once probed, the direct ack
		// times out and the indirect ping via node 2 succeeds within the same period.
		time.Sleep(c.protocolPeriod * 4)
		synctest.Wait()

		c.assertFinalState(0, 1, swim.Alive)
		c.assertFinalState(2, 1, swim.Alive)
		cancel()
	})
}

// TestProbeIndirectFailPeerDead verifies that a peer unreachable both directly and via all
// intermediaries is declared dead after indirect probing fails.
func TestProbeIndirectFailPeerDead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 3 nodes: node 0 probes node 1, node 2 is the intermediary.
		// node 1 is partitioned from everyone so no path exists.
		c := newCluster(t, 3)

		ctx, cancel := context.WithCancel(t.Context())
		c.start(ctx)

		// Let bootstrap complete so the cluster is formed before the partition.
		synctest.Wait()
		c.partition(1)

		// Round-robin: node 1 is selected within 2(n-1)=4 periods. Once probed, direct and indirect
		// pings both time out within the same period. Node 0 and node 2 run concurrently so both
		// converge within 4 periods.
		time.Sleep(c.protocolPeriod * 4)
		synctest.Wait()

		c.assertEvents(0,
			events{
				1: {swim.Alive, swim.Dead},
				2: {swim.Alive},
			},
		)
		c.assertEvents(1,
			events{
				0: {swim.Alive, swim.Dead},
				2: {swim.Alive, swim.Dead},
			},
		)
		c.assertEvents(2,
			events{
				0: {swim.Alive},
				1: {swim.Alive, swim.Dead},
			},
		)
		cancel()
	})
}

// TestProbeDirectFailPeerDead verifies that a peer that never replies is declared dead.
func TestProbeDirectFailPeerDead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t, 2)

		ctx, cancel := context.WithCancel(t.Context())
		c.start(ctx)

		// Let bootstrap complete so the cluster is formed before the partition.
		synctest.Wait()
		// Drop node 1 from the network so it never replies to pings.
		c.partition(1)

		// Round-robin: node 1 is selected within 2(n-1)=2 periods. The ack timeout and period
		// expiry both fall within the same period, so 2 periods is sufficient.
		time.Sleep(2 * c.protocolPeriod)
		synctest.Wait()

		c.assertEvents(0,
			events{
				1: {swim.Alive, swim.Dead},
			},
		)
		c.assertEvents(1,
			events{
				0: {swim.Alive, swim.Dead},
			},
		)
		cancel()
	})
}

// TestProbeNoPeers verifies that the probe loop handles having no peers without hanging.
// This can happen in a 2-node cluster once the only peer is declared dead.
func TestProbeNoPeers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t, 2)

		ctx, cancel := context.WithCancel(t.Context())
		c.start(ctx)

		// Let bootstrap complete so the cluster is formed before the partition.
		synctest.Wait()
		c.partition(1)

		// Same detection bound as TestProbeDirectFailPeerDead, then extra periods to exercise the
		// probe loop with an empty peer list.
		time.Sleep(c.protocolPeriod * 4)
		synctest.Wait()

		c.assertEvents(0,
			events{
				1: {swim.Alive, swim.Dead},
			},
		)
		c.assertEvents(1,
			events{
				0: {swim.Alive, swim.Dead},
			},
		)
		cancel()
	})
}
