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

package secadmin

import (
	"fmt"

	"brique_engine/security/policysrc"
)

// Simulate answers one concrete question from the access source with the
// reference evaluator: may <principal> <verb> <target>? The target is a single
// resource, either a context (read, define, control) or one element
// (<context>#<genre>:<name>). It never reads the compiled policy: the property
// tests of policysrc guarantee the two agree.
func (d *Dir) Simulate(principal, verb, target string) (policysrc.Decision, error) {
	return d.SimulateAs(principal, "", verb, target)
}

// SimulateAs asks as one actor of the principal.
func (d *Dir) SimulateAs(principal, actor, verb, target string) (policysrc.Decision, error) {
	src, err := d.LoadSource()
	if err != nil {
		return policysrc.Decision{}, err
	}
	return SimulateSourceAs(src, principal, actor, verb, target)
}

// SimulateSource is Simulate over an already loaded source.
func SimulateSource(src *policysrc.Source, principal, verb, target string) (policysrc.Decision, error) {
	return SimulateSourceAs(src, principal, "", verb, target)
}

// SimulateSourceAs is SimulateAs over an already loaded source.
func SimulateSourceAs(src *policysrc.Source, principal, actor, verb, target string) (policysrc.Decision, error) {
	if _, exists := src.Principals[principal]; !exists {
		return policysrc.Decision{}, fmt.Errorf("unknown principal %q", principal)
	}
	t, err := policysrc.ParseTarget(target)
	if err != nil {
		return policysrc.Decision{}, err
	}
	if t.Subtree || t.Name == "*" {
		return policysrc.Decision{}, fmt.Errorf("simulate asks about one resource: give a context or <context>#<genre>:<name>, not %q", target)
	}
	kind, id := t.Genre, t.Name
	switch {
	case verb == policysrc.VerbCall && t.Genre != "capacity":
		return policysrc.Decision{}, fmt.Errorf("call applies to a capacity: <context>#capacity:<name>")
	case t.Genre == "" && (verb == policysrc.VerbRead || verb == policysrc.VerbDefine || verb == policysrc.VerbControl):
		kind, id = "context", ""
	case t.Genre == "":
		return policysrc.Decision{}, fmt.Errorf("%s applies to elements: <context>#<genre>:<name>", verb)
	}
	switch verb {
	case policysrc.VerbRead, policysrc.VerbWrite, policysrc.VerbCreate, policysrc.VerbCall, policysrc.VerbDefine, policysrc.VerbControl:
	default:
		return policysrc.Decision{}, fmt.Errorf("unknown verb %q", verb)
	}
	return src.ExplainAs(principal, actor, verb, t.Path, kind, id), nil
}
