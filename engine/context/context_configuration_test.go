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

package context_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"brique_engine/circulation"
	"brique_engine/configuration"
	"brique_engine/context"
	"brique_engine/shared"
)

type mockCtxCommReg struct{}

func (m *mockCtxCommReg) Register(addr shared.ContextAddr, commIn chan<- circulation.Message, extName string) {
}
func (m *mockCtxCommReg) Unregister(addr shared.ContextAddr, extName string) {}
func (m *mockCtxCommReg) ResolveCh(addr shared.ContextAddr) (chan<- circulation.Message, bool) {
	return nil, false
}
func (m *mockCtxCommReg) ResolveExtName(extName string) (shared.ContextAddr, bool) {
	return "", false
}
func (m *mockCtxCommReg) ResolveIDToExtName(id shared.ContextAddr) (string, bool) {
	return "", false
}
func (m *mockCtxCommReg) RegisterUI(uiName string, ctxID shared.ContextAddr) {}
func (m *mockCtxCommReg) UnregisterUI(uiName string)                         {}
func (m *mockCtxCommReg) UnregisterUIOwner(uiName string, ctxID shared.ContextAddr) {
}
func (m *mockCtxCommReg) ResolveUI(uiName string) (shared.ContextAddr, bool) {
	return "", false
}
func (m *mockCtxCommReg) ResolveUIInScope(uiName string, scopeCtxID shared.ContextAddr) (shared.ContextAddr, bool) {
	return "", false
}
func (m *mockCtxCommReg) RegisterWrapperBoundary(wrapperName string, boundaryCtxID shared.ContextAddr) error {
	return nil
}
func (m *mockCtxCommReg) UnregisterWrapperBoundary(wrapperName string) {}
func (m *mockCtxCommReg) ResolveWrapperBoundary(wrapperName string) (shared.ContextAddr, bool) {
	return "", false
}

func writeContextJSON(t *testing.T, dir string, root map[string]any) {
	t.Helper()
	b, err := json.Marshal(root)
	if err != nil {
		t.Fatalf("marshal root json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "context.json"), b, 0o644); err != nil {
		t.Fatalf("write context.json: %v", err)
	}
}

func baseBrique() map[string]any {
	return map[string]any{
		configuration.KeyContextName:   "ctx-A",
		configuration.KeyEngineVersion: shared.EngineBinaryVersion,
		configuration.KeyCtxVersion:    "1",
	}
}

func TestBuildContextRegistry_N1_CCFG_01_MapsDescriptor(t *testing.T) {
	reg := &mockCtxCommReg{}
	desc := context.ContextDescriptor{
		CtxId:      "/ctx/a",
		CtxName:    "ctx-a",
		CtxExtName: "public-a",
		EngineVers: "0.0.1",
		CtxVersion: "1",
	}

	got := context.BuildContextRegistry(desc, reg, "/tmp/ctx-a")

	if got.CtxCommReg != reg {
		t.Fatalf("CtxCommReg not mapped")
	}
	if got.CtxId != desc.CtxId || got.CtxName != desc.CtxName || got.CtxExtName != desc.CtxExtName {
		t.Fatalf("identity fields not mapped")
	}
	if got.EngineVers != desc.EngineVers || got.CtxVersion != desc.CtxVersion {
		t.Fatalf("version fields not mapped")
	}
	if got.ContextDir != "/tmp/ctx-a" {
		t.Fatalf("ContextDir = %q, want /tmp/ctx-a", got.ContextDir)
	}
	if got.FamIn == nil {
		t.Fatalf("FamIn should be initialized")
	}
	if len(got.FamIn) != 0 {
		t.Fatalf("FamIn should start empty")
	}
}

func TestLoadContextDescriptor_N1_CCFG_02_EmptyContextDir(t *testing.T) {
	_, err := context.LoadContextDescriptor("", "/ctx/a")
	if err == nil || !strings.Contains(err.Error(), "contextDir is empty") {
		t.Fatalf("expected contextDir empty error, got %v", err)
	}
}

func TestLoadContextDescriptor_N1_CCFG_03_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := context.LoadContextDescriptor(dir, "/ctx/a")
	if err == nil || !strings.Contains(err.Error(), "read context.json") {
		t.Fatalf("expected read context.json error, got %v", err)
	}
}

func TestLoadContextDescriptor_N1_CCFG_04_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "context.json"), []byte("{bad-json"), 0o644); err != nil {
		t.Fatalf("write context.json: %v", err)
	}

	_, err := context.LoadContextDescriptor(dir, "/ctx/a")
	if err == nil || !strings.Contains(err.Error(), "parse context.json") {
		t.Fatalf("expected parse context.json error, got %v", err)
	}
}

func TestLoadContextDescriptor_N1_CCFG_05_MissingBriqueSection(t *testing.T) {
	dir := t.TempDir()
	writeContextJSON(t, dir, map[string]any{"other": map[string]any{}})

	_, err := context.LoadContextDescriptor(dir, "/ctx/a")
	if err == nil || !strings.Contains(err.Error(), "missing 'brique' section") {
		t.Fatalf("expected missing brique section error, got %v", err)
	}
}

