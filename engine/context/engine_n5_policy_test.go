/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package context

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strconv"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/manager"
	"brique_engine/security/policysrc"
	"brique_engine/security/refusal"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

// These tests run the two-instance harness under a policy that is NOT permissive:
// what instance B grants to instance A is chosen by the test, so that the decisions of
// the signed membrane (grant, refusal, reload, restart) are observed end to end over
// real signed HTTPS, not only in unit tests.

// n5Allow keeps, of the permissive grant, only the named capacities (family -> names).
func n5Allow(wanted map[string][]string) func(trust.Catalog) trust.PermissionSet {
	return func(catalog trust.Catalog) trust.PermissionSet {
		all := n5AllowAllPermissions(catalog)
		out := trust.PermissionSet{Capabilities: []trust.CapabilityPermission{}, Resources: all.Resources}
		for _, permission := range all.Capabilities {
			keep := map[string]bool{}
			for _, name := range wanted[permission.Family] {
				keep[name] = true
			}
			var names []string
			for _, name := range permission.Names {
				if keep[name] {
					names = append(names, name)
				}
			}
			if len(names) > 0 {
				permission.Names = names
				out.Capabilities = append(out.Capabilities, permission)
			}
		}
		return out
	}
}

var n5OnlyReadState = n5Allow(map[string][]string{"reflexive": {"read.state"}})

// callB sends one call from A to B's beta child and returns A's answer, or false if
// none comes within the window.
func (h *engineN5Harness) callB(t *testing.T, label, toType, toCap string, params map[string]any, window time.Duration) (circulation.Message, bool) {
	t.Helper()
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(label, "@ext_"+h.pubB+":/root/beta_child", toType, toCap, n5SinkAID, params))
	select {
	case out := <-h.sinkA:
		return out, true
	case <-time.After(window):
		return circulation.Message{}, false
	}
}

func (h *engineN5Harness) readState(t *testing.T, label string, window time.Duration) (circulation.Message, bool) {
	t.Helper()
	return h.callB(t, label, circulation.ValueTypeReflexive, "read.state", map[string]any{circulation.KeyInclude: []any{circulation.KeyContext}}, window)
}

func requireCapabilityDenied(t *testing.T, out circulation.Message, what string) {
	t.Helper()
	if out.Kind != circulation.ValueKindResponse || out.Response.Status != circulation.ValueStatusError || out.Response.Error == nil ||
		out.Response.Error.Message != refusal.MessageCapabilityDenied {
		t.Fatalf("%s: want the canonical capability refusal, got %#v error=%+v", what, out, out.Response.Error)
	}
}

func TestEngine_N5_POLICY_01_AGrantIsHonoredAndEverythingElseIsRefusedAlike(t *testing.T) {
	h := newEngineN5HarnessWith(t, n5OnlyReadState)

	out, ok := h.readState(t, "policy-granted", 5*time.Second)
	if !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("the granted capability was not served: %#v ok=%v", out, ok)
	}

	// An engine capacity that is not granted, a user capacity that exists but is not
	// granted, and a capacity that does not exist at all get the same answer.
	var answers []circulation.Message
	for label, call := range map[string]struct {
		kind, name string
	}{
		"policy-ungranted-engine": {circulation.ValueTypeReflexive, "read.structure"},
		"policy-ungranted-user":   {circulation.ValueTypeUser, "sandbox.sleep.echo"},
		"policy-nonexistent":      {circulation.ValueTypeUser, "no.such.capacity"},
	} {
		denied, ok := h.callB(t, label, call.kind, call.name, map[string]any{"message": "x", "delay_ms": 1}, 5*time.Second)
		if !ok {
			t.Fatalf("%s: no answer", label)
		}
		requireCapabilityDenied(t, denied, label)
		answers = append(answers, denied)
	}
	for _, other := range answers[1:] {
		if other.Response.Error.Code != answers[0].Response.Error.Code || other.Response.Error.Message != answers[0].Response.Error.Message ||
			other.Response.Error.Origin != answers[0].Response.Error.Origin {
			t.Fatalf("a refusal tells a missing capacity from an ungranted one: %+v vs %+v", other.Response.Error, answers[0].Response.Error)
		}
	}
}

