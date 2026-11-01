package execution_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/security/admission"
	"brique_engine/security/catalog"
	"brique_engine/security/manager"
	"brique_engine/security/refusal"
	"brique_engine/security/secadmin"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

const (
	callerRightsChainCap   = "sandbox.dsl.chain"
	callerRightsOutsideCap = "sandbox.dsl.outside"
	callerRightsEchoCap    = "sandbox.dsl.echo"
)

// writeChainDSL adds a DSL capacity of the workspace whose single step calls the
// reflexive read.state of its own context (it needs the read verb there).
func writeChainDSL(t *testing.T, rootDir string) {
	t.Helper()
	writeStateDSL(t, rootDir, callerRightsChainCap, n6ExecutionWorkspaceID)
	// A step implemented by the Python wrapper: it travels with the delegation handle.
	writeInvokeDSL(t, rootDir, callerRightsEchoCap, map[string]any{
		"@capacity": n6ExecutionWorkspaceID + "/sandbox.py.echo", "as": "e", "with": map[string]any{"message": "chained"},
	})
	// The same step aimed at the parent context, outside the capacity's subtree.
	writeStateDSL(t, rootDir, callerRightsOutsideCap, "/root")
}

func writeStateDSL(t *testing.T, rootDir, capName, target string) {
	t.Helper()
	writeInvokeDSL(t, rootDir, capName, map[string]any{
		"@capacity": target + "/read.state", "to_type": "reflexive", "as": "e",
		"with": map[string]any{"include": []any{"context"}},
	})
}

