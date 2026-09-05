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
	"brique_engine/junction"
	"brique_engine/shared"
)

// Covers: N1-CCR-01, N1-CCR-10
func TestContextCommRegistry_N1_CCR_01_10_NewRegistryStartsEmpty(t *testing.T) {
	r := junction.NewContextCommRegistry()

	if ch, ok := r.ResolveCh("/ctx/a"); ok || ch != nil {
		t.Fatalf("ResolveCh expected miss, got (%v,%v)", ch, ok)
	}
	if _, ok := r.ResolveEntry("/ctx/a"); ok {
		t.Fatalf("ResolveEntry expected miss")
	}
	if addr, ok := r.ResolveExtName("ext-a"); ok || addr != "" {
		t.Fatalf("ResolveExtName expected miss, got (%q,%v)", addr, ok)
	}
	if ext, ok := r.ResolveIDToExtName("/ctx/a"); ok || ext != "" {
		t.Fatalf("ResolveIDToExtName expected miss, got (%q,%v)", ext, ok)
	}
	if ctx, ok := r.ResolveUI("ui-a"); ok || ctx != "" {
		t.Fatalf("ResolveUI expected miss, got (%q,%v)", ctx, ok)
	}
	if ctx, ok := r.ResolveWrapperBoundary("wrapper-a"); ok || ctx != "" {
		t.Fatalf("ResolveWrapperBoundary expected miss, got (%q,%v)", ctx, ok)
	}
}

// Covers: N1-CCR-02
func TestContextCommRegistry_N1_CCR_02_RegisterGuardNoOp(t *testing.T) {
	r := junction.NewContextCommRegistry()
	ch := make(chan circulation.Message)

	r.Register("", ch, "ext-a")
	r.Register("/ctx/a", nil, "ext-a")

	if _, ok := r.ResolveCh("/ctx/a"); ok {
		t.Fatalf("expected no registration when guard fails")
	}
	if _, ok := r.ResolveExtName("ext-a"); ok {
		t.Fatalf("expected no ext mapping when guard fails")
	}
}

// Covers: N1-CCR-03
func TestContextCommRegistry_N1_CCR_03_RegisterAndResolve(t *testing.T) {
	r := junction.NewContextCommRegistry()
	ch := make(chan circulation.Message)

	r.Register("/ctx/a", ch, "ext-a")

	resolvedCh, ok := r.ResolveCh("/ctx/a")
	if !ok || resolvedCh == nil {
		t.Fatalf("ResolveCh expected hit")
	}
	if resolvedCh != (chan<- circulation.Message)(ch) {
		t.Fatalf("ResolveCh returned unexpected channel")
	}

	entry, ok := r.ResolveEntry("/ctx/a")
	if !ok || entry.CommIn == nil {
		t.Fatalf("ResolveEntry expected hit")
	}
	if entry.CommIn != (chan<- circulation.Message)(ch) {
		t.Fatalf("ResolveEntry returned unexpected CommIn")
	}

	addr, ok := r.ResolveExtName("ext-a")
	if !ok || addr != shared.ContextAddr("/ctx/a") {
		t.Fatalf("ResolveExtName got (%q,%v), want (/ctx/a,true)", addr, ok)
	}

	ext, ok := r.ResolveIDToExtName("/ctx/a")
	if !ok || ext != "ext-a" {
		t.Fatalf("ResolveIDToExtName got (%q,%v), want (ext-a,true)", ext, ok)
	}
}

// Covers: N1-CCR-04
func TestContextCommRegistry_N1_CCR_04_ReregisterChangesExtAlias(t *testing.T) {
	r := junction.NewContextCommRegistry()
	ch := make(chan circulation.Message)

	r.Register("/ctx/a", ch, "ext-a")
	r.Register("/ctx/a", ch, "ext-b")

	if _, ok := r.ResolveExtName("ext-a"); ok {
		t.Fatalf("old ext alias should be removed")
	}
	addr, ok := r.ResolveExtName("ext-b")
	if !ok || addr != shared.ContextAddr("/ctx/a") {
		t.Fatalf("new ext alias expected, got (%q,%v)", addr, ok)
	}
	ext, ok := r.ResolveIDToExtName("/ctx/a")
	if !ok || ext != "ext-b" {
		t.Fatalf("inverse mapping should point to new ext, got (%q,%v)", ext, ok)
	}
}