func TestEngine_N5_POLICY_02_ReloadWhileACallIsInFlight(t *testing.T) {
	h := newEngineN5HarnessWith(t, nil) // permissive to begin with
	if out, ok := h.readState(t, "policy-before-reload", 5*time.Second); !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("baseline call failed: %#v ok=%v", out, ok)
	}

	// A slow call is admitted under the permissive policy ...
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call("policy-in-flight", "@ext_"+h.pubB+":/root/beta_child",
		circulation.ValueTypeUser, "sandbox.sleep.echo", n5SinkAID, map[string]any{"message": "admitted-before", "delay_ms": 1500}))
	time.Sleep(400 * time.Millisecond)

	// ... and while it runs B adopts a successor that only grants read.state.
	catalog, err := n5SecurityCatalog(h.rootBDir)
	if err != nil {
		t.Fatal(err)
	}
	h.kit.writeSuccessorForB(t, "2", h.kit.managerB.Runtime().Trust().Active().PolicyHash(), "active", n5OnlyReadState)
	if err := h.kit.managerB.Reload(catalog); err != nil {
		t.Fatalf("reload: %v", err)
	}

	// The call admitted before the reload finishes with its own answer ...
	select {
	case out := <-h.sinkA:
		if out.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("the call admitted before the reload was cut by it: %#v error=%+v", out, out.Response.Error)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("the in-flight call never answered")
	}
	// ... a new call to the same capacity is refused ...
	denied, ok := h.callB(t, "policy-after-reload", circulation.ValueTypeUser, "sandbox.sleep.echo", map[string]any{"message": "x", "delay_ms": 1}, 5*time.Second)
	if !ok {
		t.Fatal("no answer to the call made after the reload")
	}
	requireCapabilityDenied(t, denied, "a capacity removed by the reload")
	// ... and what is still granted still works.
	if out, ok := h.readState(t, "policy-granted-after-reload", 5*time.Second); !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("the capacity kept by the reload stopped working: %#v ok=%v", out, ok)
	}
}

func TestEngine_N5_POLICY_03_ADisabledPrincipalIsAnsweredNothing(t *testing.T) {
	h := newEngineN5HarnessWith(t, nil)
	if out, ok := h.readState(t, "policy-enabled", 5*time.Second); !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("baseline call failed: %#v ok=%v", out, ok)
	}
	catalog, err := n5SecurityCatalog(h.rootBDir)
	if err != nil {
		t.Fatal(err)
	}
	h.kit.writeSuccessorForB(t, "2", h.kit.managerB.Runtime().Trust().Active().PolicyHash(), "disabled", nil)
	if err := h.kit.managerB.Reload(catalog); err != nil {
		t.Fatalf("reload: %v", err)
	}
	// An unauthenticated sender gets no answer at all: nothing tells it whether the key,
	// the principal or the capacity was the problem.
	if out, ok := h.readState(t, "policy-disabled", 2*time.Second); ok {
		t.Fatalf("a disabled principal was answered: %#v", out)
	}
}

func TestEngine_N5_POLICY_04_ARestartedPeerRemembersWhatItAlreadyExecuted(t *testing.T) {
	h := newEngineN5Harness(t)
	if out, ok := h.readState(t, "policy-before-restart", 5*time.Second); !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("baseline call failed: %#v ok=%v", out, ok)
	}

	h.restartB(t)

	// The same intention identifier, freshly signed, is a conflict with what B already
	// executed: it is not run a second time and nothing is answered.
	if out, ok := h.readState(t, "policy-before-restart", 2*time.Second); ok {
		t.Fatalf("a restarted peer executed an intention it had already executed: %#v", out)
	}
	// A new intention works: the restart did not break the service.
	if out, ok := h.readState(t, "policy-after-restart", 5*time.Second); !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("the restarted peer does not serve: %#v ok=%v", out, ok)
	}
}

