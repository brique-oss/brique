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

package trust

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRuntimeActivationIsDurableThenAtomic(t *testing.T) {
	envelope, authority, _, _ := signedTestEnvelope(t)
	decoded := mustDecode(t, envelope, authority)
	runtime, err := NewRuntime("prod-a", "main")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := runtime.Prepare(decoded, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	persistErr := errors.New("disk unavailable")
	if err := runtime.Commit(candidate, func(ActivationRecord) error { return persistErr }); !errors.Is(err, persistErr) {
		t.Fatalf("Commit error = %v, want persistence error", err)
	}
	if runtime.Active() != nil {
		t.Fatal("snapshot published despite persistence failure")
	}

	var persisted ActivationRecord
	if err := runtime.Commit(candidate, func(record ActivationRecord) error {
		persisted = record
		if runtime.Active() != nil {
			t.Fatal("snapshot was visible before persistence completed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if runtime.Active() == nil || persisted.PolicyHash != decoded.PolicyHash() || persisted.Revision != "1" {
		t.Fatalf("unexpected activation: %#v", persisted)
	}
}

func TestRuntimeRecoversExactDurableActivation(t *testing.T) {
	envelope, authority, _, _ := signedTestEnvelope(t)
	decoded := mustDecode(t, envelope, authority)
	record := ActivationRecord{
		InstanceID: decoded.InstanceID(), PolicyID: decoded.PolicyID(), Revision: decoded.Revision(),
		PolicyHash: decoded.PolicyHash(), PreviousPolicyHash: "",
	}
	runtime, err := NewRuntime(decoded.InstanceID(), decoded.PolicyID())
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Recover(decoded, testCatalog(), record); err != nil {
		t.Fatal(err)
	}
	if runtime.Active() == nil || runtime.Active().PolicyHash() != decoded.PolicyHash() {
		t.Fatal("durable activation was not recovered")
	}
	wrong := record
	wrong.PolicyHash = "bp1_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	other, _ := NewRuntime(decoded.InstanceID(), decoded.PolicyID())
	if err := other.Recover(decoded, testCatalog(), wrong); err == nil {
		t.Fatal("mismatched durable activation was recovered")
	}
	if err := runtime.Recover(decoded, testCatalog(), record); err == nil {
		t.Fatal("active runtime accepted a second recovery")
	}
}

func TestRuntimeRejectsWrongChainAndStaleCandidate(t *testing.T) {
	_, authority, privateKey, policy := signedTestEnvelope(t)
	runtime := mustRuntime(t)
	activatePolicy(t, runtime, policy, authority, privateKey)

	wrong := clonePolicy(t, policy)
	wrong.Revision = "3"
	wrong.PreviousPolicyHash = runtime.Active().PolicyHash()
	if _, err := runtime.Prepare(mustDecode(t, encodeSignedEnvelope(t, wrong, authority, privateKey), authority), testCatalog()); err == nil || !strings.Contains(err.Error(), "exact successor") {
		t.Fatalf("wrong revision error = %v", err)
	}

	next := clonePolicy(t, policy)
	next.Revision = "2"
	next.PreviousPolicyHash = runtime.Active().PolicyHash()
	decoded := mustDecode(t, encodeSignedEnvelope(t, next, authority, privateKey), authority)
	first, err := runtime.Prepare(decoded, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.Prepare(decoded, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Commit(first, successfulPersist); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Commit(second, successfulPersist); !errors.Is(err, ErrStaleCandidate) {
		t.Fatalf("second Commit = %v, want ErrStaleCandidate", err)
	}
}

func TestRuntimeRejectsTerminalTombstoneReactivation(t *testing.T) {
	_, authority, privateKey, policy := signedTestEnvelope(t)
	policy.Principals[0].SigningKeys[0].Status = "revoked"
	runtime := mustRuntime(t)
	activatePolicy(t, runtime, policy, authority, privateKey)

	next := clonePolicy(t, policy)
	next.Revision = "2"
	next.PreviousPolicyHash = runtime.Active().PolicyHash()
	next.Principals[0].SigningKeys[0].Status = "active"
	decoded := mustDecode(t, encodeSignedEnvelope(t, next, authority, privateKey), authority)
	if _, err := runtime.Prepare(decoded, testCatalog()); err == nil || !strings.Contains(err.Error(), "reactivated") {
		t.Fatalf("Prepare = %v, want tombstone rejection", err)
	}
}

func TestRuntimeIntersectsAdmissionAndCurrentPolicy(t *testing.T) {
	_, authority, privateKey, policy := signedTestEnvelope(t)
	policy.Scopes[0].Allow.Capabilities[0].Names = []string{"matter.read"}
	policy.Scopes[0].Allow.Resources[0].Actions = []string{"read"}
	runtime := mustRuntime(t)
	activatePolicy(t, runtime, policy, authority, privateKey)
	admission, err := runtime.Active().Authenticate(policy.Principals[0].SigningKeys[0].KeyID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	expanded := clonePolicy(t, policy)
	expanded.Revision = "2"
	expanded.PreviousPolicyHash = runtime.Active().PolicyHash()
	expanded.Scopes[0].Allow.Capabilities[0].Names = append(expanded.Scopes[0].Allow.Capabilities[0].Names, "matter.stat")
	activatePolicy(t, runtime, expanded, authority, privateKey)
	if runtime.AuthorizeCapability(admission, "/root/catalog", "matter", "matter.stat") {
		t.Fatal("policy expansion enlarged an existing admission")
	}
	if !runtime.AuthorizeCapability(admission, "/root/catalog", "matter", "matter.read") {
		t.Fatal("permission present in both snapshots was rejected")
	}

	restricted := clonePolicy(t, expanded)
	restricted.Revision = "3"
	restricted.PreviousPolicyHash = runtime.Active().PolicyHash()
	restricted.Scopes[0].Allow.Capabilities[0].Names = []string{"matter.stat"}
	activatePolicy(t, runtime, restricted, authority, privateKey)
	if runtime.AuthorizeCapability(admission, "/root/catalog", "matter", "matter.read") {
		t.Fatal("current policy restriction did not constrain an old admission")
	}
}

func TestRuntimeConcurrentReadersObserveCompleteSnapshots(t *testing.T) {
	_, authority, privateKey, policy := signedTestEnvelope(t)
	runtime := mustRuntime(t)
	activatePolicy(t, runtime, policy, authority, privateKey)

	const readers = 16
	var wait sync.WaitGroup
	errorsCh := make(chan error, readers)
	for range readers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 1000 {
				snapshot := runtime.Active()
				if snapshot == nil || (snapshot.Revision() != "1" && snapshot.Revision() != "2") || snapshot.PolicyHash() == "" {
					errorsCh <- fmt.Errorf("observed incomplete snapshot")
					return
				}
			}
		}()
	}
	next := clonePolicy(t, policy)
	next.Revision = "2"
	next.PreviousPolicyHash = runtime.Active().PolicyHash()
	activatePolicy(t, runtime, next, authority, privateKey)
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
}

func mustRuntime(t *testing.T) *Runtime {
	t.Helper()
	runtime, err := NewRuntime("prod-a", "main")
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func activatePolicy(t *testing.T, runtime *Runtime, policy Policy, authority Authority, privateKey []byte) {
	t.Helper()
	decoded := mustDecode(t, encodeSignedEnvelope(t, policy, authority, privateKey), authority)
	candidate, err := runtime.Prepare(decoded, testCatalog())
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Commit(candidate, successfulPersist); err != nil {
		t.Fatal(err)
	}
}

func clonePolicy(t *testing.T, source Policy) Policy {
	t.Helper()
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var clone Policy
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func successfulPersist(ActivationRecord) error { return nil }
