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
	"errors"
	"testing"

	"brique_engine/circulation"
	"brique_engine/security/access"
	"brique_engine/shared"
)

func TestRegistryExtractValidatesBoundsAndStableDeduplication(t *testing.T) {
	want := access.ResourceAccess{Kind: "matter", Context: "/root/catalog", ID: "conversation", Action: "read"}
	registry, err := access.NewRegistry(map[string]access.Extractor{
		"matter.read": func(shared.ContextAddr, circulation.Intention) ([]access.ResourceAccess, error) {
			return []access.ResourceAccess{want, want}, nil
		},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	got, err := registry.Extract("matter.read", "/root/catalog", circulation.Intention{})
	if err != nil || len(got) != 1 || got[0] != want {
		t.Fatalf("Extract = %#v, %v", got, err)
	}

	overflow, err := access.NewRegistry(map[string]access.Extractor{
		"matter.read": func(shared.ContextAddr, circulation.Intention) ([]access.ResourceAccess, error) {
			return []access.ResourceAccess{want, want, want}, nil
		},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := overflow.Extract("matter.read", "/root/catalog", circulation.Intention{}); err == nil {
		t.Fatal("expected pre-deduplication bound rejection")
	}
}

func TestRegistryFailsClosedForMissingInvalidAndFailingExtractors(t *testing.T) {
	if _, err := access.NewRegistry(map[string]access.Extractor{"Bad Cap": func(shared.ContextAddr, circulation.Intention) ([]access.ResourceAccess, error) { return nil, nil }}, 0); err == nil {
		t.Fatal("expected invalid capacity rejection")
	}
	if _, err := access.NewRegistry(map[string]access.Extractor{"matter.read": nil}, 0); err == nil {
		t.Fatal("expected nil extractor rejection")
	}
	registry, err := access.NewRegistry(map[string]access.Extractor{
		"matter.read": func(shared.ContextAddr, circulation.Intention) ([]access.ResourceAccess, error) {
			return nil, errors.New("ambiguous target")
		},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Extract("matter.write", "/root", circulation.Intention{}); err == nil {
		t.Fatal("expected missing extractor rejection")
	}
	if _, err := registry.Extract("matter.read", "/root", circulation.Intention{}); err == nil {
		t.Fatal("expected extractor error propagation")
	}
	if !registry.Has("matter.read") || registry.Has("matter.write") || (*access.Registry)(nil).Has("matter.read") {
		t.Fatal("Has did not report exact immutable registry membership")
	}
}

func TestNormalizeRejectsAmbiguousResourceCoordinates(t *testing.T) {
	valid := []access.ResourceAccess{
		{Kind: "matter", Context: "/root/catalog", ID: "conversation", Action: "append"},
		{Kind: "context", Context: "/root/catalog", Action: "start"},
	}
	for _, item := range valid {
		if got, err := access.Normalize(item); err != nil || got != item {
			t.Fatalf("Normalize(%#v) = %#v, %v", item, got, err)
		}
	}
	invalid := []access.ResourceAccess{
		{Kind: "matter", Context: "root/catalog", ID: "conversation", Action: "read"},
		{Kind: "matter", Context: "/root//catalog", ID: "conversation", Action: "read"},
		{Kind: "matter", Context: "/root/catalog", ID: "", Action: "read"},
		{Kind: "context", Context: "/root/catalog", ID: "catalog", Action: "start"},
		{Kind: "matter", Context: "/root/catalog", ID: "Conversation", Action: "read"},
		{Kind: "matter", Context: "/root/catalog", ID: "conversation", Action: ""},
	}
	for _, item := range invalid {
		if _, err := access.Normalize(item); err == nil {
			t.Fatalf("expected Normalize rejection for %#v", item)
		}
	}
}