// restartB stops instance B and starts it again on the same directories: the same
// security directory (durable activation, replay journal), the same configuration.
func (h *engineN5Harness) restartB(t *testing.T) {
	t.Helper()
	h.rootB.Stop()
	if err := h.kit.managerB.Close(); err != nil {
		t.Fatalf("close manager B: %v", err)
	}
	regB := junction.NewContextCommRegistry()
	catalogB, err := n5SecurityCatalog(h.rootBDir)
	if err != nil {
		t.Fatal(err)
	}
	managerB, err := manager.Start(h.kit.options(h.kit.securityB), catalogB, regB)
	if err != nil {
		t.Fatalf("restart manager B: %v", err)
	}
	t.Cleanup(func() { _ = managerB.Close() })
	sinkB := make(chan circulation.Message, 16)
	registerContextTestSink(regB, shared.ContextAddr(n5SinkBID), sinkB, n5SinkBExt)
	rootB, err := NewContextLoop(h.rootBDir, shared.RootContextID, regB)
	if err != nil {
		t.Fatalf("restart root B: %v", err)
	}
	t.Cleanup(rootB.Stop)
	waitFor(t, 10*time.Second, func() bool { return rootB.State() == shared.ContextRunning }, "n5 root B running after restart")
	waitFor(t, 10*time.Second, func() bool {
		child := childLoop(rootB, "beta_child")
		return child != nil && child.State() == shared.ContextRunning
	}, "n5 beta child running after restart")
	h.rootB, h.regB, h.sinkB = rootB, regB, sinkB
	h.kit.managerB = managerB
}

func TestEngine_N5_POLICY_05_ARestartDuringACallIsNeverAnsweredAsSuccessNorRunTwice(t *testing.T) {
	h := newEngineN5Harness(t)
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call("policy-restart-in-flight", "@ext_"+h.pubB+":/root/beta_child",
		circulation.ValueTypeUser, "sandbox.sleep.echo", n5SinkAID, map[string]any{"message": "cut", "delay_ms": 3000}))
	time.Sleep(600 * time.Millisecond)
	h.restartB(t)

	// The waiting side is not restarted: the call that was cut is never turned into a
	// success by the restart.
	select {
	case out := <-h.sinkA:
		if out.Response.Status == circulation.ValueStatusOK {
			t.Fatalf("a call cut by the restart of its callee was answered as a success: %#v", out)
		}
	case <-time.After(6 * time.Second):
	}
	// The same intention again: the caller still holds it as pending and refuses a second
	// wait locally (so it does not even reach B), or B refuses it from its journal. Either
	// way it is not served a second time.
	if out, ok := h.callB(t, "policy-restart-in-flight", circulation.ValueTypeUser, "sandbox.sleep.echo", map[string]any{"message": "cut", "delay_ms": 1}, 3*time.Second); ok &&
		out.Response.Status == circulation.ValueStatusOK {
		t.Fatalf("the same intention was served again after the restart: %#v", out)
	}
	// The service itself came back.
	if out, ok := h.readState(t, "policy-restart-fresh", 5*time.Second); !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("the restarted peer does not serve: %#v ok=%v", out, ok)
	}
}

func TestEngine_N5_POLICY_06_RotatingTheCallersKeyDuringACallCutsOnlyNewCalls(t *testing.T) {
	h := newEngineN5HarnessWith(t, nil)
	if out, ok := h.readState(t, "policy-key-before", 5*time.Second); !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("baseline call failed: %#v ok=%v", out, ok)
	}
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call("policy-key-in-flight", "@ext_"+h.pubB+":/root/beta_child",
		circulation.ValueTypeUser, "sandbox.sleep.echo", n5SinkAID, map[string]any{"message": "admitted-before-rotation", "delay_ms": 1500}))
	time.Sleep(400 * time.Millisecond)

	// B replaces A's only key by another one A does not hold.
	newPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := n5SecurityCatalog(h.rootBDir)
	if err != nil {
		t.Fatal(err)
	}
	writeN5GenerationSpec(t, n5GenerationSpec{
		securityDir: h.kit.securityB, rootDir: h.kit.rootBDir, instanceID: "instance-b", remoteID: "instance-a", target: h.kit.targetA,
		messagePrivate: h.kit.privateB, remotePublic: newPublic, authority: h.kit.authority, authorityPrivate: h.kit.authorityPrivate,
		generation: "g2", revision: "2", previousHash: h.kit.managerB.Runtime().Trust().Active().PolicyHash(),
	})
	if err := h.kit.managerB.Reload(catalog); err != nil {
		t.Fatalf("reload: %v", err)
	}

	select {
	case out := <-h.sinkA:
		if out.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("the call admitted before the rotation was cut by it: %#v error=%+v", out, out.Response.Error)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("the in-flight call never answered")
	}
	// The old key now authenticates nobody: no answer, and nothing tells why.
	if out, ok := h.readState(t, "policy-key-after", 2*time.Second); ok {
		t.Fatalf("a revoked key was answered: %#v", out)
	}
}

