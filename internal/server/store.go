package server

import (
	"encoding/json"
	"sync"

	"github.com/teleivo/commute/internal/crdt"
)

// Message is the gossip wire format: a snapshot of all CRDT state.
type Message struct {
	Counters  map[string]*crdt.PNCounter   `json:"counters"`
	Registers map[string]*crdt.LWWRegister `json:"registers"`
	Sets      map[string]*crdt.ORSet       `json:"sets"`
}

// Store holds the CRDT state for all keys.
type Store struct {
	nodeID      crdt.NodeID
	clock       crdt.Clock
	muCounters  sync.RWMutex
	counters    map[string]*crdt.PNCounter
	muRegisters sync.RWMutex
	registers   map[string]*crdt.LWWRegister
	muSets      sync.RWMutex
	sets        map[string]*crdt.ORSet
}

// NewStore creates a Store for the given node.
func NewStore(nodeID crdt.NodeID, clock crdt.Clock) *Store {
	return &Store{
		nodeID:    nodeID,
		clock:     clock,
		counters:  make(map[string]*crdt.PNCounter),
		registers: make(map[string]*crdt.LWWRegister),
		sets:      make(map[string]*crdt.ORSet),
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

// SetRegister writes a value to the register for key, creating it if it doesn't exist.
func (st *Store) SetRegister(key string, value json.RawMessage) {
	st.muRegisters.Lock()
	register, ok := st.registers[key]
	if !ok {
		register = crdt.NewLWWRegister(st.nodeID, st.clock)
		st.registers[key] = register
	}
	register.Set(value)
	st.muRegisters.Unlock()
}

// GetRegister returns the value of the register for key, or false if it doesn't exist.
func (st *Store) GetRegister(key string) (json.RawMessage, bool) {
	st.muRegisters.RLock()
	register, ok := st.registers[key]
	if !ok {
		st.muRegisters.RUnlock()
		return nil, false
	}
	value := register.Value()
	st.muRegisters.RUnlock()
	return value, true
}

// GetSet returns the values of the set for key, or false if it doesn't exist.
func (st *Store) GetSet(key string) ([]string, map[string]crdt.VV, bool) {
	st.muSets.RLock()
	set, ok := st.sets[key]
	if !ok {
		st.muSets.RUnlock()
		return nil, nil, false
	}
	value := set.Values()
	vvs := set.CausalContexts()
	st.muSets.RUnlock()
	return value, vvs, true
}

// AddSet adds a value to the set for key with the given client causal context vv, creating the
// set if it doesn't exist.
func (st *Store) AddSet(key, value string, vv crdt.VV) {
	st.muSets.Lock()
	set, ok := st.sets[key]
	if !ok {
		set = crdt.NewORSet(st.nodeID)
		st.sets[key] = set
	}
	set.Add(value, vv)
	st.muSets.Unlock()
}

// RemoveSet removes a value from the set for key with the given client causal context vv. It is
// a no-op if the set does not exist on this replica.
func (st *Store) RemoveSet(key, value string, vv crdt.VV) {
	st.muSets.Lock()
	defer st.muSets.Unlock()
	set, ok := st.sets[key]
	if !ok {
		return
	}
	set.Remove(value, vv)
}

// Merge incorporates the state from a gossip message into the store.
func (st *Store) Merge(msg Message) {
	st.muCounters.Lock()
	for k, incoming := range msg.Counters {
		if _, ok := st.counters[k]; !ok {
			st.counters[k] = crdt.NewPNCounter(st.nodeID)
		}
		st.counters[k].Merge(incoming)
	}
	st.muCounters.Unlock()

	st.muRegisters.Lock()
	for k, incoming := range msg.Registers {
		if _, ok := st.registers[k]; !ok {
			st.registers[k] = crdt.NewLWWRegister(st.nodeID, st.clock)
		}
		st.registers[k].Merge(incoming)
	}
	st.muRegisters.Unlock()

	st.muSets.Lock()
	for k, incoming := range msg.Sets {
		if _, ok := st.sets[k]; !ok {
			st.sets[k] = crdt.NewORSet(st.nodeID)
		}
		st.sets[k].Merge(incoming)
	}
	st.muSets.Unlock()
}

// MarshalState returns the JSON encoding of all CRDT state.
func (st *Store) MarshalState() ([]byte, error) {
	st.muCounters.RLock()
	defer st.muCounters.RUnlock()
	st.muRegisters.RLock()
	defer st.muRegisters.RUnlock()
	st.muSets.RLock()
	defer st.muSets.RUnlock()
	return json.Marshal(Message{
		Counters:  st.counters,
		Registers: st.registers,
		Sets:      st.sets,
	})
}
