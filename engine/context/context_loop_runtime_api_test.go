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
	"sort"
	"testing"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

type fakeFamilyLoop struct {
	state shared.FamilyState
}

func (f *fakeFamilyLoop) InChan() chan circulation.Message { return make(chan circulation.Message) }
func (f *fakeFamilyLoop) Start()                           {}
func (f *fakeFamilyLoop) Stop()                            {}
func (f *fakeFamilyLoop) State() shared.FamilyState        { return f.state }

func TestInstallRuntimeAPI_N1_CLRA_01_RuntimeInstalled(t *testing.T) {
	c := &ContextLoop{
		families:   make(map[shared.FamilyName]junction.FamilyLoop),
		childLoops: make(map[string]*ContextLoop),
		state:      shared.ContextInitializing,
	}

	c.installRuntimeAPI()

	if c.frame.Runtime == nil {
		t.Fatalf("frame.Runtime should be installed")
	}
}

func TestInstallRuntimeAPI_N1_CLRA_02_GetContextState(t *testing.T) {
	c := &ContextLoop{
		families:   make(map[shared.FamilyName]junction.FamilyLoop),
		childLoops: make(map[string]*ContextLoop),
		state:      shared.ContextRunning,
	}
	c.installRuntimeAPI()

	got := c.frame.Runtime.GetContextState()
	if got != shared.ContextRunning {
		t.Fatalf("GetContextState = %v, want %v", got, shared.ContextRunning)
	}
}

func TestInstallRuntimeAPI_N1_CLRA_03_04_05_GetFamilyStateBranches(t *testing.T) {
	c := &ContextLoop{
		families:   make(map[shared.FamilyName]junction.FamilyLoop),
		childLoops: make(map[string]*ContextLoop),
		state:      shared.ContextRunning,
	}

	c.families[shared.FamilyComm] = &fakeFamilyLoop{state: shared.FamilyRunning}
	c.families[shared.FamilyTrace] = nil
	c.installRuntimeAPI()

	if st, ok := c.frame.Runtime.GetFamilyState(""); ok || st != shared.FamilyStopped {
		t.Fatalf("empty family name should fail-closed, got (%v,%v)", st, ok)
	}
	if st, ok := c.frame.Runtime.GetFamilyState(shared.FamilyExecution); ok || st != shared.FamilyStopped {
		t.Fatalf("missing family should fail-closed, got (%v,%v)", st, ok)
	}
	if st, ok := c.frame.Runtime.GetFamilyState(shared.FamilyTrace); ok || st != shared.FamilyStopped {
		t.Fatalf("nil family should fail-closed, got (%v,%v)", st, ok)
	}
	if st, ok := c.frame.Runtime.GetFamilyState(shared.FamilyComm); !ok || st != shared.FamilyRunning {
		t.Fatalf("existing family should return running,true got (%v,%v)", st, ok)
	}
}

func TestInstallRuntimeAPI_N1_CLRA_06_ListFamilyStateSkipsNil(t *testing.T) {
	c := &ContextLoop{
		families:   make(map[shared.FamilyName]junction.FamilyLoop),
		childLoops: make(map[string]*ContextLoop),
		state:      shared.ContextRunning,
	}

	c.families[shared.FamilyComm] = &fakeFamilyLoop{state: shared.FamilyRunning}
	c.families[shared.FamilyExecution] = &fakeFamilyLoop{state: shared.FamilyStopped}
	c.families[shared.FamilyTrace] = nil
	c.installRuntimeAPI()

	got := c.frame.Runtime.ListFamilyState()
	if len(got) != 2 {
		t.Fatalf("ListFamilyState len=%d, want 2", len(got))
	}
	if got[shared.FamilyComm] != shared.FamilyRunning {
		t.Fatalf("FamilyComm state mismatch")
	}
	if got[shared.FamilyExecution] != shared.FamilyStopped {
		t.Fatalf("FamilyExecution state mismatch")
	}
	if _, ok := got[shared.FamilyTrace]; ok {
		t.Fatalf("nil family should be excluded")
	}
}

func TestInstallRuntimeAPI_N1_CLRA_07_ListChildrenNamesSkipsEmpty(t *testing.T) {
	c := &ContextLoop{
		families:   make(map[shared.FamilyName]junction.FamilyLoop),
		childLoops: make(map[string]*ContextLoop),
		state:      shared.ContextRunning,
	}
	c.childLoops[""] = &ContextLoop{}
	c.childLoops["alpha"] = &ContextLoop{}
	c.childLoops["beta"] = &ContextLoop{}
	c.installRuntimeAPI()

	got := c.frame.Runtime.ListChildrenNames()
	sort.Strings(got)
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("ListChildrenNames = %#v, want [alpha beta]", got)
	}
}

func TestInstallRuntimeAPI_N1_CLRA_08_HasChildBranches(t *testing.T) {
	c := &ContextLoop{
		families:   make(map[shared.FamilyName]junction.FamilyLoop),
		childLoops: make(map[string]*ContextLoop),
		state:      shared.ContextRunning,
	}
	c.childLoops["alpha"] = nil // key existence is the contract for HasChild
	c.installRuntimeAPI()

	if c.frame.Runtime.HasChild("") {
		t.Fatalf("HasChild empty should be false")
	}
	if c.frame.Runtime.HasChild("missing") {
		t.Fatalf("HasChild missing should be false")
	}
	if !c.frame.Runtime.HasChild("alpha") {
		t.Fatalf("HasChild existing key should be true")
	}
}