// A burst of signed calls from one principal: each call gets exactly one terminal answer,
// either its result or the canonical "unavailable" back-pressure (a full queue, the peer refusing the delivery, a full response-binding table: the code is the same), never silence and never
// a duplicate, and the service is the same afterwards. (What one principal can hold in the
// replay ledger is bounded separately, in security/replay.)
func TestEngine_N5_POLICY_07_ABurstFromOnePrincipalGetsOneTerminalAnswerEach(t *testing.T) {
	n5Limits = map[string]*trust.Limits{"instance-b": {Unlimited: true}}
	defer func() { n5Limits = nil }()
	h := newEngineN5HarnessWith(t, n5OnlyReadState)
	const burst = 150
	// The sink holds 16 messages, and the engine does not drop an answer for a consumer that is
	// full: the sender of an answer waits. A consumer that reads only after it has sent all its
	// calls is therefore stopped by its own backlog, and the test would depend on how fast the
	// answers come back. This one reads while it sends, as a real consumer does.
	answers := make(chan circulation.Message, 2*burst)
	stopReading := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case out := <-h.sinkA:
				answers <- out
			case <-stopReading:
				return
			}
		}
	}()
	for i := 0; i < burst; i++ {
		label := "policy-burst-" + strconv.Itoa(i)
		sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(label, "@ext_"+h.pubB+":/root/beta_child",
			circulation.ValueTypeReflexive, "read.state", n5SinkAID, map[string]any{circulation.KeyInclude: []any{circulation.KeyContext}}))
	}
	deadline := time.After(40 * time.Second)
	seen := map[string]int{}
	served, backpressure := 0, 0
	reasons := map[string]int{}
	for len(seen) < burst {
		select {
		case out := <-answers:
			seen[out.Response.IntentionID]++
			switch {
			case out.Response.Status == circulation.ValueStatusOK:
				served++
			case out.Response.Error != nil && out.Response.Error.Code == "unavailable":
				backpressure++
				reasons[fmt.Sprint(out.Response.Error.Origin, "/", out.Response.Error.Details["reason"])]++
			default:
				t.Fatalf("a call of the burst failed for another reason: %+v", out.Response.Error)
			}
		case <-deadline:
			t.Fatalf("only %d of %d calls got an answer (%d served, %d back-pressure)", len(seen), burst, served, backpressure)
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("%s answered %d times", id, n)
		}
	}
	if served == 0 {
		t.Fatal("nothing of the burst was served")
	}
	t.Logf("burst of %d: %d served, %d refused by back-pressure %v", burst, served, backpressure, reasons)
	// No second answer to any call, whether it reached the reader or is still in the sink.
	time.Sleep(300 * time.Millisecond)
	close(stopReading)
	<-readerDone
	select {
	case extra := <-answers:
		t.Fatalf("an answer came twice: %+v", extra.Response)
	default:
	}
	recvNoN5Msg(t, h.sinkA, 50*time.Millisecond, "no duplicate after the burst")
	if out, ok := h.readState(t, "policy-after-burst", 5*time.Second); !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("the service changed after the burst: %#v ok=%v", out, ok)
	}
}

// The same signed membrane under a policy written as an access source (format 2: verbs,
// subtrees and exceptions) rather than generated names: B grants A the right to read
// the beta child and to call a capacity there, except what is excluded.
func n5VerbSource(grants ...policysrc.Grant) func(string) *policysrc.Source {
	return func(publicKey string) *policysrc.Source {
		return &policysrc.Source{Format: policysrc.Format, Version: policysrc.Version, InstanceID: "instance-b", PolicyID: "main",
			Principals: map[string]*policysrc.Principal{"instance-a": {Kind: "instance", Encryption: policysrc.EncryptionNone, Keys: []policysrc.Key{{PublicKey: publicKey}}, Grants: grants}}}
	}
}

