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

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/matter"
	"brique_engine/reflexive"
	"brique_engine/security/access"
	"brique_engine/security/policysrc"
	"brique_engine/security/trust"
	"brique_engine/shared"
)

// The door-level property test. For random access sources, the decision the engine
// reaches for a real request — the capability door of the family, then the resources
// its REAL extractor returns for that request, each authorized against the compiled
// snapshot — must equal the decision of an independent evaluator of the source applied
// to the same resources. The source is written freely: verbs, subtrees, sub-contexts,
// named elements, genres, exceptions inside granted subtrees. The mapping from a
// resource action to the verb that grants it is written here by hand from the model, not
// taken from the compiler.

var doorContexts = []string{"/root", "/root/a", "/root/a/b", "/root/a/s", "/root/ab", "/root/z"}

var doorNames = map[string][]string{
	"matter": {"m1", "m2"}, "structure": {"s1"}, "capacity": {"x", "y"}, "schema": {"x"}, "document": {"x", "y"},
}

var verbGenres = map[string][]string{
	"read":   {"capacity", "schema", "document", "matter", "structure"},
	"write":  {"matter", "structure"},
	"create": {"matter", "structure"},
	"define": {"capacity", "schema", "document"},
	"call":   {"capacity"},
}

// expectedVerb is the model's table (SECURITY.md §2): which verb grants a resource action.
func expectedVerb(kind, action string) string {
	switch kind {
	case "matter", "structure":
		switch action {
		case "read", "stat":
			return "read"
		case "write", "append", "truncate", "delete":
			return "write"
		case "create":
			return "create"
		}
	case "capacity", "schema", "document":
		switch action {
		case "read":
			return "read"
		case "create", "write", "delete":
			return "define"
		}
	case "context":
		switch action {
		case "read", "stat", "read_recursive":
			return "read"
		case "write", "create", "delete":
			return "define"
		case "control":
			return "control"
		}
	case "trace":
		if action == "read" || action == "payloads" {
			return "read"
		}
	}
	return ""
}

func randomDoorTarget(random *rand.Rand, verb string) string {
	path := doorContexts[random.Intn(len(doorContexts))]
	target := path
	if random.Intn(2) == 0 {
		target += "/**"
	}
	genres := verbGenres[verb]
	if len(genres) == 0 || random.Intn(2) == 0 {
		return target
	}
	genre := genres[random.Intn(len(genres))]
	if random.Intn(3) == 0 {
		return target + "#" + genre + ":*"
	}
	names := doorNames[genre]
	// A named element addresses one context, never a subtree (the compiler refuses the mix).
	return path + "#" + genre + ":" + names[random.Intn(len(names))]
}

func randomDoorSource(random *rand.Rand, publicKey string) *policysrc.Source {
	principal := &policysrc.Principal{Kind: "instance", Keys: []policysrc.Key{{PublicKey: publicKey}}, TracePayloads: random.Intn(2) == 0}
	verbs := []string{"read", "read", "write", "create", "define", "control", "call"}
	for n := 1 + random.Intn(5); n > 0; n-- {
		verb := verbs[random.Intn(len(verbs))]
		grant := policysrc.Grant{}
		targets := policysrc.TargetList{randomDoorTarget(random, verb)}
		if verb == "control" {
			targets = policysrc.TargetList{doorContexts[random.Intn(len(doorContexts))] + []string{"", "/**"}[random.Intn(2)]}
		}
		switch verb {
		case "read":
			grant.Read = targets
		case "write":
			grant.Write = targets
		case "create":
			grant.Create = targets
		case "define":
			grant.Define = targets
		case "control":
			grant.Control = targets
		case "call":
			grant.Call = targets
		}
		for x := random.Intn(3); x > 0; x-- {
			if verb == "control" {
				grant.Except = append(grant.Except, doorContexts[random.Intn(len(doorContexts))]+[]string{"", "/**"}[random.Intn(2)])
			} else {
				grant.Except = append(grant.Except, randomDoorTarget(random, verb))
			}
		}
		principal.Grants = append(principal.Grants, grant)
	}
	return &policysrc.Source{Format: policysrc.Format, Version: policysrc.Version, InstanceID: "door", PolicyID: "main",
		Principals: map[string]*policysrc.Principal{"p": principal}}
}

