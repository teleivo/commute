package server

import (
	"encoding/json"
	"sync"

	"github.com/teleivo/commute/internal/crdt"
)

// Message is the gossip wire format carrying either a full snapshot or a delta-interval of CRDT state.
// It is used inbound: json.Unmarshal into Message, then Store.Merge works with the typed maps.
// Outbound, Delta marshals the typed store maps to []byte while holding each lock, so the live maps
// never escape the lock. Message cannot serve both directions because the lock boundary prevents
// assigning live maps to Message fields and marshaling after the lock drops.
type Message struct {
	CountersSeq  uint64                       `json:"countersSeq"`
	Counters     map[string]*crdt.PNCounter   `json:"counters"`
	RegistersSeq uint64                       `json:"registersSeq"`
	Registers    map[string]*crdt.LWWRegister `json:"registers"`
	SetsSeq      uint64                       `json:"setsSeq"`
	Sets         map[string]*crdt.ORSet       `json:"sets"`
}

// AckMessage is the gossip acknowledgement wire format. Each sequence number is the highest
// sequence the sender has incorporated for the corresponding CRDT type.
type AckMessage struct {
	CountersSeq  uint64 `json:"countersSeq"`
	RegistersSeq uint64 `json:"registersSeq"`
	SetsSeq      uint64 `json:"setsSeq"`
}

// Store holds the CRDT state for all keys.
type Store struct {
	nodeID         crdt.NodeID
	clock          crdt.Clock
	muCounters     sync.RWMutex
	counters       map[string]*crdt.PNCounter
	counterSeq     uint64
	deltaCounters  map[uint64]map[string]crdt.PNCounter
	ackCounters    map[string]uint64
	muRegisters    sync.RWMutex
	registers      map[string]*crdt.LWWRegister
	registersSeq   uint64
	deltaRegisters map[uint64]map[string]crdt.LWWRegister
	ackRegisters   map[string]uint64
	muSets         sync.RWMutex
	sets           map[string]*crdt.ORSet
	setsSeq        uint64
	deltaSets      map[uint64]map[string]crdt.ORSet
	ackSets        map[string]uint64
}

