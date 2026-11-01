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

package policysrc

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"testing"

	"brique_engine/security/trust"
)

func ownerSource(t *testing.T, grants ...Grant) (*Source, map[string]testKey) {
	t.Helper()
	key := newTestKey(t)
	return &Source{Format: Format, Version: Version, InstanceID: "inst", PolicyID: "main", Principals: map[string]*Principal{
		"partner":       {Kind: "instance", Keys: []Key{{PublicKey: key.public}}, Grants: grants},
		"local.reports": {Kind: "local", Grants: []Grant{{Read: TargetList{"/root/**"}}}},
		"local.other":   {Kind: "local", Grants: []Grant{{Read: TargetList{"/root/**"}}}},
	}}, map[string]testKey{"partner": key}
}

func facade(owner string, targets ...string) Grant {
	return Grant{Call: TargetList(targets), AsOwner: owner}
}

func TestAsOwnerCompilesToFormat3OnlyWhenUsed(t *testing.T) {
	plain, _ := ownerSource(t, call("/root/reports#capacity:report.generate"))
	result, err := Compile(plain, testCatalog(), Options{Revision: "1", IssuedAt: "2026-10-24T12:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Policy.FormatVersion != trust.PolicyFormatVersionV2 {
		t.Fatalf("a source without as_owner is format %s, want 2 so older engines keep reading it", result.Policy.FormatVersion)
	}
	owned, keys := ownerSource(t, facade("local.reports", "/root/reports#capacity:report.generate"))
	result, err = Compile(owned, testCatalog(), Options{Revision: "1", IssuedAt: "2026-10-24T12:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Policy.FormatVersion != trust.PolicyFormatVersionV3 {
		t.Fatalf("format = %s, want 3", result.Policy.FormatVersion)
	}
	c := compileToSnapshot(t, owned, keys)
	if owner, ok := c.snapshot.OwnerFor(c.admissions["partner"], "/root/reports", "report.generate"); !ok || owner != "local.reports" {
		t.Fatalf("owner = %q, %v", owner, ok)
	}
	// The call right itself is the ordinary capacity/execute resource.
	if !c.resourceVerdict(t, "partner", VerbCall, "/root/reports", "capacity", "report.generate") {
		t.Fatal("as_owner removed the right to call")
	}
}

func TestAsOwnerIsValidated(t *testing.T) {
	cases := map[string]func() (*Source, error){
		"on a read grant": func() (*Source, error) {
			src, _ := ownerSource(t, Grant{Read: TargetList{"/root/a"}, AsOwner: "local.reports"})
			return src, nil
		},
		"unknown owner": func() (*Source, error) {
			src, _ := ownerSource(t, facade("local.ghost", "/root/a#capacity:f"))
			return src, nil
		},
		"owner is a keyed principal": func() (*Source, error) {
			src, _ := ownerSource(t, facade("partner", "/root/a#capacity:f"))
			return src, nil
		},
		"in an actor's grants": func() (*Source, error) {
			src, _ := ownerSource(t)
			src.Principals["partner"].Actors = map[string]*Actor{"alice": {Grants: []Grant{facade("local.reports", "/root/a#capacity:f")}}}
			return src, nil
		},
		"an owner that uses as_owner itself": func() (*Source, error) {
			src, _ := ownerSource(t, facade("local.reports", "/root/a#capacity:f"))
			src.Principals["local.reports"].Grants = append(src.Principals["local.reports"].Grants, facade("local.other", "/root/b#capacity:g"))
			return src, nil
		},
	}
	for name, build := range cases {
		src, _ := build()
		if err := Validate(src); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good, _ := ownerSource(t, facade("local.reports", "/root/a#capacity:f"))
	if err := Validate(good); err != nil {
		t.Fatalf("a valid facade was refused: %v", err)
	}
}

// The property: the owner the compiled snapshot picks equals the oracle's, on random
// call grants with or without as_owner, exceptions included, for every context and
// capacity of the grid.
func TestPropertyOwnerMatchesOracle(t *testing.T) {
	sources := 300
	if raw := os.Getenv("BRIQUE_PROP_SOURCES"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			sources = n
		}
	}
	r := rand.New(rand.NewSource(20261101))
	owners := []string{"", "local.reports", "local.other"}
	asked, owned := 0, 0
	for i := 0; i < sources; i++ {
		var grants []Grant
		for n := 1 + r.Intn(5); n > 0; n-- {
			g := Grant{Call: TargetList{randTarget(r, VerbCall)}, AsOwner: owners[r.Intn(len(owners))]}
			if r.Intn(2) == 0 {
				g.Call = append(g.Call, randTarget(r, VerbCall))
			}
			for x := r.Intn(2); x > 0; x-- {
				g.Except = append(g.Except, randTarget(r, VerbCall))
			}
			grants = append(grants, g)
		}
		src, keys := ownerSource(t, grants...)
		c := compileToSnapshot(t, src, keys)
		for _, ctx := range propRequests {
			for _, name := range propUser {
				want := src.OwnerOf("partner", "", ctx, name)
				got, ok := "", false
				if c.resourceVerdict(t, "partner", VerbCall, ctx, "capacity", name) {
					got, ok = c.snapshot.OwnerFor(c.admissions["partner"], ctx, name)
				}
				if ok != (want != "") || got != want {
					t.Fatalf("source %d, %s %s: snapshot owner %q (%v), oracle %q\n%s", i, ctx, name, got, ok, want, dumpSource(src, "partner"))
				}
				asked++
				if want != "" {
					owned++
				}
			}
		}
	}
	if owned < asked/20 {
		t.Fatalf("the generator is too weak: only %d of %d answers have an owner", owned, asked)
	}
	fmt.Printf("owner property: %d identical answers, %d of them with an owner\n", asked, owned)
}