func compileDoorSnapshot(t *testing.T, src *policysrc.Source, keyID string) (*trust.Snapshot, trust.Admission, error) {
	t.Helper()
	catalog := trust.Catalog{Capabilities: map[string][]string{}, ResourceActions: trust.EngineResourceActions()}
	result, err := policysrc.Compile(src, catalog, policysrc.Options{Revision: "1", IssuedAt: "2026-11-01T10:00:00Z"})
	if err != nil {
		return nil, trust.Admission{}, err
	}
	authorityPublic, authorityPrivate, _ := ed25519.GenerateKey(nil)
	authorityID, _ := trust.SigningKeyID(authorityPublic)
	raw, _ := json.Marshal(result.Policy)
	canonical, err := trust.CanonicalPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := trust.SignPolicy(canonical, authorityPrivate)
	envelope, _ := json.Marshal(trust.Envelope{Policy: canonical, Proofs: []trust.Proof{{AuthorityKeyID: authorityID, Algorithm: trust.SigningAlgorithm, Signature: proof}}})
	decoded, err := trust.VerifyAndDecode(envelope, trust.Authority{KeyID: authorityID, PublicKey: authorityPublic})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := trust.Compile(decoded, catalog)
	if err != nil {
		t.Fatalf("the snapshot refuses the compiled policy: %v", err)
	}
	admission, err := snapshot.Authenticate(keyID, time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, admission, nil
}

// modelResources is the hand-written model of what each capacity touches, per
// SECURITY.md §2 and §5: the resources the engine must ask the snapshot about for the
// given request. It is deliberately independent of the extractors, so that an extractor
// asking for the wrong verb, context or element is caught instead of followed.
func modelResources(name, destination string, params map[string]any) []string {
	res := func(kind, context, id, action string) string { return kind + "|" + context + "|" + id + "|" + action }
	str := func(key string) string { value, _ := params[key].(string); return value }
	items := func() []map[string]any {
		var out []map[string]any
		list, _ := params[circulation.KeyItems].([]any)
		for _, raw := range list {
			out = append(out, raw.(map[string]any))
		}
		return out
	}
	child := func(parent, leaf string) string { return strings.TrimSuffix(parent, "/") + "/" + leaf }
	var out []string
	switch name {
	case "matter.create", "matter.derive":
		if name == "matter.derive" {
			guards, _ := params[circulation.KeySourceGuards].([]any)
			for _, raw := range guards {
				out = append(out, res("matter", destination, raw.(map[string]any)[circulation.KeyMatterID].(string), "read"))
			}
			out = append(out, res("matter", destination, str(circulation.KeyTargetMatterID), "create"))
		} else {
			out = append(out, res("matter", destination, str(circulation.KeyMatterID), "create"))
		}
	case "structure.create":
		out = append(out, res("structure", destination, str(circulation.KeyStructureID), "create"))
	case "structure.derive":
		out = append(out, res("structure", destination, str(circulation.KeyTargetStructureID), "create"))
	case "matter.delete", "matter.read", "matter.write", "matter.append", "matter.truncate", "matter.subscribe":
		action := strings.TrimPrefix(name, "matter.")
		if name == "matter.subscribe" {
			action = "read"
		}
		out = append(out, res("matter", destination, str(circulation.KeyMatterID), action))
	case "matter.stat", "matter.exists":
		out = append(out, res("matter", destination, str(circulation.KeyMatterID), "stat"))
	case "structure.delete", "structure.read":
		out = append(out, res("structure", destination, str(circulation.KeyStructureID), strings.TrimPrefix(name, "structure.")))
	case "structure.patch":
		out = append(out, res("structure", destination, str(circulation.KeyStructureID), "write"))
	case "matter.read_batch":
		for _, id := range params[circulation.KeyMatterIDs].([]any) {
			out = append(out, res("matter", destination, id.(string), "read"))
		}
	case "matter.clone":
		out = append(out, res("matter", destination, str(circulation.KeySourceMatterID), "read"), res("matter", destination, str(circulation.KeyTargetMatterID), "create"))
	case "structure.clone":
		out = append(out, res("structure", destination, str(circulation.KeySourceStructureID), "read"), res("structure", destination, str(circulation.KeyTargetStructureID), "create"))
	case "matter.unsubscribe", "matter.subscription.ack", "edit.get_element_template":
		// no world resource
	case "read.state":
		out = append(out, res("context", destination, "", "stat"))
	case "read.structure", "meaning.query":
		out = append(out, res("context", destination, "", "read_recursive"))
	case "meaning.rebuild", "meaning.update":
		out = append(out, res("context", destination, "", "control"))
	case "vocabulary.get", "vocabulary.query":
		out = append(out, res("context", "/root", "", "read_recursive"))
	case "vocabulary.patch", "vocabulary.delete":
		out = append(out, res("context", "/root", "", "write"))
	case "trace.inspect":
		out = append(out, res("trace", destination, "events", "read"))
		if wanted, _ := params[circulation.KeyIncludePayloads].(bool); wanted {
			out = append(out, res("trace", destination, "events", "payloads"))
		}
	case "read.capacity":
		out = append(out, res("capacity", destination, str("cap_name"), "read"))
	case "read.document":
		for _, item := range items() {
			out = append(out, res("document", destination, item[circulation.KeyName].(string), "read"))
		}
	case "read.meaning":
		for _, raw := range params[circulation.KeyInput].([]any) {
			item := raw.(map[string]any)
			out = append(out, res(item[circulation.KeyElementKind].(string), destination, item[circulation.KeyElementName].(string), "read"))
		}
	case "edit.create", "edit.delete", "edit.patch_meaning":
		action := map[string]string{"edit.create": "create", "edit.delete": "delete", "edit.patch_meaning": "write"}[name]
		for _, item := range items() {
			kind, leaf := item[circulation.KeyType].(string), item[circulation.KeyName].(string)
			if kind == "context" {
				if action == "write" {
					out = append(out, res("context", destination, "", "write"))
				} else {
					out = append(out, res("context", child(destination, leaf), "", action), res("context", destination, "", "write"))
				}
				continue
			}
			out = append(out, res(kind, destination, leaf, action))
		}
	case "edit.duplicate":
		for _, item := range items() {
			kind := item[circulation.KeyType].(string)
			source, target := item[circulation.KeySourceName].(string), item[circulation.KeyTargetName].(string)
			if kind == "context" {
				out = append(out, res("context", child(destination, source), "", "read"), res("context", child(destination, target), "", "create"), res("context", destination, "", "write"))
				continue
			}
			out = append(out, res(kind, destination, source, "read"), res(kind, destination, target, "create"))
		}
	default:
		panic("no model for " + name)
	}
	sort.Strings(out)
	return out
}

func describeAccesses(accesses []access.ResourceAccess) []string {
	out := make([]string, 0, len(accesses))
	for _, item := range accesses {
		out = append(out, item.Kind+"|"+string(item.Context)+"|"+item.ID+"|"+item.Action)
	}
	sort.Strings(out)
	return out
}

type doorProbe struct {
	label  string
	params map[string]any
}

func doorProbes() map[string]map[string][]doorProbe {
	byName := func(base map[string]any, key string, values ...string) []doorProbe {
		var out []doorProbe
		for _, value := range values {
			params := map[string]any{}
			for k, v := range base {
				params[k] = v
			}
			params[key] = value
			out = append(out, doorProbe{key + "=" + value, params})
		}
		return out
	}
	matterProbes := map[string][]doorProbe{}
	for _, name := range []string{"matter.create", "matter.delete", "matter.read", "matter.stat", "matter.exists", "matter.write",
		"matter.append", "matter.truncate", "matter.subscribe", "matter.unsubscribe", "matter.subscription.ack"} {
		matterProbes[name] = byName(nil, circulation.KeyMatterID, "m1", "m2")
	}
	for _, name := range []string{"structure.create", "structure.delete", "structure.read", "structure.patch"} {
		matterProbes[name] = byName(nil, circulation.KeyStructureID, "s1")
	}
	matterProbes["matter.read_batch"] = []doorProbe{{"m1,m2", map[string]any{circulation.KeyMatterIDs: []any{"m1", "m2"}}}}
	matterProbes["matter.clone"] = []doorProbe{{"m1->m2", map[string]any{circulation.KeySourceMatterID: "m1", circulation.KeyTargetMatterID: "m2"}}}
	matterProbes["matter.derive"] = []doorProbe{{"guard m1 -> m2", map[string]any{
		circulation.KeyTargetMatterID: "m2", circulation.KeySourceGuards: []any{map[string]any{circulation.KeyMatterID: "m1"}}}}}
	matterProbes["structure.clone"] = []doorProbe{{"s1->s1", map[string]any{circulation.KeySourceStructureID: "s1", circulation.KeyTargetStructureID: "s1"}}}
	matterProbes["structure.derive"] = []doorProbe{{"-> s1", map[string]any{circulation.KeyTargetStructureID: "s1"}}}

	element := func(kind, name string) map[string]any {
		return map[string]any{circulation.KeyItems: []any{map[string]any{circulation.KeyType: kind, circulation.KeyName: name}}}
	}
	duplicate := func(kind, source, target string) map[string]any {
		return map[string]any{circulation.KeyItems: []any{map[string]any{circulation.KeyType: kind, circulation.KeySourceName: source, circulation.KeyTargetName: target}}}
	}
	reflexiveProbes := map[string][]doorProbe{
		"read.state": {{"", map[string]any{}}}, "read.structure": {{"", map[string]any{}}}, "meaning.query": {{"", map[string]any{}}},
		"meaning.rebuild": {{"", map[string]any{}}}, "meaning.update": {{"", map[string]any{}}},
		"vocabulary.get": {{"", map[string]any{}}}, "vocabulary.query": {{"", map[string]any{}}},
		"vocabulary.patch": {{"", map[string]any{}}}, "vocabulary.delete": {{"", map[string]any{}}},
		"edit.get_element_template": {{"", map[string]any{}}},
		"trace.inspect":             {{"events", map[string]any{}}, {"payloads", map[string]any{circulation.KeyIncludePayloads: true}}},
		"read.capacity":             {{"x", map[string]any{"cap_name": "x"}}, {"y", map[string]any{"cap_name": "y"}}},
		"read.document": {{"x", map[string]any{circulation.KeyItems: []any{map[string]any{circulation.KeyName: "x"}}}},
			{"y", map[string]any{circulation.KeyItems: []any{map[string]any{circulation.KeyName: "y"}}}}},
		"read.meaning": {{"capacity x", map[string]any{circulation.KeyInput: []any{map[string]any{circulation.KeyElementKind: "capacity", circulation.KeyElementName: "x"}}}},
			{"document y", map[string]any{circulation.KeyInput: []any{map[string]any{circulation.KeyElementKind: "document", circulation.KeyElementName: "y"}}}}},
	}
	for _, name := range []string{"edit.create", "edit.delete", "edit.patch_meaning"} {
		var probes []doorProbe
		for _, kind := range []string{"capacity", "schema", "document"} {
			probes = append(probes, doorProbe{kind + " x", element(kind, "x")})
		}
		probes = append(probes, doorProbe{"child b", element("context", "b")}, doorProbe{"child s", element("context", "s")})
		reflexiveProbes[name] = probes
	}
	reflexiveProbes["edit.duplicate"] = []doorProbe{
		{"capacity x->y", duplicate("capacity", "x", "y")}, {"document x->y", duplicate("document", "x", "y")},
		{"context b->s", duplicate("context", "b", "s")},
	}
	return map[string]map[string][]doorProbe{"matter": matterProbes, "reflexive": reflexiveProbes}
}

// expectedDoor is the independent decision: the verb of the capacity's class must be
// held on the addressed context, and every resource must be granted by its verb.
func expectedDoor(src *policysrc.Source, family, name string, class access.Class, destination string, accesses []access.ResourceAccess) bool {
	held := false
	for _, verb := range class.Verbs() {
		held = held || src.VerbHeld("p", verb, destination)
	}
	if !held {
		return false
	}
	for _, item := range accesses {
		context := string(item.Context)
		verb := expectedVerb(item.Kind, item.Action)
		if verb == "" {
			return false // an action the model does not know is never granted
		}
		switch {
		case item.Kind == "context" && item.Action == "read_recursive":
			if !src.ReadRecursiveAllowed("p", context) {
				return false
			}
		case item.Kind == "trace" && item.Action == "payloads":
			if !src.TracePayloadsAllowed("p", context) {
				return false
			}
		default:
			if !src.Allowed("p", verb, context, item.Kind, item.ID) {
				return false
			}
		}
	}
	return true
}

func TestDoorDecisionsMatchAnIndependentEvaluatorForAnyConfiguration(t *testing.T) {
	sources := 300
	if raw := os.Getenv("BRIQUE_PROPERTY_ITERATIONS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			sources = n
		}
	}
	registries := map[string]*access.Registry{"matter": matter.AccessRegistry(), "reflexive": reflexive.AccessRegistry()}
	probes := doorProbes()
	random := rand.New(rand.NewSource(20261101))
	decisions, allowed, skipped := 0, 0, 0
	reasons := map[string]int{}
	covered := map[string]bool{}
	for index := 0; index < sources; index++ {
		publicKey, _, _ := ed25519.GenerateKey(nil)
		keyID, _ := trust.SigningKeyID(publicKey)
		src := randomDoorSource(random, base64.RawURLEncoding.EncodeToString(publicKey))
		snapshot, admission, err := compileDoorSnapshot(t, src, keyID)
		if err != nil {
			reasons[err.Error()]++
			skipped++ // a random source the compiler refuses (for example an exception that covers no grant)
			continue
		}
		for family, registry := range registries {
			names := make([]string, 0)
			for name := range access.EngineCapacities(family) {
				if registry.Has(name) {
					names = append(names, name)
				}
			}
			sort.Strings(names)
			for _, name := range names {
				class, _ := access.ClassOf(family, name)
				variants := probes[family][name]
				if len(variants) == 0 {
					t.Fatalf("%s/%s has no probe: add one to doorProbes", family, name)
				}
				for _, variant := range variants {
					for _, destination := range doorContexts {
						intention := circulation.Intention{IntentionID: "probe", To: circulation.Address{Context: circulation.ContextID(destination), Cap: name, Type: family}, Params: variant.params}
						accesses, err := registry.Extract(name, shared.ContextAddr(destination), intention)
						if err != nil {
							t.Fatalf("%s/%s (%s) at %s: %v", family, name, variant.label, destination, err)
						}
						if have, want := describeAccesses(accesses), modelResources(name, destination, variant.params); strings.Join(have, ";") != strings.Join(want, ";") {
							t.Fatalf("%s/%s (%s) at %s: the extractor asks for %v, the model says %v", family, name, variant.label, destination, have, want)
						}
						got := snapshot.AuthorizeCapabilityVerbs(admission, destination, family, name, class.Verbs())
						for _, item := range accesses {
							got = got && snapshot.AuthorizeResource(admission, string(item.Context), item.Kind, item.ID, item.Action)
						}
						want := expectedDoor(src, family, name, class, destination, accesses)
						decisions++
						if got {
							allowed++
							covered[family+"/"+name] = true
						}
						if got != want {
							dump, _ := json.MarshalIndent(src.Principals["p"], "", " ")
							t.Fatalf("source %d: %s/%s (%s) at %s: engine=%v, evaluator=%v, resources=%v\n%s",
								index, family, name, variant.label, destination, got, want, accesses, dump)
						}
					}
				}
			}
		}
	}
	// The generator must reach both outcomes for most capacities, or the test proves nothing.
	if allowed < decisions/50 || len(covered) < 30 {
		t.Fatalf("weak generator: %d of %d decisions allowed, %d capacities ever granted", allowed, decisions, len(covered))
	}
	for reason, n := range reasons {
		t.Logf("compiler refusal x%d: %.140s", n, reason)
	}
	fmt.Printf("door property: %d decisions over %d sources (%d refused by the compiler), %d allowed, %d capacities granted at least once\n",
		decisions, sources, skipped, allowed, len(covered))
}
