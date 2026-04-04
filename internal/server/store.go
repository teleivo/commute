package server

import (
	"sync"

	"github.com/teleivo/commute/internal/crdt"
)

// Message is the gossip wire format: a snapshot of all counters keyed by name.
type Message map[string]*crdt.PNCounter

// Store holds the CRDT state for all keys.
type Store struct {
	nodeID     crdt.NodeID
	muCounters sync.RWMutex
	counters   map[string]*crdt.PNCounter
}

// NewStore creates a Store for the given node.
func NewStore(nodeID crdt.NodeID) *Store {
	return &Store{
		nodeID:   nodeID,
		counters: make(map[string]*crdt.PNCounter),
	}
}

// IncrementCounter adds value to the counter for key, creating it if it doesn't exist.
func (st *Store) IncrementCounter(key string, value uint64) {
	st.muCounters.Lock()
	counter, ok := st.counters[key]
	if !ok {
		counter = crdt.NewPNCounter(st.nodeID)
		st.counters[key] = counter
	}
	counter.Increment(value)
	st.muCounters.Unlock()
}

// DecrementCounter subtracts value from the counter for key, creating it if it doesn't exist.
func (st *Store) DecrementCounter(key string, value uint64) {
	st.muCounters.Lock()
	counter, ok := st.counters[key]
	if !ok {
		counter = crdt.NewPNCounter(st.nodeID)
		st.counters[key] = counter
	}
	counter.Decrement(value)
	st.muCounters.Unlock()
}

// GetCounter returns the value of the counter for key, or false if it doesn't exist.
func (st *Store) GetCounter(key string) (int64, bool) {
	st.muCounters.RLock()
	counter, ok := st.counters[key]
	if !ok {
		st.muCounters.RUnlock()
		return 0, false
	}
	value := counter.Value()
	st.muCounters.RUnlock()
	return value, true
}

// Merge incorporates the state from a gossip message into the store.
func (st *Store) Merge(msg Message) {
	st.muCounters.Lock()
	for k, counter := range msg {
		if _, ok := st.counters[k]; ok {
			st.counters[k].Merge(counter)
		} else {
			c := crdt.NewPNCounter(st.nodeID)
			c.Merge(counter)
			st.counters[k] = c
		}
	}
	st.muCounters.Unlock()
}
