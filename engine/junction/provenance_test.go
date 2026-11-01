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

package junction_test

import (
	"testing"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/junction"
)

// The engine stamps the principal of a chain's root on its trace events; a value a
// producer wrote is never kept, and an unknown chain is left unstamped.
func TestTraceEmitStampsProvenanceFromTheAdmittedRoot(t *testing.T) {
	registry := junction.NewContextCommRegistry()
	registry.Provenance().Record("root-1", "partner-a", "alice")
	ch := make(chan junction.Delivery, 4)
	f := frameForTraceTest(true, configuration.ValueConfigTraceLevelDebug, ch)
	f.CtxCommReg = registry

	junction.TraceEmit(f, circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress, IntentionId: "child-9", RootIntentionId: "root-1"})
	if tw := recvTrace(t, ch); tw.Principal != "partner-a" || tw.Actor != "alice" {
		t.Fatalf("derived call: principal %q actor %q", tw.Principal, tw.Actor)
	}
	junction.TraceEmit(f, circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress, IntentionId: "root-1"})
	if tw := recvTrace(t, ch); tw.Principal != "partner-a" {
		t.Fatalf("the root itself: principal %q", tw.Principal)
	}
	junction.TraceEmit(f, circulation.TraceWire{TraceKind: circulation.ValueTraceCommIngress, IntentionId: "other", Principal: "forged", Actor: "forged"})
	if tw := recvTrace(t, ch); tw.Principal != "" || tw.Actor != "" {
		t.Fatalf("a forged or unknown provenance was kept: %q/%q", tw.Principal, tw.Actor)
	}
}
