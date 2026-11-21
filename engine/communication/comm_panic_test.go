package comm

import (
	"strings"
	"testing"

	"brique_engine/engine/circulation"
	"brique_engine/engine/configuration"
	"brique_engine/engine/junction"
	"brique_engine/engine/shared"
)

// panicOnResolveReg is a registry whose lookup of one address panics.
type panicOnResolveReg struct {
	*mockCommReg
	boom shared.ContextAddr
}

func (r *panicOnResolveReg) ResolveCh(addr shared.ContextAddr) (chan<- junction.Delivery, bool) {
	if addr == r.boom {
		panic("boom")
	}
	return r.mockCommReg.ResolveCh(addr)
}

func newPanicLoop(reg junction.ContextCommInRegistry) (*CommLoop, chan junction.Delivery) {
	traceCh := make(chan junction.Delivery, 32)
	l := &CommLoop{
		frame: &junction.ContextRegistry{
			CtxId:        "/ctx/a",
			CtxCommReg:   reg,
			FamIn:        junction.FamiliesInChanRegistry{shared.FamilyTrace: traceCh},
			TraceEnabled: true,
			TraceLevel:   configuration.ValueConfigTraceLevelDebug,
		},
		ifaces: map[string]*InterfaceRuntime{},
		done:   make(chan struct{}),
	}
	return l, traceCh
}

func awaitedIntention(id, to string) circulation.Message {
	msg := mkIntentionMsg(to, circulation.ValueTypeMatter, "/ctx/src")
	msg.Intention.IntentionID = id
	msg.Intention.AwaitResponse = true
	return msg
}

// A message whose routing panics is answered with an internal error, without its
// cause, and the next message is routed as usual: the loop is not taken down.
func TestCommRoutingPanicAnswersTheSenderAndTheLoopGoesOn(t *testing.T) {
	reg := &panicOnResolveReg{mockCommReg: newMockCommReg(), boom: "/ctx/boom"}
	sender := make(chan junction.Delivery, 4)
	reg.chByID["/ctx/src"] = sender
	healthy := make(chan junction.Delivery, 4)
	reg.chByID["/ctx/dst"] = healthy
	l, _ := newPanicLoop(reg)

	bad := awaitedIntention("cp-1", "/ctx/boom")
	l.routeEgressSafely(bad, junction.MustLocalDelivery(junction.MustLocalAuthority("test:comm"), bad))

	got := recvMsg(t, sender, "answer to the sender")
	if got.Kind != circulation.ValueKindResponse || got.Response.IntentionID != "cp-1" {
		t.Fatalf("answer = %#v, want a response for cp-1", got)
	}
	if got.Response.Status != circulation.ValueStatusError || got.Response.Error == nil ||
		got.Response.Error.Code != circulation.ValueCodeInternal {
		t.Fatalf("answer = %#v, want an internal error", got.Response)
	}
	if strings.Contains(got.Response.Error.Message, "boom") {
		t.Fatalf("the cause leaked to the sender: %q", got.Response.Error.Message)
	}

	ok := awaitedIntention("cp-2", "/ctx/dst")
	l.routeEgressSafely(ok, junction.MustLocalDelivery(junction.MustLocalAuthority("test:comm"), ok))
	if next := recvMsg(t, healthy, "next message"); next.Intention.IntentionID != "cp-2" {
		t.Fatalf("the next message was not routed: %#v", next)
	}
}

// A panic while answering must not take the loop down either.
func TestCommAnswerToAPanicDoesNotPanicAgain(t *testing.T) {
	reg := &panicOnResolveReg{mockCommReg: newMockCommReg(), boom: "/ctx/boom"}
	l, _ := newPanicLoop(reg)
	msg := awaitedIntention("cp-3", "/ctx/boom")
	msg.Intention.From.Context = "/ctx/boom" // the answer goes to the address that panics
	l.routeEgressSafely(msg, junction.MustLocalDelivery(junction.MustLocalAuthority("test:comm"), msg))
}
