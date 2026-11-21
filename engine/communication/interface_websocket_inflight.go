package comm

// communication/interface_websocket_inflight.go
//
// The awaited intentions in flight to one wrapper, by connection.
//
// Why: a wrapper whose connection drops will never answer what was written to
// that connection. Without this registry the callers wait for their own
// timeout. With it, the loss is answered at once, with reason `connection_lost`.
//
// What the answer means: the intention may have been executed before the loss
// (a write can be committed and its Response lost). `connection_lost` therefore
// says "outcome unknown", never "refused before execution": it is not a reason a
// caller may retry blindly, unlike a busy refusal.
//
// Contract:
// - Only awaited intentions are recorded, stripped of their parameters (an entry
//   is a few hundred bytes whatever the size of the request).
// - An entry leaves the registry on the first terminal Response read from the
//   wrapper for its intention id (a `running` response does not settle it), or
//   when its connection is lost.
// - Losing a connection answers every entry recorded for that connection exactly
//   once; entries of other connections are untouched.

import (
	"sync"

	"brique_engine/engine/circulation"
	"brique_engine/engine/junction"
)

type inflightEntry struct {
	connection junction.WrapperConnection
	request    circulation.Message
	// external is the admitted external request this message dispatches, when it is one.
	external *junction.Delivery
}

type inflightTracker struct {
	mu      sync.Mutex
	entries map[string]inflightEntry
}

func newInflightTracker() *inflightTracker {
	return &inflightTracker{entries: make(map[string]inflightEntry)}
}

// add records an awaited intention written to connection. Anything else is ignored.
func (t *inflightTracker) add(connection junction.WrapperConnection, msg circulation.Message, external ...*junction.Delivery) {
	if t == nil || msg.Kind != circulation.ValueKindIntention || !msg.Intention.AwaitResponse || msg.Intention.IntentionID == "" {
		return
	}
	stripped := msg
	stripped.Intention.Params = nil
	t.mu.Lock()
	entry := inflightEntry{connection: connection, request: stripped}
	if len(external) == 1 {
		entry.external = external[0]
	}
	t.entries[msg.Intention.IntentionID] = entry
	t.mu.Unlock()
}

// settle forgets the intention a terminal Response answers.
func (t *inflightTracker) settle(msg circulation.Message) {
	if t == nil || msg.Kind != circulation.ValueKindResponse || msg.Response.Status == circulation.ValueStatusRunning {
		return
	}
	t.mu.Lock()
	delete(t.entries, msg.Response.IntentionID)
	t.mu.Unlock()
}

// lose removes and returns the intentions recorded for connection.
func (t *inflightTracker) lose(connection junction.WrapperConnection) []inflightEntry {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var lost []inflightEntry
	for id, entry := range t.entries {
		if entry.connection == connection {
			lost = append(lost, entry)
			delete(t.entries, id)
		}
	}
	return lost
}

func (t *inflightTracker) size() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}
