// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package execution_test

import (
	"testing"

	"brique_engine/engine/circulation"
	"brique_engine/engine/shared"
)

// A call to a capacity that does not exist is answered at once, in a child context and at
// the root alike (the root declares no user capacity): its sender never waits for a timeout.
func TestACallToAnUnknownCapacityIsAnsweredAtTheRootAsInAChild(t *testing.T) {
	h := newEngineN6ExecutionHarness(t)
	for _, ctx := range []string{n6ExecutionWorkspaceID, shared.RootContextID} {
		resp := callN6ExecutionUserCap(t, h, ctx, "unknown-cap-"+ctx, "does.not.exist", map[string]any{})
		if resp.Status != circulation.ValueStatusError || resp.Error == nil {
			t.Fatalf("%s: status=%q error=%+v, want an error", ctx, resp.Status, resp.Error)
		}
		if resp.Error.Code != circulation.ValueCodeInvalid || resp.Error.Details[circulation.KeyReason] != circulation.ValueReasonUnknownCap {
			t.Fatalf("%s: error=%+v, want invalid/unknown_cap", ctx, resp.Error)
		}
	}
}