func TestEngine_N5_POLICY_08_AVerbPolicyIsHonoredEndToEnd(t *testing.T) {
	h := newEngineN5HarnessSource(t, n5VerbSource(policysrc.Grant{Read: policysrc.TargetList{"/root/beta_child/**"}}))
	// read on the subtree opens the read-class capacities there ...
	if out, ok := h.readState(t, "policy-verb-read", 5*time.Second); !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("read.state is not served under read on the subtree: %#v ok=%v", out, ok)
	}
	// ... but none of write, define, control or a user capacity.
	for label, call := range map[string]struct{ kind, name string }{
		"policy-verb-define":  {circulation.ValueTypeReflexive, "edit.create"},
		"policy-verb-control": {circulation.ValueTypeReflexive, "meaning.rebuild"},
		"policy-verb-user":    {circulation.ValueTypeUser, "sandbox.sleep.echo"},
	} {
		denied, ok := h.callB(t, label, call.kind, call.name, map[string]any{"message": "x", "delay_ms": 1}, 5*time.Second)
		if !ok {
			t.Fatalf("%s: no answer", label)
		}
		requireCapabilityDenied(t, denied, label)
	}
}

func TestEngine_N5_POLICY_09_AVerbPolicyReloadedWidensThenNarrowsTheGrant(t *testing.T) {
	h := newEngineN5HarnessSource(t, n5VerbSource(policysrc.Grant{Read: policysrc.TargetList{"/root/beta_child/**"}}))
	catalog, err := n5SecurityCatalog(h.rootBDir)
	if err != nil {
		t.Fatal(err)
	}
	reload := func(revision string, source func(string) *policysrc.Source) {
		t.Helper()
		writeN5GenerationSpec(t, n5GenerationSpec{
			securityDir: h.kit.securityB, rootDir: h.kit.rootBDir, instanceID: "instance-b", remoteID: "instance-a", target: h.kit.targetA,
			messagePrivate: h.kit.privateB, remotePublic: h.kit.publicA, authority: h.kit.authority, authorityPrivate: h.kit.authorityPrivate,
			generation: "g" + revision, revision: revision, previousHash: h.kit.managerB.Runtime().Trust().Active().PolicyHash(), source: source,
		})
		if err := h.kit.managerB.Reload(catalog); err != nil {
			t.Fatalf("reload %s: %v", revision, err)
		}
	}
	// Adding the read exception on the subtree takes the capacity away at once ...
	reload("2", n5VerbSource(policysrc.Grant{Read: policysrc.TargetList{"/root/beta_child/**"}, Except: policysrc.TargetList{"/root/beta_child"}}))
	denied, ok := h.readState(t, "policy-verb-excluded", 5*time.Second)
	if !ok {
		t.Fatal("no answer after the exception")
	}
	// A context the caller may not read is answered exactly like one that is absent (D06).
	if denied.Response.Status != circulation.ValueStatusError || denied.Response.Error == nil ||
		denied.Response.Error.Code != "not_found" || len(denied.Response.Payload) != 0 {
		t.Fatalf("an excluded context is not answered as absent: %#v error=%+v", denied, denied.Response.Error)
	}
	// ... and removing it gives it back.
	reload("3", n5VerbSource(policysrc.Grant{Read: policysrc.TargetList{"/root/beta_child/**"}}))
	if out, ok := h.readState(t, "policy-verb-restored", 5*time.Second); !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("the capacity did not come back: %#v ok=%v", out, ok)
	}
}

