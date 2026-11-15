package comm

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
)

// --- the offer ------------------------------------------------------------------------------

func TestOfferWithAPlaceDoesNotWait(t *testing.T) {
	queue := make(chan int, 1)
	outcome, waited := offer(queue, 1, time.Hour, make(chan struct{}))
	if outcome != offerAccepted || waited != 0 || len(queue) != 1 {
		t.Fatalf("outcome=%v waited=%v queued=%d, want accepted at once", outcome, waited, len(queue))
	}
}

func TestOfferIsAcceptedWhenAPlaceComesDuringTheWait(t *testing.T) {
	queue := make(chan int, 1)
	queue <- 0
	go func() { time.Sleep(30 * time.Millisecond); <-queue }()
	outcome, waited := offer(queue, 1, 2*time.Second, make(chan struct{}))
	if outcome != offerAccepted || waited < 20*time.Millisecond || waited > time.Second {
		t.Fatalf("outcome=%v waited=%v, want accepted after about 30 ms", outcome, waited)
	}
}

func TestOfferGivesUpAfterTheWait(t *testing.T) {
	queue := make(chan int, 1)
	queue <- 0
	started := time.Now()
	outcome, waited := offer(queue, 1, 50*time.Millisecond, make(chan struct{}))
	if outcome != offerTimedOut || time.Since(started) < 45*time.Millisecond || time.Since(started) > time.Second {
		t.Fatalf("outcome=%v after %v (reported %v), want a time out after about 50 ms", outcome, time.Since(started), waited)
	}
	if len(queue) != 1 {
		t.Fatal("a message that timed out must not have been queued")
	}
}

func TestOfferStopsWithTheLoop(t *testing.T) {
	queue := make(chan int, 1)
	queue <- 0
	done := make(chan struct{})
	go func() { time.Sleep(20 * time.Millisecond); close(done) }()
	outcome, _ := offer(queue, 1, time.Hour, done)
	if outcome != offerStopped {
		t.Fatalf("outcome=%v, want stopped", outcome)
	}
}

// --- the settings ---------------------------------------------------------------------------

func TestEgressSettingsDefaultsContextAndInterfaceOverride(t *testing.T) {
	if places, wait := egressSettings(CommCfg{}, InterfaceCfg{}); places != 16 || wait != 5*time.Second {
		t.Fatalf("defaults = %d, %v, want 16 and 5s", places, wait)
	}
	cfg := ParseCommCfg(map[string]any{configuration.KeyEgressBuffer: float64(64), configuration.KeyEgressWaitMs: float64(1500)})
	if places, wait := egressSettings(cfg, InterfaceCfg{}); places != 64 || wait != 1500*time.Millisecond {
		t.Fatalf("context settings = %d, %v, want 64 and 1.5s", places, wait)
	}
	own := InterfaceCfg{Config: map[string]any{configuration.KeyEgressBuffer: float64(8), configuration.KeyEgressWaitMs: float64(200)}}
	if places, wait := egressSettings(cfg, own); places != 8 || wait != 200*time.Millisecond {
		t.Fatalf("interface settings = %d, %v, want 8 and 200ms (its own win)", places, wait)
	}
	if places, wait := egressSettings(cfg, InterfaceCfg{Config: map[string]any{configuration.KeyEgressBuffer: float64(-3), configuration.KeyEgressWaitMs: "x"}}); places != 64 || wait != 1500*time.Millisecond {
		t.Fatalf("invalid interface values must be ignored, got %d, %v", places, wait)
	}
}

// --- a slow consumer does not stop the others -----------------------------------------------

// disconnectingImpl is the consumer of an interface; it records that its connection was closed.
type disconnectingImpl struct {
	InterfaceImpl
	closed atomic.Int32
	reason atomic.Value
}

func (d *disconnectingImpl) Disconnect(reason string) { d.reason.Store(reason); d.closed.Add(1) }

