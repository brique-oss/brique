package comm

import (
	"testing"
	"time"
)

func signedOfferFixture(places int, wait time.Duration) (*CommLoop, *InterfaceRuntime) {
	loop := &CommLoop{done: make(chan struct{})}
	runtime := &InterfaceRuntime{SignedExternalEgress: make(chan signedExternalEgressItem, places), egressWait: wait}
	return loop, runtime
}

// A burst of signed answers must not be dropped at the first full queue: a peer refused for its rate
// has every one of its calls answered at once, and each caller waits for its answer.
func TestSignedEgressHoldsTheRoutingLoopInsteadOfDroppingABurst(t *testing.T) {
	loop, runtime := signedOfferFixture(2, 5*time.Second)
	const burst = 40
	received := make(chan int, burst)
	go func() { // a writer slower than the burst
		for i := 0; i < burst; i++ {
			<-runtime.SignedExternalEgress
			time.Sleep(2 * time.Millisecond)
			received <- i
		}
	}()
	for i := 0; i < burst; i++ {
		if outcome := loop.offerSignedEgress(runtime, signedExternalEgressItem{Key: "k"}); outcome != offerAccepted {
			t.Fatalf("answer %d of the burst was dropped (outcome %d)", i, outcome)
		}
	}
	for i := 0; i < burst; i++ {
		select {
		case <-received:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d answers reached the writer", i, burst)
		}
	}
}

func TestSignedEgressGivesUpAfterTheWaitAndStopsWithTheLoop(t *testing.T) {
	loop, runtime := signedOfferFixture(1, 40*time.Millisecond)
	if loop.offerSignedEgress(runtime, signedExternalEgressItem{}) != offerAccepted {
		t.Fatal("a free place was refused")
	}
	started := time.Now()
	if outcome := loop.offerSignedEgress(runtime, signedExternalEgressItem{}); outcome != offerTimedOut {
		t.Fatalf("a queue nobody drains answered %d, want a timeout", outcome)
	}
	if waited := time.Since(started); waited < 30*time.Millisecond || waited > 2*time.Second {
		t.Fatalf("waited %s for a 40 ms bound", waited)
	}
	go func() { time.Sleep(20 * time.Millisecond); close(loop.done) }()
	runtime.egressWait = 5 * time.Second
	if outcome := loop.offerSignedEgress(runtime, signedExternalEgressItem{}); outcome != offerStopped {
		t.Fatalf("a stopping loop answered %d, want stopped", outcome)
	}
}

func TestSignedTransportQueueWaitsForAPlaceToo(t *testing.T) {
	loop := &CommLoop{done: make(chan struct{}), signedTransportEgress: make(chan signedExternalEgressItem, 1)}
	loop.signedTransportEgress <- signedExternalEgressItem{}
	go func() { time.Sleep(30 * time.Millisecond); <-loop.signedTransportEgress }()
	if !loop.enqueueSignedTransport(signedExternalEgressItem{Key: "late"}) {
		t.Fatal("an answer was refused although a place came free within the wait")
	}
}