// A full key rotation: the callee adopts the caller's new key, the caller adopts its new
// signing key. Between the two steps the caller is unauthenticated (silent); afterwards the
// new key is served, and the old one stays refused.
func TestEngine_N5_POLICY_10_ANewCallerKeyIsServedOnceBothSidesRotated(t *testing.T) {
	h := newEngineN5HarnessWith(t, nil)
	if out, ok := h.readState(t, "policy-rotate-before", 5*time.Second); !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("baseline call failed: %#v ok=%v", out, ok)
	}
	newPublic, newPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	catalogA, err := n5SecurityCatalog(h.rootADir)
	if err != nil {
		t.Fatal(err)
	}
	catalogB, err := n5SecurityCatalog(h.rootBDir)
	if err != nil {
		t.Fatal(err)
	}

	// Step 1: B trusts the new key only.
	writeN5GenerationSpec(t, n5GenerationSpec{
		securityDir: h.kit.securityB, rootDir: h.kit.rootBDir, instanceID: "instance-b", remoteID: "instance-a", target: h.kit.targetA,
		messagePrivate: h.kit.privateB, remotePublic: newPublic, authority: h.kit.authority, authorityPrivate: h.kit.authorityPrivate,
		generation: "g2", revision: "2", previousHash: h.kit.managerB.Runtime().Trust().Active().PolicyHash(),
	})
	if err := h.kit.managerB.Reload(catalogB); err != nil {
		t.Fatalf("reload B: %v", err)
	}
	if out, ok := h.readState(t, "policy-rotate-half", 2*time.Second); ok {
		t.Fatalf("the old key was answered after only the callee rotated: %#v", out)
	}

	// Step 2: A signs with the new key (its own policy is unchanged: it still trusts B's key).
	writeN5GenerationSpec(t, n5GenerationSpec{
		securityDir: h.kit.securityA, rootDir: h.kit.rootADir, instanceID: "instance-a", remoteID: "instance-b", target: h.kit.targetB,
		messagePrivate: newPrivate, remotePublic: h.kit.publicB, authority: h.kit.authority, authorityPrivate: h.kit.authorityPrivate,
		generation: "g2", revision: "2", previousHash: h.kit.managerA.Runtime().Trust().Active().PolicyHash(),
	})
	if err := h.kit.managerA.Reload(catalogA); err != nil {
		t.Fatalf("reload A: %v", err)
	}
	if out, ok := h.readState(t, "policy-rotate-after", 5*time.Second); !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("the new key is not served once both sides rotated: %#v ok=%v", out, ok)
	}
}

// restartA stops instance A (the caller) and starts it again on the same directories.
func (h *engineN5Harness) restartA(t *testing.T) {
	t.Helper()
	h.rootA.Stop()
	if err := h.kit.managerA.Close(); err != nil {
		t.Fatalf("close manager A: %v", err)
	}
	regA := junction.NewContextCommRegistry()
	catalogA, err := n5SecurityCatalog(h.rootADir)
	if err != nil {
		t.Fatal(err)
	}
	managerA, err := manager.Start(h.kit.options(h.kit.securityA), catalogA, regA)
	if err != nil {
		t.Fatalf("restart manager A: %v", err)
	}
	t.Cleanup(func() { _ = managerA.Close() })
	sinkA := make(chan circulation.Message, 16)
	registerContextTestSink(regA, shared.ContextAddr(n5SinkAID), sinkA, n5SinkAExt)
	rootA, err := NewContextLoop(h.rootADir, shared.RootContextID, regA)
	if err != nil {
		t.Fatalf("restart root A: %v", err)
	}
	t.Cleanup(rootA.Stop)
	waitFor(t, 10*time.Second, func() bool { return rootA.State() == shared.ContextRunning }, "n5 root A running after restart")
	h.rootA, h.regA, h.sinkA = rootA, regA, sinkA
	h.kit.managerA = managerA
}

func TestEngine_N5_POLICY_11_ARestartOfTheCallerDuringACallReportsResponseStateLostAndDoesNotReplay(t *testing.T) {
	h := newEngineN5Harness(t)
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call("policy-caller-restart", "@ext_"+h.pubB+":/root/beta_child",
		circulation.ValueTypeUser, "sandbox.sleep.echo", n5SinkAID, map[string]any{"message": "late", "delay_ms": 2000}))
	time.Sleep(500 * time.Millisecond)
	oldSink := h.sinkA
	h.restartA(t)

	// The wait that was open is ended with the documented, honest outcome: the remote
	// operation may have completed, the caller must not assume either way.
	select {
	case out := <-oldSink:
		if out.Response.Status != circulation.ValueStatusError || out.Response.Error == nil ||
			out.Response.Error.Details["reason"] != "response_state_lost" {
			t.Fatalf("the wait ended with something else than response_state_lost: %#v error=%+v", out, out.Response.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait cut by the restart was never ended")
	}
	// B finishes and answers an instance that no longer waits for that call: the answer
	// is not delivered to the new instance.
	select {
	case out := <-h.sinkA:
		t.Fatalf("the restarted caller received the answer of a call it no longer waits for: %#v", out)
	case <-time.After(3 * time.Second):
	}
	// The same intention again is not run a second time (B holds it as done) ...
	if out, ok := h.callB(t, "policy-caller-restart", circulation.ValueTypeUser, "sandbox.sleep.echo", map[string]any{"message": "late", "delay_ms": 1}, 3*time.Second); ok &&
		out.Response.Status == circulation.ValueStatusOK {
		t.Fatalf("the same intention was served again after the caller restarted: %#v", out)
	}
	// ... and the restarted caller calls normally with a new one.
	if out, ok := h.readState(t, "policy-caller-fresh", 5*time.Second); !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("the restarted caller does not call: %#v ok=%v", out, ok)
	}
}