func TestASlowConsumerIsGivenUpAndTheHealthyOneIsServed(t *testing.T) {
	reg := newMockCommReg()
	l, traceCh := newEgressLoop("/ctx/local", reg)
	slowImpl := &disconnectingImpl{}
	slow := &InterfaceRuntime{Egress: make(chan circulation.Message, 4), status: InterfaceRunning, egressWait: 60 * time.Millisecond, Impl: slowImpl}
	healthy := &InterfaceRuntime{Egress: make(chan circulation.Message, 4), status: InterfaceRunning, egressWait: 60 * time.Millisecond}
	l.ifaces["slow"], l.ifaces["healthy"] = slow, healthy

	started := time.Now()
	served := make(chan time.Duration, 1)
	go func() { // the routing loop: one message after the other, for everyone
		for i := 0; i < 5; i++ { // 4 fill the queue of the slow one, the 5th waits
			l.sendToIface("slow", mkIntentionMsg("@ui_slow:/x", circulation.ValueTypeMatter, "/ctx/local"))
		}
		l.sendToIface("healthy", mkIntentionMsg("@ui_healthy:/x", circulation.ValueTypeMatter, "/ctx/local"))
		served <- time.Since(started)
	}()
	select {
	case after := <-served:
		if after > 2*time.Second {
			t.Fatalf("the healthy interface was served after %v, want about the wait of the slow one (60 ms)", after)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a consumer that no longer reads stopped the routing of the healthy interface")
	}
	if len(healthy.Egress) != 1 {
		t.Fatalf("the healthy queue holds %d messages, want 1", len(healthy.Egress))
	}
	deadline := time.Now().Add(time.Second)
	for slowImpl.closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if slowImpl.closed.Load() != 1 {
		t.Fatalf("the connection of the slow consumer was closed %d times, want once", slowImpl.closed.Load())
	}
	stats := l.EgressStats()
	if s := stats["slow"]; s.Offers != 5 || s.Full != 1 || s.Timeouts != 1 || s.MaxWait < 50*time.Millisecond {
		t.Fatalf("slow stats = %+v, want 5 offers, 1 found full, 1 timeout, a wait of about 60 ms", s)
	}
	if h := stats["healthy"]; h.Full != 0 || h.Timeouts != 0 || h.Waited != 0 {
		t.Fatalf("healthy stats = %+v, want nothing met", h)
	}
	// The message that gave up is reported as a delivery failure.
	found := false
	for len(traceCh) > 0 {
		if m := unwrapTestMessage(t, <-traceCh); m.Kind != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("the message that could not be queued was not reported")
	}
}

func TestAQueueThatFindsAPlaceInTimeIsNotGivenUp(t *testing.T) {
	reg := newMockCommReg()
	l, _ := newEgressLoop("/ctx/local", reg)
	impl := &disconnectingImpl{}
	rt := &InterfaceRuntime{Egress: make(chan circulation.Message, 1), status: InterfaceRunning, egressWait: time.Second, Impl: impl}
	l.ifaces["busy"] = rt
	l.sendToIface("busy", mkIntentionMsg("@ui_busy:/x", circulation.ValueTypeMatter, "/ctx/local"))
	go func() { time.Sleep(40 * time.Millisecond); <-rt.Egress }()
	result := l.sendToIface("busy", mkIntentionMsg("@ui_busy:/x", circulation.ValueTypeMatter, "/ctx/local"))
	if !result.accepted() {
		t.Fatalf("a queue that frees a place within the wait must accept: %+v", result)
	}
	time.Sleep(20 * time.Millisecond)
	if impl.closed.Load() != 0 {
		t.Fatal("an interface that took the message in time must not be disconnected")
	}
	if s := l.EgressStats()["busy"]; s.Full != 1 || s.Timeouts != 0 || s.Waited <= 0 {
		t.Fatalf("stats = %+v, want one wait that ended in acceptance", s)
	}
}

// --- an unsent message to a wrapper is reported ----------------------------------------------

type failingHandle struct {
	sends  atomic.Int32
	closes atomic.Int32
}

func (h *failingHandle) Send(circulation.Message) error {
	h.sends.Add(1)
	return errors.New("write failed")
}
func (h *failingHandle) Close() error { h.closes.Add(1); return nil }

var _ junction.WebSocketRouteHandle = (*failingHandle)(nil)

func TestAMessageNotWrittenToAWrapperIsReportedAsConnectionLost(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		reported bool
	}{{EndpointWrapper, true}, {EndpointUI, false}} {
		var mu sync.Mutex
		var outcomes []InterfaceEgressOutcome
		rt := &InterfaceRuntime{Type: tc.endpoint, Egress: make(chan circulation.Message, 2), status: InterfaceRunning}
		rt.ReportEgressOutcome = func(_ circulation.Message, outcome InterfaceEgressOutcome) {
			mu.Lock()
			outcomes = append(outcomes, outcome)
			mu.Unlock()
		}
		handle := &failingHandle{}
		w := &wsInterface{rt: rt, handle: handle, done: make(chan struct{}), inflight: newInflightTracker()}
		rt.Egress <- mkIntentionMsg("@wrapper_w:/x", circulation.ValueTypeUser, "/ctx/local")
		finished := make(chan struct{})
		go func() { _ = w.WriteLoop(); close(finished) }()
		deadline := time.Now().Add(time.Second)
		for handle.sends.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
		close(w.done)
		<-finished
		mu.Lock()
		got := len(outcomes)
		mu.Unlock()
		if tc.reported {
			if got != 1 || !outcomes[0].Definitive || outcomes[0].Reason != circulation.ValueReasonConnectionLost {
				t.Fatalf("%s: outcomes=%+v, want one definitive connection_lost", tc.endpoint, outcomes)
			}
		} else if got != 0 {
			t.Fatalf("%s: a UI that misses a message reconnects; nothing must be reported, got %+v", tc.endpoint, outcomes)
		}
	}
}

func TestDisconnectClosesTheRouteHandle(t *testing.T) {
	handle := &failingHandle{}
	w := &wsInterface{handle: handle}
	w.Disconnect("too slow")
	if handle.closes.Load() != 1 {
		t.Fatalf("closes=%d, want 1", handle.closes.Load())
	}
	(&wsInterface{}).Disconnect("no handle") // must not panic
}

// --- an answer refused as overloaded is sent again -------------------------------------------

