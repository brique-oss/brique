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

package access_test

import (
	"reflect"
	"testing"

	"brique_engine/circulation"
	"brique_engine/security/access"
	"brique_engine/shared"
)

func noAccess(shared.ContextAddr, circulation.Intention) ([]access.ResourceAccess, error) {
	return nil, nil
}

func TestClassVerbs(t *testing.T) {
	for class, want := range map[access.Class][]string{
		access.ClassRead: {"read"}, access.ClassFilteredRead: {"read"}, access.ClassAllOrNothing: {"read"},
		access.ClassWrite: {"write"}, access.ClassCreate: {"create"}, access.ClassReadOrCreate: {"read", "create"},
		access.ClassDefine: {"define"}, access.ClassControl: {"control"},
		access.Class("unknown"): nil, access.Class(""): nil,
	} {
		if got := class.Verbs(); !reflect.DeepEqual(got, want) {
			t.Errorf("%q verbs = %v, want %v", class, got, want)
		}
	}
}

func TestEveryClassifiedCapacityHasExactlyOneKnownClass(t *testing.T) {
	for _, family := range []string{"matter", "reflexive", "execution", "control"} {
		for name, class := range access.EngineCapacities(family) {
			if len(class.Verbs()) == 0 {
				t.Errorf("%s/%s has class %q with no verb", family, name, class)
			}
			if got, ok := access.ClassOf(family, name); !ok || got != class {
				t.Errorf("ClassOf(%s,%s) = %q,%v", family, name, got, ok)
			}
		}
	}
	if _, ok := access.ClassOf("user", "sample.echo"); ok {
		t.Error("a user capacity must have no engine class")
	}
	if _, ok := access.ClassOf("matter", "matter.bogus"); ok {
		t.Error("an unknown capacity must have no class")
	}
}

func TestSafetyCriticalClasses(t *testing.T) {
	// Pinned so that a reclassification is a reviewed, visible change.
	for key, want := range map[[2]string]access.Class{
		{"matter", "matter.create"}: access.ClassCreate, {"matter", "matter.write"}: access.ClassWrite,
		{"matter", "matter.clone"}: access.ClassReadOrCreate, {"matter", "matter.subscribe"}: access.ClassRead, {"matter", "matter.subscription.ack"}: access.ClassRead,
		{"reflexive", "meaning.rebuild"}: access.ClassControl, {"reflexive", "edit.delete"}: access.ClassDefine,
		{"reflexive", "trace.inspect"}: access.ClassAllOrNothing, {"reflexive", "vocabulary.patch"}: access.ClassDefine,
		{"execution", "wrapper.start"}: access.ClassControl, {"control", "context.stop"}: access.ClassControl,
	} {
		if got, _ := access.ClassOf(key[0], key[1]); got != want {
			t.Errorf("%v class = %q, want %q", key, got, want)
		}
	}
}

func TestFamilyRegistryRefusesUnclassifiedCapacities(t *testing.T) {
	if _, err := access.NewFamilyRegistry("matter", map[string]access.Extractor{"matter.read": noAccess}, 0); err != nil {
		t.Fatalf("classified table refused: %v", err)
	}
	if _, err := access.NewFamilyRegistry("matter", map[string]access.Extractor{"matter.read": noAccess, "matter.sneaky": noAccess}, 0); err == nil {
		t.Fatal("an unclassified capacity was accepted")
	}
	if _, err := access.NewFamilyRegistry("nowhere", map[string]access.Extractor{"matter.read": noAccess}, 0); err == nil {
		t.Fatal("an unknown family was accepted")
	}
}