// N5_SEAL: content sealed to the recipient's key, signed by the sender, over the real
// signed HTTPS between two engines.

func TestEngine_N5_SEAL_01_SealedBothWaysDeliversTheCallAndItsAnswer(t *testing.T) {
	h := newEngineN5HarnessContent(t, trust.ContentModeHPKE, trust.ContentModeHPKE)
	out, ok := h.callB(t, "sealed-read", "reflexive", "read.state", nil, 15*time.Second)
	if !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("a call sealed to B was not answered: ok=%v %+v", ok, out.Response)
	}
}

// B requires sealed content from A, A's policy does not seal: the signature is good, the
// message is refused for what it is not, and A is told nothing but silence.
func TestEngine_N5_SEAL_02_AnUnsealedMessageIsRefusedWhereSealingIsRequired(t *testing.T) {
	h := newEngineN5HarnessContent(t, trust.ContentModeClear, trust.ContentModeHPKE)
	if out, ok := h.callB(t, "clear-read", "reflexive", "read.state", nil, 4*time.Second); ok && out.Response.Status == circulation.ValueStatusOK {
		t.Fatalf("a clear message was served where sealing is required: %+v", out.Response)
	}
}

// A seals to B, B accepts either: sealed content is opened with B's key and answered sealed.
func TestEngine_N5_SEAL_03_SealedContentIsAcceptedWhereClearIsToo(t *testing.T) {
	h := newEngineN5HarnessContent(t, trust.ContentModeHPKE, trust.ContentModeClear)
	out, ok := h.callB(t, "sealed-read", "reflexive", "read.state", nil, 15*time.Second)
	if !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("a sealed call to a peer that accepts clear was not answered: ok=%v %+v", ok, out.Response)
	}
}

// N5_RATE: a principal that babbles is slowed to the rate its policy gives it, and told so.

// answers sends n calls from A to B at once and counts what comes back.
func (h *engineN5Harness) burstToB(t *testing.T, prefix string, n int) (served, unavailable int) {
	t.Helper()
	for i := 0; i < n; i++ {
		sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(prefix+strconv.Itoa(i), "@ext_"+h.pubB+":/root/beta_child",
			circulation.ValueTypeReflexive, "read.state", n5SinkAID, map[string]any{circulation.KeyInclude: []any{circulation.KeyContext}}))
	}
	deadline := time.After(30 * time.Second)
	for got := 0; got < n; got++ {
		select {
		case out := <-h.sinkA:
			if out.Response.Status == circulation.ValueStatusOK {
				served++
			} else if out.Response.Error != nil && out.Response.Error.Code == "unavailable" {
				unavailable++
			} else {
				t.Fatalf("unexpected answer: %+v", out.Response.Error)
			}
		case <-deadline:
			t.Fatalf("only %d of %d calls got an answer", got, n)
		}
	}
	return served, unavailable
}

func TestEngine_N5_RATE_01_ABabblingPrincipalIsSlowedAndToldSo(t *testing.T) {
	h := newEngineN5HarnessLimits(t, &trust.Limits{RatePerSecond: 2, Burst: 5})
	served, unavailable := h.burstToB(t, "rate-", 40)
	// 5 in the burst, plus what refills while the burst is sent (a couple of seconds at most).
	if served < 5 || served > 15 {
		t.Fatalf("%d of 40 served under a rate of 2 per second with a burst of 5", served)
	}
	if served+unavailable != 40 || unavailable == 0 {
		t.Fatalf("every call must be answered, and the excess refused: %d served, %d refused", served, unavailable)
	}
}

func TestEngine_N5_RATE_02_UnlimitedMustBeWrittenToLiftTheLimit(t *testing.T) {
	limited := newEngineN5HarnessLimits(t, nil)
	servedByDefault, _ := limited.burstToB(t, "default-", 120)
	if servedByDefault >= 120 {
		t.Fatalf("a principal with no setting was not limited: %d of 120 served", servedByDefault)
	}
	if servedByDefault < trust.DefaultNetworkBurst {
		t.Fatalf("the default burst of %d was not honored: %d served", trust.DefaultNetworkBurst, servedByDefault)
	}
}

