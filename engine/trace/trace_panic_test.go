package trace

import (
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
)

// The trace loop is written to by every other family: a message that makes its
// ingress panic costs that message, which is counted as rejected, and the loop
// goes on with the next one.
func TestTraceIngressPanicCostsOneMessage(t *testing.T) {
	l := &TraceLoop{
		cfg: TraceCfg{
			Enabled:            true,
			FlushEveryN:        1,
			FlushEveryInterval: 1000,
			RingCapacity:       16,
		},
		ring:       &eventRing{capacity: 16}, // room but no buffer: pushing indexes outside it and panics
		deliveryIn: make(chan junction.Delivery, 8),
		flushReq:   make(chan []circulation.TraceWire, 8),
		done:       make(chan struct{}),
	}
	exit := make(chan struct{})
	go func() {
		defer close(exit)
		l.loopIngress()
	}()

	tw := circulation.TraceWire{Timestamp: "2026-03-07T10:00:00Z", TraceKind: circulation.ValueTraceCommIngress}
	for i := 0; i < 3; i++ {
		l.deliveryIn <- localTestDelivery(circulation.Message{Kind: circulation.ValueKindTrace, Trace: tw, TS: tw.Timestamp})
	}
	waitUntil(t, 2*time.Second, func() bool { return l.counters.ringRejected.Load() >= 3 }, "the three messages are counted as rejected")

	select {
	case <-exit:
		t.Fatal("the ingress loop ended")
	default:
	}
	close(l.done)
	waitUntil(t, time.Second, func() bool {
		select {
		case <-exit:
			return true
		default:
			return false
		}
	}, "ingress exit")
}