// Covers: N1-CCR-05
func TestContextCommRegistry_N1_CCR_05_ReregisterWithEmptyExtKeepsInverseAsImplemented(t *testing.T) {
	r := junction.NewContextCommRegistry()
	ch := make(chan circulation.Message)

	r.Register("/ctx/a", ch, "ext-a")
	r.Register("/ctx/a", ch, "")

	if _, ok := r.ResolveExtName("ext-a"); ok {
		t.Fatalf("ext->id mapping should be removed")
	}
	ext, ok := r.ResolveIDToExtName("/ctx/a")
	if !ok || ext != "ext-a" {
		t.Fatalf("id->ext mapping is expected to remain per current behavior, got (%q,%v)", ext, ok)
	}
}

// Covers: N1-CCR-06
func TestContextCommRegistry_N1_CCR_06_UnregisterGuardNoOp(t *testing.T) {
	r := junction.NewContextCommRegistry()
	ch := make(chan circulation.Message)
	r.Register("/ctx/a", ch, "ext-a")

	r.Unregister("", "ext-a")

	if _, ok := r.ResolveCh("/ctx/a"); !ok {
		t.Fatalf("state should remain unchanged on empty addr")
	}
}

// Covers: N1-CCR-07, N1-CCR-09
func TestContextCommRegistry_N1_CCR_07_09_UnregisterCleansIDUIAndInverse(t *testing.T) {
	r := junction.NewContextCommRegistry()
	ch := make(chan circulation.Message)
	r.Register("/ctx/a", ch, "ext-a")
	r.RegisterUI("ui-1", "/ctx/a")
	r.RegisterUI("ui-2", "/ctx/a")

	r.Unregister("/ctx/a", "")

	if _, ok := r.ResolveCh("/ctx/a"); ok {
		t.Fatalf("id mapping should be removed")
	}
	if _, ok := r.ResolveUI("ui-1"); ok {
		t.Fatalf("ui-1 mapping should be removed")
	}
	if _, ok := r.ResolveUI("ui-2"); ok {
		t.Fatalf("ui-2 mapping should be removed")
	}
	if _, ok := r.ResolveExtName("ext-a"); ok {
		t.Fatalf("ext mapping should be removed")
	}
	if _, ok := r.ResolveIDToExtName("/ctx/a"); ok {
		t.Fatalf("inverse mapping should be removed")
	}
}

// Covers: N1-CCR-08
func TestContextCommRegistry_N1_CCR_08_UnregisterWithExplicitExtName(t *testing.T) {
	r := junction.NewContextCommRegistry()
	ch := make(chan circulation.Message)
	r.Register("/ctx/a", ch, "ext-a")

	r.Unregister("/ctx/a", "ext-a")

	if _, ok := r.ResolveCh("/ctx/a"); ok {
		t.Fatalf("id mapping should be removed")
	}
	if _, ok := r.ResolveExtName("ext-a"); ok {
		t.Fatalf("ext mapping should be removed")
	}
	if _, ok := r.ResolveIDToExtName("/ctx/a"); ok {
		t.Fatalf("inverse mapping should be removed for matching explicit ext")
	}
}

// Covers: N1-CCR-10
func TestContextCommRegistry_N1_CCR_10_ResolveMethodsInvalidInputMiss(t *testing.T) {
	r := junction.NewContextCommRegistry()

	if addr, ok := r.ResolveExtName(""); ok || addr != "" {
		t.Fatalf("ResolveExtName empty should miss")
	}
	if ext, ok := r.ResolveIDToExtName(""); ok || ext != "" {
		t.Fatalf("ResolveIDToExtName empty should miss")
	}
	if ctx, ok := r.ResolveUI(""); ok || ctx != "" {
		t.Fatalf("ResolveUI empty should miss")
	}
	if ctx, ok := r.ResolveWrapperBoundary(""); ok || ctx != "" {
		t.Fatalf("ResolveWrapperBoundary empty should miss")
	}
}

// Covers: N1-CCR-11, N1-CCR-12
func TestContextCommRegistry_N1_CCR_11_12_RegisterUIAndUnregisterUI(t *testing.T) {
	r := junction.NewContextCommRegistry()

	r.RegisterUI("", "/ctx/a")
	r.RegisterUI("ui-a", "")
	if _, ok := r.ResolveUI("ui-a"); ok {
		t.Fatalf("invalid RegisterUI inputs should not create mapping")
	}

	r.RegisterUI("ui-a", "/ctx/a")
	ctx, ok := r.ResolveUI("ui-a")
	if !ok || ctx != shared.ContextAddr("/ctx/a") {
		t.Fatalf("ResolveUI expected (/ctx/a,true), got (%q,%v)", ctx, ok)
	}

	r.UnregisterUI("")
	ctx, ok = r.ResolveUI("ui-a")
	if !ok || ctx != shared.ContextAddr("/ctx/a") {
		t.Fatalf("empty UnregisterUI should be no-op")
	}

	r.UnregisterUI("ui-a")
	if _, ok := r.ResolveUI("ui-a"); ok {
		t.Fatalf("ui mapping should be removed")
	}
}