// N5_MACH: a peer reached on named machines, each with its own keys.

// callBOn sends one call from A to the machine of B with this name ("" for the default).
func (h *engineN5Harness) callBOn(t *testing.T, label, machine string, window time.Duration) (circulation.Message, bool) {
	t.Helper()
	address := "@ext_" + h.pubB
	if machine != "" {
		address += "~" + machine
	}
	sendToN5Context(t, h.regA, shared.RootContextID, mkN5Call(label, address+":/root/beta_child",
		circulation.ValueTypeReflexive, "read.state", n5SinkAID, nil))
	select {
	case out := <-h.sinkA:
		return out, true
	case <-time.After(window):
		return circulation.Message{}, false
	}
}

func TestEngine_N5_MACH_01_ACallGoesToTheDefaultOrToANamedMachine(t *testing.T) {
	h := newEngineN5HarnessMachines(t, func(realKey, otherKey, target string) []trust.PeerEndpoint {
		return []trust.PeerEndpoint{{Name: "main", Target: target, KeyIDs: []string{realKey}}}
	})
	for _, machine := range []string{"", "main"} {
		out, ok := h.callBOn(t, "mach-"+machine, machine, 15*time.Second)
		if !ok || out.Response.Status != circulation.ValueStatusOK {
			t.Fatalf("a call to machine %q was not answered: ok=%v %+v", machine, ok, out.Response)
		}
	}
	// A machine that is not declared is refused at once, and nothing is sent.
	out, ok := h.callBOn(t, "mach-nowhere", "nowhere", 5*time.Second)
	if !ok || out.Response.Status == circulation.ValueStatusOK {
		t.Fatalf("a machine that does not exist was served: ok=%v %+v", ok, out.Response)
	}
}

// The answer must be signed by a key of the machine that was asked: B's real key is not one of
// the keys A bound to machine "one", so what B answers there is not accepted.
func TestEngine_N5_MACH_02_AnAnswerFromAnotherMachinesKeyIsRefused(t *testing.T) {
	h := newEngineN5HarnessMachines(t, func(realKey, otherKey, target string) []trust.PeerEndpoint {
		return []trust.PeerEndpoint{
			{Name: "one", Target: target, KeyIDs: []string{otherKey}},
			{Name: "two", Target: target, KeyIDs: []string{realKey}},
		}
	})
	if out, ok := h.callBOn(t, "mach-two", "two", 15*time.Second); !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("the machine whose key B holds was not served: ok=%v %+v", ok, out.Response)
	}
	if out, ok := h.callBOn(t, "mach-one", "one", 4*time.Second); ok && out.Response.Status == circulation.ValueStatusOK {
		t.Fatalf("an answer signed by a key of another machine was accepted: %+v", out.Response)
	}
}

// Sealing follows the machine: the message is sealed to the key bound to the machine asked, and B,
// which holds that key, opens it. The same message sealed to a key B does not hold is not served.
func TestEngine_N5_MACH_03_AMessageIsSealedToTheKeyOfItsMachine(t *testing.T) {
	n5ContentMode = map[string]string{"instance-a": trust.ContentModeHPKE, "instance-b": trust.ContentModeHPKE}
	t.Cleanup(func() { n5ContentMode = nil })
	h := newEngineN5HarnessMachines(t, func(realKey, otherKey, target string) []trust.PeerEndpoint {
		return []trust.PeerEndpoint{
			{Name: "here", Target: target, KeyIDs: []string{realKey}},
			{Name: "elsewhere", Target: target, KeyIDs: []string{otherKey}},
		}
	})
	if out, ok := h.callBOn(t, "mach-here", "here", 15*time.Second); !ok || out.Response.Status != circulation.ValueStatusOK {
		t.Fatalf("a message sealed to the key B holds was not served: ok=%v %+v", ok, out.Response)
	}
	if out, ok := h.callBOn(t, "mach-elsewhere", "elsewhere", 4*time.Second); ok && out.Response.Status == circulation.ValueStatusOK {
		t.Fatalf("a message sealed to a key B does not hold was served: %+v", out.Response)
	}
}