func writeInvokeDSL(t *testing.T, rootDir, capName string, invoke map[string]any) {
	t.Helper()
	doc := map[string]any{
		"brique": map[string]any{"cap_name": capName, "kind": "dsl"},
		"functional": map[string]any{"#root": map[string]any{
			"role": "call the echo capacity", "inputs": map[string]any{}, "outputs": map[string]any{}, "effects": map[string]any{},
			"transformation_contract": map[string]any{"from": []any{}, "to": []any{}, "affecting": []any{}, "morphing": "chain"},
			"resolution": map[string]any{">sequence": map[string]any{"items": []any{
				map[string]any{"invoke": invoke},
			}}},
		}},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(rootDir, "workspace", "capacity", capName+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

type callerRightsEngine struct {
	h       *engineN6ExecutionHarness
	runtime *trust.Runtime
	dir     *secadmin.Dir
	manager *manager.Manager
}

// startCallerRightsEngine runs the real sandbox engine under a v2 policy where the
// restricted local principal local.tester holds the given grants ("verb target").
func startCallerRightsEngine(t *testing.T, grants ...string) callerRightsEngine {
	t.Helper()
	return startCallerRightsEngineWith(t, func(d *secadmin.Dir) {
		for _, grant := range grants {
			verb, target, _ := strings.Cut(grant, " ")
			if _, err := d.GrantAdd("local.tester", verb, []string{target}, nil); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// startCallerRightsEngineWith is startCallerRightsEngine with the policy written
// by setup, on top of the restricted principal local.tester.
func startCallerRightsEngineWith(t *testing.T, setup func(*secadmin.Dir)) callerRightsEngine {
	t.Helper()
	return startCallerRightsEngineAs(t, "", setup)
}

// startCallerRightsEngineAs is startCallerRightsEngineWith with the wrapper_auth mode of
// the root listener set ("" leaves the engine default).
func startCallerRightsEngineAs(t *testing.T, wrapperAuth string, setup func(*secadmin.Dir)) callerRightsEngine {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}
	if _, err := exec.LookPath("c++"); err != nil {
		t.Skipf("c++ unavailable: %v", err)
	}
	rootDir := copyN6ExecutionSandboxRoot(t)
	patchN6ExecutionSandboxForRuntime(t, rootDir)
	writeChainDSL(t, rootDir)
	if wrapperAuth != "" {
		setRootWrapperAuth(t, rootDir, wrapperAuth)
	}

	dir := &secadmin.Dir{
		Root: filepath.Join(t.TempDir(), "security"),
		Now:  func() time.Time { return time.Now().UTC() },
	}
	if _, err := dir.Init(secadmin.InitOptions{InstanceID: "n6-instance"}); err != nil {
		t.Fatal(err)
	}
	reg := junction.NewContextCommRegistry()
	policyCatalog := trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: catalog.ResourceActions()}
	m, err := manager.Start(manager.Options{
		SecurityDirectory: dir.Root, InitialGeneration: "g000001", RequireReadOnlyKeyFiles: true,
	}, policyCatalog, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if err := dir.PrincipalAdd("local.tester", "local", ""); err != nil {
		t.Fatal(err)
	}
	setup(dir)
	if _, err := dir.Apply(secadmin.ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(policyCatalog); err != nil {
		t.Fatal(err)
	}
	return callerRightsEngine{h: startEngineN6ExecutionHarnessWithRegistry(t, rootDir, reg), runtime: m.Runtime().Trust(), dir: dir, manager: m}
}

// callAsTester sends one intention admitted as the restricted local principal.
func (e callerRightsEngine) callAsTester(t *testing.T, id, capName string) circulation.Response {
	t.Helper()
	msg := mkEngineN6ExecutionIntention(id, n6ExecutionWorkspaceID, capName, circulation.ValueTypeUser, map[string]any{"message": "direct"})
	msg.Intention.AwaitResponse = true
	adm, err := e.runtime.AdmitLocal("local.tester")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := admission.NewRestrictedLocal(adm, "tester", msg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := junction.AdmitRestrictedLocal(msg, ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The injection stands for Communication's ingress, which records who the root
	// intention was admitted for (covered by the Comm tests).
	e.h.reg.Provenance().Record(id, ctx.PrincipalID(), ctx.ActorID())
	ch, ok := e.h.reg.ResolveCh(shared.ContextAddr(n6ExecutionWorkspaceID))
	if !ok {
		t.Fatal("workspace channel not found")
	}
	ch <- delivery
	resp := recvEngineN6ExecutionMsg(t, e.h.sinkCh, id+" response")
	if resp.Kind != circulation.ValueKindResponse {
		t.Fatalf("kind=%q want response", resp.Kind)
	}
	return resp.Response
}

// D13 end to end: the steps of a DSL capacity are decided against the caller's
// rights. Holding the DSL capacity alone is not enough; holding the step too is.
func TestEngine_N6_EXEC_CallerRightsGovernDSLSteps(t *testing.T) {
	t.Run("the caller holds only the DSL capacity: its step is refused", func(t *testing.T) {
		e := startCallerRightsEngine(t, "call "+n6ExecutionWorkspaceID+"#capacity:"+callerRightsChainCap)
		resp := e.callAsTester(t, "n6-rights-dsl-only", callerRightsChainCap)
		if resp.Status != circulation.ValueStatusError || resp.Error == nil || resp.Error.Message != refusal.MessageCapabilityDenied {
			t.Fatalf("want the canonical capability refusal, got %#v / %#v", resp.Payload, resp.Error)
		}
	})
	t.Run("the caller holds the DSL capacity and its step: it runs", func(t *testing.T) {
		e := startCallerRightsEngine(t, "call "+n6ExecutionWorkspaceID+"#capacity:"+callerRightsChainCap, "read "+n6ExecutionWorkspaceID)
		resp := e.callAsTester(t, "n6-rights-dsl-and-step", callerRightsChainCap)
		if resp.Status != circulation.ValueStatusOK {
			t.Fatalf("status=%q error=%#v payload=%#v", resp.Status, resp.Error, resp.Payload)
		}
		if payload := mustExecutionPayloadMap(t, resp.Payload); payload[circulation.KeyContext] == nil {
			t.Fatalf("the step did not return the context state: %#v", payload)
		}
	})
}

// D24 end to end: a façade runs as its owner, bounded to the capacity's context.
// The caller holds no read right of its own; the capacity reads for it inside its
// subtree and cannot be made to read outside it.
func TestEngine_N6_EXEC_AsOwnerFacade(t *testing.T) {
	facade := func(t *testing.T, owner bool) callerRightsEngine {
		return startCallerRightsEngineWith(t, func(d *secadmin.Dir) {
			if err := d.PrincipalAdd("local.owner", "local", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := d.GrantAdd("local.owner", "read", []string{"/root/**"}, nil); err != nil {
				t.Fatal(err)
			}
			asOwner := ""
			if owner {
				asOwner = "local.owner"
			}
			for _, name := range []string{callerRightsChainCap, callerRightsOutsideCap} {
				if _, err := d.GrantAddAsOwner("local.tester", "", "call", []string{n6ExecutionWorkspaceID + "#capacity:" + name}, nil, asOwner); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	t.Run("without as_owner the caller's missing read right refuses the step", func(t *testing.T) {
		e := facade(t, false)
		resp := e.callAsTester(t, "n6-owner-without", callerRightsChainCap)
		if resp.Status != circulation.ValueStatusError {
			t.Fatalf("status=%q payload=%#v", resp.Status, resp.Payload)
		}
	})
	t.Run("with as_owner the capacity reads for a caller who cannot", func(t *testing.T) {
		e := facade(t, true)
		resp := e.callAsTester(t, "n6-owner-with", callerRightsChainCap)
		if resp.Status != circulation.ValueStatusOK {
			t.Fatalf("status=%q error=%#v payload=%#v", resp.Status, resp.Error, resp.Payload)
		}
		if payload := mustExecutionPayloadMap(t, resp.Payload); payload[circulation.KeyContext] == nil {
			t.Fatalf("the step did not return the context state: %#v", payload)
		}
	})
	t.Run("the owner's authority stops at the capacity's context", func(t *testing.T) {
		e := facade(t, true)
		resp := e.callAsTester(t, "n6-owner-outside", callerRightsOutsideCap)
		if resp.Status != circulation.ValueStatusError {
			t.Fatalf("the façade read outside its context: payload=%#v", resp.Payload)
		}
	})
}

// With the real, current Python wrapper library: the delegation handle carries the
// caller across a wrapper, directly and through a DSL step.
func TestEngine_N6_EXEC_CallerRightsAcrossARealPythonWrapper(t *testing.T) {
	grant := func(names ...string) []string {
		var out []string
		for _, name := range names {
			out = append(out, "call "+n6ExecutionWorkspaceID+"#capacity:"+name)
		}
		return out
	}
	t.Run("direct call of the wrapper capacity", func(t *testing.T) {
		e := startCallerRightsEngine(t, grant("sandbox.py.echo")...)
		resp := e.callAsTester(t, "n6-wrapper-direct", "sandbox.py.echo")
		if resp.Status != circulation.ValueStatusOK {
			t.Fatalf("status=%q error=%#v", resp.Status, resp.Error)
		}
		if payload := mustExecutionPayloadMap(t, resp.Payload); payload["echo"] != "direct" {
			t.Fatalf("payload=%#v", payload)
		}
	})
	t.Run("a DSL step served by the wrapper, caller holds both", func(t *testing.T) {
		e := startCallerRightsEngine(t, grant(callerRightsEchoCap, "sandbox.py.echo")...)
		resp := e.callAsTester(t, "n6-wrapper-dsl", callerRightsEchoCap)
		if resp.Status != circulation.ValueStatusOK {
			t.Fatalf("status=%q error=%#v", resp.Status, resp.Error)
		}
		if payload := mustExecutionPayloadMap(t, resp.Payload); payload["echo"] != "chained" {
			t.Fatalf("payload=%#v", payload)
		}
	})
	t.Run("a DSL step served by the wrapper, caller holds only the DSL", func(t *testing.T) {
		e := startCallerRightsEngine(t, grant(callerRightsEchoCap)...)
		resp := e.callAsTester(t, "n6-wrapper-dsl-denied", callerRightsEchoCap)
		if resp.Status != circulation.ValueStatusError {
			t.Fatalf("the wrapper step ran without the caller's right: %#v", resp.Payload)
		}
	})
}

// D12 end to end: the trace events of a restricted caller's request carry its
// principal, including those of the DSL step and of the wrapper call it caused.
func TestEngine_N6_EXEC_ProvenanceTraversesDSLAndWrapper(t *testing.T) {
	t.Setenv("BRIQUE_N6_EXECUTION_TRACE_EAGER", "1")
	e := startCallerRightsEngine(t,
		"call "+n6ExecutionWorkspaceID+"#capacity:"+callerRightsEchoCap,
		"call "+n6ExecutionWorkspaceID+"#capacity:sandbox.py.echo")
	const rootID = "n6-provenance-root"
	if resp := e.callAsTester(t, rootID, callerRightsEchoCap); resp.Status != circulation.ValueStatusOK {
		t.Fatalf("status=%q error=%#v", resp.Status, resp.Error)
	}
	// An unrelated local call leaves no provenance.
	if resp := callN6ExecutionUserCap(t, e.h, n6ExecutionWorkspaceID, "n6-provenance-local", "sandbox.py.echo", map[string]any{"message": "x"}); resp.Status != circulation.ValueStatusOK {
		t.Fatalf("local call: %#v", resp.Error)
	}

	var events []any
	waitForExecution(t, 5*time.Second, func() bool {
		resp := callN6ExecutionReflexiveCap(t, e.h, n6ExecutionWorkspaceID, "n6-provenance-inspect-"+time.Now().Format("150405.000000"), "trace.inspect", map[string]any{
			circulation.KeyMode:    circulation.ValueModeEvents,
			circulation.KeyLimit:   200,
			circulation.KeyOrderBy: "ts_ns ASC",
			circulation.KeyFilters: map[string]any{circulation.KeyPrincipal: "local.tester"},
			circulation.KeyTimeRange: map[string]any{
				circulation.KeyFromTsNs: time.Now().Add(-time.Minute).UnixNano(),
				circulation.KeyToTsNs:   time.Now().Add(5 * time.Second).UnixNano(),
			},
		})
		if resp.Status != circulation.ValueStatusOK {
			return false
		}
		events = mustExecutionPayloadArray(t, mustExecutionPayloadMap(t, resp.Payload)[circulation.KeyEvents])
		for _, raw := range events {
			if ev, _ := raw.(map[string]any); ev != nil && ev[circulation.KeyIntentionId] != rootID {
				return true // an event of a derived call (the DSL step) is stamped too
			}
		}
		return false
	}, "trace events stamped with the caller's principal")

	for _, raw := range events {
		ev := raw.(map[string]any)
		if ev[circulation.KeyPrincipal] != "local.tester" {
			t.Fatalf("filtered event without the principal: %#v", ev)
		}
		root, _ := ev[circulation.KeyRootIntentionId].(string)
		if id, _ := ev[circulation.KeyIntentionId].(string); id != rootID && root != rootID {
			t.Fatalf("an event outside the caller's chain is stamped: %#v", ev)
		}
	}
}