func TestLoadContextDescriptor_N1_CCFG_06_MissingCtxName(t *testing.T) {
	dir := t.TempDir()
	syn := baseBrique()
	delete(syn, configuration.KeyContextName)
	writeContextJSON(t, dir, map[string]any{circulation.KeyBrique: syn})

	_, err := context.LoadContextDescriptor(dir, "/ctx/a")
	if err == nil || !strings.Contains(err.Error(), "ctx_name missing or not a string") {
		t.Fatalf("expected ctx_name error, got %v", err)
	}
}

func TestLoadContextDescriptor_N1_CCFG_07_MissingEngineVersion(t *testing.T) {
	dir := t.TempDir()
	syn := baseBrique()
	delete(syn, configuration.KeyEngineVersion)
	writeContextJSON(t, dir, map[string]any{circulation.KeyBrique: syn})

	_, err := context.LoadContextDescriptor(dir, "/ctx/a")
	if err == nil || !strings.Contains(err.Error(), "engine_ver missing or not a string") {
		t.Fatalf("expected engine_version error, got %v", err)
	}
}

func TestLoadContextDescriptor_N1_CCFG_08_EngineVersionTooHigh(t *testing.T) {
	dir := t.TempDir()
	syn := baseBrique()
	syn[configuration.KeyEngineVersion] = "9.9.9"
	writeContextJSON(t, dir, map[string]any{circulation.KeyBrique: syn})

	_, err := context.LoadContextDescriptor(dir, "/ctx/a")
	if err == nil || !strings.Contains(err.Error(), "context requires engine") {
		t.Fatalf("expected engine compatibility error, got %v", err)
	}
}

func TestLoadContextDescriptor_N1_CCFG_09_MissingCtxVersion(t *testing.T) {
	dir := t.TempDir()
	syn := baseBrique()
	delete(syn, configuration.KeyCtxVersion)
	writeContextJSON(t, dir, map[string]any{circulation.KeyBrique: syn})

	_, err := context.LoadContextDescriptor(dir, "/ctx/a")
	if err == nil || !strings.Contains(err.Error(), "ctx_ver missing or not a string") {
		t.Fatalf("expected ctx_version error, got %v", err)
	}
}

func TestLoadContextDescriptor_N1_CCFG_10_SuccessRequiredOnly(t *testing.T) {
	dir := t.TempDir()
	syn := baseBrique()
	writeContextJSON(t, dir, map[string]any{circulation.KeyBrique: syn})

	desc, err := context.LoadContextDescriptor(dir, "/ctx/success")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc.CtxId != "/ctx/success" {
		t.Fatalf("CtxId = %q, want /ctx/success", desc.CtxId)
	}
	if desc.CtxName != "ctx-A" {
		t.Fatalf("CtxName = %q, want ctx-A", desc.CtxName)
	}
	if desc.EngineVers != shared.EngineBinaryVersion {
		t.Fatalf("EngineVers = %q, want %q", desc.EngineVers, shared.EngineBinaryVersion)
	}
	if desc.CtxVersion != "1" {
		t.Fatalf("CtxVersion = %q, want 1", desc.CtxVersion)
	}
	if desc.CtxExtName != "" {
		t.Fatalf("CtxExtName should default to empty")
	}
	if desc.EngineConfig == nil {
		t.Fatalf("EngineConfig should be initialized")
	}
	if len(desc.EngineConfig) != 0 {
		t.Fatalf("EngineConfig should default empty")
	}
	if len(desc.Children) != 0 {
		t.Fatalf("Children should default empty")
	}
}

func TestLoadContextDescriptor_N1_CCFG_11_SuccessWithOptionalFields(t *testing.T) {
	dir := t.TempDir()
	syn := baseBrique()
	syn[configuration.KeyCxtExtName] = "public-a"
	syn[configuration.KeyEngineCfg] = map[string]any{
		"communication": map[string]any{"enabled": true},
		"trace":         map[string]any{"level": "minimal"},
	}
	syn[configuration.KeyChildList] = []any{"childA", "", 42, "childB"}
	writeContextJSON(t, dir, map[string]any{circulation.KeyBrique: syn})

	desc, err := context.LoadContextDescriptor(dir, "/ctx/opt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if desc.CtxExtName != "public-a" {
		t.Fatalf("CtxExtName = %q, want public-a", desc.CtxExtName)
	}
	if len(desc.EngineConfig) != 2 {
		t.Fatalf("EngineConfig len = %d, want 2", len(desc.EngineConfig))
	}
	if _, ok := desc.EngineConfig[shared.FamilyName("communication")]; !ok {
		t.Fatalf("expected communication config entry")
	}
	if _, ok := desc.EngineConfig[shared.FamilyName("trace")]; !ok {
		t.Fatalf("expected trace config entry")
	}
	if len(desc.Children) != 2 || desc.Children[0] != "childA" || desc.Children[1] != "childB" {
		t.Fatalf("Children = %#v, want [childA childB]", desc.Children)
	}
}