func TestContextCommRegistry_N1_CCR_12b_ResolveUIInScopePrefersNearestAncestor(t *testing.T) {
	r := junction.NewContextCommRegistry()

	r.RegisterUI("main", "/root")
	r.RegisterUI("main", "/root/photographie")

	ctx, ok := r.ResolveUIInScope("main", "/root/photographie/image")
	if !ok || ctx != shared.ContextAddr("/root/photographie") {
		t.Fatalf("ResolveUIInScope expected (/root/photographie,true), got (%q,%v)", ctx, ok)
	}

	ctx, ok = r.ResolveUIInScope("main", "/root/mcp")
	if !ok || ctx != shared.ContextAddr("/root") {
		t.Fatalf("ResolveUIInScope root fallback expected (/root,true), got (%q,%v)", ctx, ok)
	}
}

func TestContextCommRegistry_N1_CCR_12c_UnregisterUIOwnerKeepsCollidingOwners(t *testing.T) {
	r := junction.NewContextCommRegistry()

	r.RegisterUI("main", "/root")
	r.RegisterUI("main", "/root/photographie")
	r.UnregisterUIOwner("main", "/root/photographie")

	ctx, ok := r.ResolveUIInScope("main", "/root/mcp")
	if !ok || ctx != shared.ContextAddr("/root") {
		t.Fatalf("root UI owner should remain after child unregister, got (%q,%v)", ctx, ok)
	}
	if ctx, ok := r.ResolveUIInScope("main", "/root/photographie/image"); !ok || ctx != shared.ContextAddr("/root") {
		t.Fatalf("child scope should fall back to remaining root owner, got (%q,%v)", ctx, ok)
	}

	r.RegisterUI("main", "/root/photographie")
	r.UnregisterUIOwner("main", "/root")
	ctx, ok = r.ResolveUIInScope("main", "/root/photographie/image")
	if !ok || ctx != shared.ContextAddr("/root/photographie") {
		t.Fatalf("child UI owner should remain after root unregister, got (%q,%v)", ctx, ok)
	}
}

// Covers: N1-CCR-13, N1-CCR-14
func TestContextCommRegistry_N1_CCR_13_14_RegisterWrapperBoundaryAndCollision(t *testing.T) {
	r := junction.NewContextCommRegistry()

	if err := r.RegisterWrapperBoundary("", "/ctx/a"); err != nil {
		t.Fatalf("empty wrapper should be guard no-op with nil err, got %v", err)
	}
	if err := r.RegisterWrapperBoundary("w", ""); err != nil {
		t.Fatalf("empty boundary should be guard no-op with nil err, got %v", err)
	}
	if _, ok := r.ResolveWrapperBoundary("w"); ok {
		t.Fatalf("guard no-op should not create mapping")
	}

	if err := r.RegisterWrapperBoundary("w", "/ctx/a"); err != nil {
		t.Fatalf("register wrapper expected success, got %v", err)
	}
	ctx, ok := r.ResolveWrapperBoundary("w")
	if !ok || ctx != shared.ContextAddr("/ctx/a") {
		t.Fatalf("ResolveWrapperBoundary expected (/ctx/a,true), got (%q,%v)", ctx, ok)
	}

	if err := r.RegisterWrapperBoundary("w", "/ctx/a"); err != nil {
		t.Fatalf("same binding should be idempotent, got %v", err)
	}
	if err := r.RegisterWrapperBoundary("w", "/ctx/b"); err == nil {
		t.Fatalf("collision expected non-nil error")
	}
	ctx, ok = r.ResolveWrapperBoundary("w")
	if !ok || ctx != shared.ContextAddr("/ctx/a") {
		t.Fatalf("collision must preserve existing mapping, got (%q,%v)", ctx, ok)
	}
}

// Covers: N1-CCR-15
func TestContextCommRegistry_N1_CCR_15_UnregisterWrapperBoundary(t *testing.T) {
	r := junction.NewContextCommRegistry()
	if err := r.RegisterWrapperBoundary("w", "/ctx/a"); err != nil {
		t.Fatalf("setup register wrapper failed: %v", err)
	}

	r.UnregisterWrapperBoundary("")
	if _, ok := r.ResolveWrapperBoundary("w"); !ok {
		t.Fatalf("empty unregister should be no-op")
	}

	r.UnregisterWrapperBoundary("w")
	if _, ok := r.ResolveWrapperBoundary("w"); ok {
		t.Fatalf("wrapper mapping should be removed")
	}
}
