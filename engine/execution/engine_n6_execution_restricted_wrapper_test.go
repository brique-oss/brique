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

package execution_test

import (
	"testing"

	"brique_engine/circulation"
	"brique_engine/security/audit"
	"brique_engine/security/secadmin"
)

// The whole path of a restricted wrapper, with nothing injected: the policy names the
// principal local.sandbox_py, the listener requires the launch secret, the engine launches
// the real Python wrapper with that secret in its URL, the wrapper connects, and the call
// it makes on its own is decided by its principal. A trusted local caller starts it.
func TestEngine_N6_EXEC_ARestrictedRealWrapperActsAsItsPrincipal(t *testing.T) {
	setup := func(grants ...string) func(*secadmin.Dir) {
		return func(d *secadmin.Dir) {
			if err := d.PrincipalAdd("local.sandbox_py", "local", ""); err != nil {
				t.Fatal(err)
			}
			for _, target := range grants {
				if _, err := d.GrantAdd("local.sandbox_py", "call", []string{target}, nil); err != nil {
					t.Fatal(err)
				}
			}
			// A principal with no grant at all is not valid for a wrapper to run as;
			// give it something harmless so that the policy compiles in both cases.
			if _, err := d.GrantAdd("local.sandbox_py", "read", []string{"/root/nowhere"}, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	const callee = n6ExecutionWorkspaceID + "#capacity:sandbox.cpp.echo"

	t.Run("the call its principal is granted goes through", func(t *testing.T) {
		e := startCallerRightsEngineAs(t, "required", setup(callee))
		resp := callN6ExecutionUserCap(t, e.h, n6ExecutionWorkspaceID, "n6-restricted-granted", "sandbox.py.call.cpp",
			map[string]any{"message": "from-a-restricted-wrapper"})
		if resp.Status != circulation.ValueStatusOK {
			t.Fatalf("status=%q error=%#v payload=%#v", resp.Status, resp.Error, resp.Payload)
		}
		payload := mustExecutionPayloadMap(t, resp.Payload)
		if payload["remote_status"] != circulation.ValueStatusOK {
			t.Fatalf("the granted call was not served: %#v", payload)
		}
	})

	t.Run("the call its principal is not granted is refused and journaled under its name", func(t *testing.T) {
		e := startCallerRightsEngineAs(t, "required", setup())
		resp := callN6ExecutionUserCap(t, e.h, n6ExecutionWorkspaceID, "n6-restricted-denied", "sandbox.py.call.cpp",
			map[string]any{"message": "from-a-restricted-wrapper"})
		if resp.Status == circulation.ValueStatusOK {
			if payload := mustExecutionPayloadMap(t, resp.Payload); payload["remote_status"] == circulation.ValueStatusOK {
				t.Fatalf("a restricted wrapper made a call its principal does not hold: %#v", payload)
			}
		}
		// The journal holds the first line of a burst for a few seconds; closing the
		// security manager flushes it.
		if err := e.manager.Close(); err != nil {
			t.Fatal(err)
		}
		records, err := e.dir.Denials(audit.Filter{Principal: "local.sandbox_py"})
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.Reason == audit.ReasonCapabilityDenied && record.Capacity == "user/sandbox.cpp.echo" {
				return
			}
		}
		t.Fatalf("no capability_denied record for local.sandbox_py on user/sandbox.cpp.echo: %+v", records)
	})

	t.Run("without a principal of its name the same wrapper keeps the trust of its context", func(t *testing.T) {
		e := startCallerRightsEngineAs(t, "required", func(*secadmin.Dir) {})
		resp := callN6ExecutionUserCap(t, e.h, n6ExecutionWorkspaceID, "n6-unrestricted", "sandbox.py.call.cpp",
			map[string]any{"message": "from-a-trusted-wrapper"})
		payload := mustExecutionPayloadMap(t, resp.Payload)
		if resp.Status != circulation.ValueStatusOK || payload["remote_status"] != circulation.ValueStatusOK {
			t.Fatalf("a wrapper with no principal lost its authority: %#v", resp)
		}
	})
}
