package swim

import (
	"container/heap"
	"fmt"
	"sync"
)

const (
	eventHeaderSize = 2                               // 1 (Kind) + 1 (NodeLen)
	maxEventSize    = eventHeaderSize + maxTargetSize // worst-case event: max-length node address

	// 576 is the conservative IPv4 minimum reassembly buffer ([RFC 791]); minus IP header (20 bytes)
	// and UDP header (8 bytes).
	//
	// [RFC 791]: https://www.rfc-editor.org/rfc/rfc791
	udpPayload = 576 - 20 - 8

	// maxPiggybackEvents is how many events fit in a single message in the worst case: a ping-req
	// with a max-length target carrying max-length node addresses.
	maxPiggybackEvents = (udpPayload - maxBaseMessageSize) / maxEventSize
)

// EventKind identifies the type of membership change a [Notifier] receives.
type EventKind uint8

const (
	Dead EventKind = iota
	Alive
)

func (e EventKind) String() string {
	switch e {
	case Dead:
		return "dead"
	case Alive:
		return "alive"
	default:
		panic(fmt.Sprintf("unknown EventKind %d", uint8(e)))
	}
}

// Event is a membership change in the SWIM group.
type Event struct {
	Kind EventKind
	Node string
}

func (e *Event) UnmarshalBinary(data []byte) error {
	if len(data) < eventHeaderSize {
		return fmt.Errorf("event too short: need at least %d bytes for header, got %d", eventHeaderSize, len(data))
	}

	kind := EventKind(data[0])
	switch kind {
	case Dead, Alive:
	default:
		return fmt.Errorf("unknown event kind: %d", data[0])
	}

	node, err := unmarshalString("node", int(data[1]), data[2:])
	if err != nil {
		return err
	}

	e.Kind = kind
	e.Node = node

	return nil
}

// EventQueue is a concurrency-safe priority queue of membership events for
// dissemination via piggybacking. Events with the lowest send count are
// returned first, as per section 4.1 of the SWIM paper.
type EventQueue struct {
	pq eventQueueInternal
	mu sync.Mutex
}

// Push adds one or more items to the queue.
func (eq *EventQueue) Push(items ...EventItem) {
	if len(items) == 0 {
		return
	}

	eq.mu.Lock()

	for _, item := range items {
		heap.Push(&eq.pq, item)
	}

	eq.mu.Unlock()
}

// Pop removes and returns up to n items with the lowest send counts. Returns
// nil if the queue is empty.
func (eq *EventQueue) Pop(n int) []EventItem {
	eq.mu.Lock()

	var items []EventItem
	n = min(n, len(eq.pq))
	if n > 0 {
		items = make([]EventItem, 0, n)
	}
	for range n {
		items = append(items, *(heap.Pop(&eq.pq).(*EventItem)))
	}

	eq.mu.Unlock()

	return items
}

// EventItem wraps an Event with its dissemination send count.
type EventItem struct {
	Event     Event
	SendCount int // number of times this event has been piggybacked by this node

	index int
}

type eventQueueInternal []*EventItem

func (eq eventQueueInternal) Len() int {
	return len(eq)
}

func (eq eventQueueInternal) Less(i, j int) bool {
	return eq[i].SendCount < eq[j].SendCount
}

func (eq eventQueueInternal) Swap(i, j int) {
	eq[i], eq[j] = eq[j], eq[i]
	eq[i].index = i
	eq[j].index = j
}

func (eq *eventQueueInternal) Push(x any) {
	item := x.(EventItem)
	item.index = len(*eq)
	*eq = append(*eq, &item)
}

func (eq *eventQueueInternal) Pop() any {
	n := len(*eq)
	item := (*eq)[n-1]
	(*eq) = (*eq)[:n-1]
	return item
}