func TestSendWithRetry(t *testing.T) {
	refused := func(status int) InterfaceEgressOutcome {
		return InterfaceEgressOutcome{Definitive: true, StatusCode: status, Err: errors.New("refused")}
	}
	short := []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	run := func(results []InterfaceEgressOutcome) (InterfaceEgressOutcome, int) {
		calls := 0
		out := sendWithRetry(func() InterfaceEgressOutcome {
			r := results[min(calls, len(results)-1)]
			calls++
			return r
		}, short, make(chan struct{}))
		return out, calls
	}
	if out, calls := run([]InterfaceEgressOutcome{refused(429), refused(503), {Definitive: true, StatusCode: 202}}); out.Err != nil || calls != 3 {
		t.Fatalf("a 429 then a 503 then an acceptance: err=%v calls=%d, want delivered after 3", out.Err, calls)
	}
	if out, calls := run([]InterfaceEgressOutcome{refused(429)}); out.Err == nil || calls != 4 {
		t.Fatalf("a peer that keeps refusing: err=%v calls=%d, want the refusal after 1+3 tries", out.Err, calls)
	}
	for _, status := range []int{400, 401, 403, 413} {
		if out, calls := run([]InterfaceEgressOutcome{refused(status)}); out.Err == nil || calls != 1 {
			t.Fatalf("a %d is a decision of the peer: calls=%d, want no retry", status, calls)
		}
	}
	if out, calls := run([]InterfaceEgressOutcome{{Definitive: false, Err: errors.New("network")}}); out.Err == nil || calls != 1 {
		t.Fatalf("a network failure has an unknown outcome and must not be sent again: calls=%d", calls)
	}
	stop := make(chan struct{})
	close(stop)
	calls := 0
	sendWithRetry(func() InterfaceEgressOutcome { calls++; return refused(429) }, []time.Duration{time.Hour}, stop)
	if calls != 1 {
		t.Fatalf("a stopped interface must not wait to retry: calls=%d", calls)
	}
}

// --- the silence a call to a peer tolerates ---------------------------------------------------

func TestExternalSilenceDefaultSettingAndLimit(t *testing.T) {
	for _, tc := range []struct {
		ms   int
		want time.Duration
	}{{0, 2 * time.Minute}, {-5, 2 * time.Minute}, {45000, 45 * time.Second}, {1800000, 30 * time.Minute}, {99999999, 30 * time.Minute}} {
		l := &CommLoop{cfg: CommCfg{ExternalCallSilenceMs: tc.ms}}
		if got := l.externalSilence(); got != tc.want {
			t.Fatalf("external_call_silence_ms=%d: %v, want %v", tc.ms, got, tc.want)
		}
	}
	if got := ParseCommCfg(map[string]any{configuration.KeyExternalCallSilenceMs: float64(45000)}).ExternalCallSilenceMs; got != 45000 {
		t.Fatalf("parsed %d, want 45000", got)
	}
	if (*CommLoop)(nil).externalSilence() != 2*time.Minute {
		t.Fatal("a nil loop must give the default")
	}
}

// --- what read.state reports -----------------------------------------------------------------

func TestInterfaceEgressReportsQueueAndCounters(t *testing.T) {
	reg := newMockCommReg()
	l, _ := newEgressLoop("/ctx/local", reg)
	l.ifaces["b_wrapper"] = &InterfaceRuntime{Type: EndpointWrapper, Egress: make(chan circulation.Message, 4), status: InterfaceRunning, egressWait: 30 * time.Millisecond, Impl: &disconnectingImpl{}}
	l.ifaces["a_ui"] = &InterfaceRuntime{Type: EndpointUI, Egress: make(chan circulation.Message, 16), status: InterfaceRunning}
	for i := 0; i < 5; i++ { // 4 fill the queue, the 5th waits and is given up
		l.sendToIface("b_wrapper", mkIntentionMsg("@wrapper_b:/x", circulation.ValueTypeUser, "/ctx/local"))
	}
	l.sendToIface("a_ui", mkIntentionMsg("@ui_a:/x", circulation.ValueTypeMatter, "/ctx/local"))

	got := l.InterfaceEgress()
	if len(got) != 2 || got[0].Name != "a_ui" || got[1].Name != "b_wrapper" {
		t.Fatalf("interfaces = %+v, want a_ui then b_wrapper", got)
	}
	ui, wrapper := got[0], got[1]
	if ui.Type != EndpointUI || ui.Status != "running" || ui.Places != 16 || ui.Queued != 1 || ui.Offers != 1 || ui.Full != 0 || ui.Timeouts != 0 {
		t.Fatalf("a_ui = %+v", ui)
	}
	if wrapper.Places != 4 || wrapper.Queued != 4 || wrapper.Offers != 5 || wrapper.Full != 1 || wrapper.Timeouts != 1 || wrapper.MaxWaitNs < int64(25*time.Millisecond) {
		t.Fatalf("b_wrapper = %+v, want 4/4 queued, 5 offers, 1 found full, 1 given up after about 30 ms", wrapper)
	}
	if (*CommLoop)(nil).InterfaceEgress() != nil {
		t.Fatal("a nil loop reports nothing")
	}
}