// NewStore creates a Store for the given node.
func NewStore(nodeID crdt.NodeID, clock crdt.Clock) *Store {
	return &Store{
		nodeID:         nodeID,
		clock:          clock,
		counters:       make(map[string]*crdt.PNCounter),
		counterSeq:     1,
		deltaCounters:  make(map[uint64]map[string]crdt.PNCounter),
		ackCounters:    make(map[string]uint64),
		registers:      make(map[string]*crdt.LWWRegister),
		registersSeq:   1,
		deltaRegisters: make(map[uint64]map[string]crdt.LWWRegister),
		ackRegisters:   make(map[string]uint64),
		sets:           make(map[string]*crdt.ORSet),
		setsSeq:        1,
		deltaSets:      make(map[uint64]map[string]crdt.ORSet),
		ackSets:        make(map[string]uint64),
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
	d := counter.Increment(value)
	counter.Merge(&d)
	st.deltaCounters[st.counterSeq] = map[string]crdt.PNCounter{
		key: d,
	}
	st.counterSeq++
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
	d := counter.Decrement(value)
	counter.Merge(&d)
	st.deltaCounters[st.counterSeq] = map[string]crdt.PNCounter{
		key: d,
	}
	st.counterSeq++
	st.muCounters.Unlock()
}

// CounterIncrements returns a snapshot of the per-node increment counters for all keys.
func (st *Store) CounterIncrements() map[string]map[crdt.NodeID]uint64 {
	st.muCounters.RLock()
	defer st.muCounters.RUnlock()
	out := make(map[string]map[crdt.NodeID]uint64, len(st.counters))
	for key, pn := range st.counters {
		out[key] = pn.IncCounters()
	}
	return out
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
	d := register.Set(value)
	register.Merge(&d)
	st.deltaRegisters[st.registersSeq] = map[string]crdt.LWWRegister{
		key: d,
	}
	st.registersSeq++
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
	d := set.Add(value, vv)
	set.Merge(&d)
	st.deltaSets[st.setsSeq] = map[string]crdt.ORSet{
		key: d,
	}
	st.setsSeq++
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
	d := set.Remove(value, vv)
	if d.IsZero() {
		return
	}
	set.Merge(&d)
	st.deltaSets[st.setsSeq] = map[string]crdt.ORSet{
		key: d,
	}
	st.setsSeq++
}

// Merge incorporates the state from a gossip message into the store.
func (st *Store) Merge(msg Message) AckMessage {
	st.muCounters.Lock()
	for k, incoming := range msg.Counters {
		local, ok := st.counters[k]
		if ok && incoming.IsLessOrEqual(local) {
			continue
		}
		if !ok {
			st.counters[k] = crdt.NewPNCounter(st.nodeID)
		}
		st.counters[k].Merge(incoming)
		st.deltaCounters[st.counterSeq] = map[string]crdt.PNCounter{
			k: *incoming,
		}
		st.counterSeq++
	}
	st.muCounters.Unlock()

	st.muRegisters.Lock()
	for k, incoming := range msg.Registers {
		local, ok := st.registers[k]
		if ok && incoming.IsLessOrEqual(local) {
			continue
		}
		if !ok {
			st.registers[k] = crdt.NewLWWRegister(st.nodeID, st.clock)
		}
		st.registers[k].Merge(incoming)
		st.deltaRegisters[st.registersSeq] = map[string]crdt.LWWRegister{
			k: *incoming,
		}
		st.registersSeq++
	}
	st.muRegisters.Unlock()

	st.muSets.Lock()
	for k, incoming := range msg.Sets {
		local, ok := st.sets[k]
		if ok && incoming.IsLessOrEqual(local) {
			continue
		}
		if !ok {
			st.sets[k] = crdt.NewORSet(st.nodeID)
		}
		st.sets[k].Merge(incoming)
		st.deltaSets[st.setsSeq] = map[string]crdt.ORSet{
			k: *incoming,
		}
		st.setsSeq++
	}
	st.muSets.Unlock()

	return AckMessage{
		CountersSeq:  msg.CountersSeq,
		RegistersSeq: msg.RegistersSeq,
		SetsSeq:      msg.SetsSeq,
	}
}

// Ack records the highest sequence numbers acknowledged by sender so Delta can skip state the
// peer already has.
func (st *Store) Ack(sender string, body AckMessage) {
	st.muCounters.Lock()
	st.ackCounters[sender] = max(st.ackCounters[sender], body.CountersSeq)
	st.muCounters.Unlock()

	st.muRegisters.Lock()
	st.ackRegisters[sender] = max(st.ackRegisters[sender], body.RegistersSeq)
	st.muRegisters.Unlock()

	st.muSets.Lock()
	st.ackSets[sender] = max(st.ackSets[sender], body.SetsSeq)
	st.muSets.Unlock()
}

// Delta returns the gossip message for peer as JSON, marshaled under each per-CRDT-type lock to
// prevent a data race: on the full-state path the live store maps must not be read after the lock
// drops, so marshaling happens inside the lock. Message cannot be used here because its typed map
// fields would hold references to the live maps past the lock boundary.
func (st *Store) Delta(peer string) ([]byte, bool) {
	var ok bool
	var countersSeq, registersSeq, setsSeq uint64
	var countersJSON, registersJSON, setsJSON []byte

	st.muCounters.RLock()
	ackedSeq := st.ackCounters[peer]
	if ackedSeq == 0 { // full
		ok = true
		countersSeq = st.counterSeq
		countersJSON = mustMarshal(st.counters)
	} else if ackedSeq < st.counterSeq { // delta
		ok = true
		countersSeq = st.counterSeq
		counters := make(map[string]*crdt.PNCounter)
		for i := ackedSeq; i < st.counterSeq; i++ {
			for k, v := range st.deltaCounters[i] {
				local, exists := counters[k]
				if !exists {
					counters[k] = &v
				} else {
					local.Merge(&v)
				}
			}
		}
		countersJSON = mustMarshal(counters)
	}
	st.muCounters.RUnlock()

	st.muRegisters.RLock()
	registerSeq := st.ackRegisters[peer]
	if registerSeq == 0 { // full
		ok = true
		registersSeq = st.registersSeq
		registersJSON = mustMarshal(st.registers)
	} else if registerSeq < st.registersSeq { // delta
		ok = true
		registersSeq = st.registersSeq
		registers := make(map[string]*crdt.LWWRegister)
		for i := registerSeq; i < st.registersSeq; i++ {
			for k, v := range st.deltaRegisters[i] {
				local, exists := registers[k]
				if !exists {
					registers[k] = &v
				} else {
					local.Merge(&v)
				}
			}
		}
		registersJSON = mustMarshal(registers)
	}
	st.muRegisters.RUnlock()

	st.muSets.RLock()
	setseq := st.ackSets[peer]
	if setseq == 0 { // full
		ok = true
		setsSeq = st.setsSeq
		setsJSON = mustMarshal(st.sets)
	} else if setseq < st.setsSeq { // delta
		ok = true
		setsSeq = st.setsSeq
		sets := make(map[string]*crdt.ORSet)
		for i := setseq; i < st.setsSeq; i++ {
			for k, v := range st.deltaSets[i] {
				local, exists := sets[k]
				if !exists {
					sets[k] = v.Clone()
				} else {
					local.Merge(&v)
				}
			}
		}
		setsJSON = mustMarshal(sets)
	}
	st.muSets.RUnlock()

	if !ok {
		return nil, false
	}
	b := mustMarshal(struct {
		CountersSeq  uint64          `json:"countersSeq"`
		Counters     json.RawMessage `json:"counters"`
		RegistersSeq uint64          `json:"registersSeq"`
		Registers    json.RawMessage `json:"registers"`
		SetsSeq      uint64          `json:"setsSeq"`
		Sets         json.RawMessage `json:"sets"`
	}{
		CountersSeq:  countersSeq,
		Counters:     countersJSON,
		RegistersSeq: registersSeq,
		Registers:    registersJSON,
		SetsSeq:      setsSeq,
		Sets:         setsJSON,
	})
	return b, true
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
