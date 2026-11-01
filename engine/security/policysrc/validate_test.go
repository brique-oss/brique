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
	"strings"
	"testing"
)

func TestDecodeIsStrict(t *testing.T) {
	key := newTestKey(t).public
	body := `{"kind":"instance","keys":[{"public_key":"` + key + `"}],"grants":[{"read":"/root/a"}]}`
	if _, err := Decode([]byte(sourceDoc("p", body))); err != nil {
		t.Fatalf("valid source rejected: %v", err)
	}
	for name, doc := range map[string]string{
		"unknown field":        strings.Replace(sourceDoc("p", body), `"policy_id"`, `"bogus":"x","policy_id"`, 1),
		"duplicate key":        strings.Replace(sourceDoc("p", body), `"policy_id":"main"`, `"policy_id":"main","policy_id":"other"`, 1),
		"trailing data":        sourceDoc("p", body) + `{}`,
		"number":               sourceDoc("p", `{"kind":"instance","grants":[{"read":5}]}`),
		"target not a string":  sourceDoc("p", `{"kind":"instance","grants":[{"read":true}]}`),
		"not json":             `nope`,
		"unknown grant field":  sourceDoc("p", `{"kind":"instance","grants":[{"read":"/root","admin":true}]}`),
		"invalid utf-8 escape": sourceDoc("p", `{"kind":"instance","grants":[{"read":"\ud800"}]}`),
	} {
		if _, err := Decode([]byte(doc)); err == nil {
			t.Errorf("%s: should be rejected", name)
		}
	}
}

func TestTargetListAcceptsStringOrArray(t *testing.T) {
	key := newTestKey(t).public
	body := `{"kind":"instance","keys":[{"public_key":"` + key + `"}],"grants":[{"read":"/root/a"},{"read":["/root/b","/root/c"]}]}`
	src := mustDecodeSource(t, sourceDoc("p", body))
	if err := Validate(src); err != nil {
		t.Fatal(err)
	}
	g := src.Principals["p"].Grants
	if len(g[0].Read) != 1 || len(g[1].Read) != 2 {
		t.Fatalf("grants = %+v", g)
	}
}

func TestValidateRejectsInvalidSources(t *testing.T) {
	key := newTestKey(t).public
	other := newTestKey(t).public
	k := `"keys":[{"public_key":"` + key + `"}]`
	cases := map[string]string{
		"no kind":                        `{` + k + `}`,
		"bad kind":                       `{"kind":"robot",` + k + `}`,
		"enabled without key":            `{"kind":"instance","grants":[{"read":"/root"}]}`,
		"bad key encoding":               `{"kind":"instance","keys":[{"public_key":"!!"}]}`,
		"short key":                      `{"kind":"instance","keys":[{"public_key":"AAAA"}]}`,
		"bad key state":                  `{"kind":"instance","keys":[{"public_key":"` + key + `","state":"paused"}]}`,
		"duplicate key in principal":     `{"kind":"instance","keys":[{"public_key":"` + key + `"},{"public_key":"` + key + `"}]}`,
		"no verb":                        `{"kind":"instance",` + k + `,"grants":[{"except":["/root"]}]}`,
		"two verbs":                      `{"kind":"instance",` + k + `,"grants":[{"read":"/root","write":"/root"}]}`,
		"empty target list":              `{"kind":"instance",` + k + `,"grants":[{"read":[]}]}`,
		"bad target":                     `{"kind":"instance",` + k + `,"grants":[{"read":"/root/A"}]}`,
		"bad except":                     `{"kind":"instance",` + k + `,"grants":[{"read":"/root","except":["root/x"]}]}`,
		"write on capacity genre":        `{"kind":"instance",` + k + `,"grants":[{"write":"/root/a#capacity:x"}]}`,
		"create on capacity genre":       `{"kind":"instance",` + k + `,"grants":[{"create":"/root/a#capacity:x"}]}`,
		"define on matter genre":         `{"kind":"instance",` + k + `,"grants":[{"define":"/root/a#matter:x"}]}`,
		"control on an element":          `{"kind":"instance",` + k + `,"grants":[{"control":"/root/a#capacity:x"}]}`,
		"write on schema genre":          `{"kind":"instance",` + k + `,"grants":[{"write":"/root/a#schema:x"}]}`,
		"call on matter":                 `{"kind":"instance",` + k + `,"grants":[{"call":"/root/a#matter:x"}]}`,
		"call named with subtree":        `{"kind":"instance",` + k + `,"grants":[{"call":"/root/a/**#capacity:x"}]}`,
		"actor id invalid":               `{"kind":"instance",` + k + `,"actors":{"Bad Name":{"grants":[]}}}`,
		"actor grant invalid":            `{"kind":"instance",` + k + `,"actors":{"alice":{"grants":[{"read":"/root/A"}]}}}`,
		"actor grant two verbs":          `{"kind":"instance",` + k + `,"actors":{"alice":{"grants":[{"read":"/root","write":"/root"}]}}}`,
		"actor grant control on element": `{"kind":"instance",` + k + `,"actors":{"alice":{"grants":[{"control":"/root/a#capacity:x"}]}}}`,
		"local without prefix":           `{"kind":"local","grants":[{"read":"/root/a"}]}`,
		"local with a key":               `{"kind":"local","keys":[{"public_key":"` + key + `"}]}`,
		"local with actors":              `{"kind":"local","actors":{"a":{"grants":[]}}}`,
		"two verbs create and read":      `{"kind":"instance",` + k + `,"grants":[{"read":"/root","create":"/root"}]}`,
	}
	for name, body := range cases {
		if err := Validate(mustDecodeSource(t, sourceDoc("p", body))); err == nil {
			t.Errorf("%s: should be rejected", name)
		} else if IsUnsupported(err) {
			t.Errorf("%s: reported as unsupported instead of invalid: %v", name, err)
		}
	}
	// Key reuse across principals.
	two := `{"format":"brique-access-source","version":"1","instance_id":"i","policy_id":"main","principals":{` +
		`"a":{"kind":"instance","keys":[{"public_key":"` + key + `"}]},"b":{"kind":"instance","keys":[{"public_key":"` + key + `"}]}}}`
	if err := Validate(mustDecodeSource(t, two)); err == nil {
		t.Error("a key shared by two principals should be rejected")
	}
	_ = other
	// Header checks.
	for name, doc := range map[string]string{
		"wrong format":  strings.Replace(sourceDoc("p", `{"kind":"instance",`+k+`}`), `brique-access-source`, `other`, 1),
		"wrong version": strings.Replace(sourceDoc("p", `{"kind":"instance",`+k+`}`), `"version":"1"`, `"version":"2"`, 1),
		"bad instance":  strings.Replace(sourceDoc("p", `{"kind":"instance",`+k+`}`), `test-instance`, `Test Instance`, 1),
		"bad principal": sourceDoc("Bad Name", `{"kind":"instance",`+k+`}`),
	} {
		if err := Validate(mustDecodeSource(t, doc)); err == nil {
			t.Errorf("%s: should be rejected", name)
		}
	}
}

// JSON null is refused by the strict decoder before validation runs.
func TestDecodeRefusesNull(t *testing.T) {
	if _, err := Decode([]byte(sourceDoc("p", `null`))); err == nil {
		t.Fatal("null principal accepted")
	}
}

func TestValidateAcceptsDisabledPrincipalWithoutKeys(t *testing.T) {
	src := mustDecodeSource(t, sourceDoc("p", `{"kind":"service","disabled":true}`))
	if err := Validate(src); err != nil {
		t.Fatalf("disabled principal without keys: %v", err)
	}
}

// Every verb of the model is accepted, in every form its grammar allows.
func TestValidateAcceptsTheSixVerbs(t *testing.T) {
	key := newTestKey(t).public
	k := `"keys":[{"public_key":"` + key + `"}]`
	for name, grant := range map[string]string{
		"read":         `{"read":"/root/a/**","except":["/root/a/s#matter:x"]}`,
		"write":        `{"write":["/root/a#matter:x","/root/b#structure:*"]}`,
		"create":       `{"create":"/root/inbox#matter:*"}`,
		"call context": `{"call":"/root/a"}`,
		"call subtree": `{"call":"/root/a/**"}`,
		"call named":   `{"call":"/root/a#capacity:f"}`,
		"call all":     `{"call":"/root/a/**#capacity:*","except":["/root/a/x#capacity:g"]}`,
		"define":       `{"define":["/root/a/**","/root/b#schema:s"]}`,
		"control":      `{"control":"/root/a/**","except":["/root/a/db"]}`,
	} {
		body := `{"kind":"service",` + k + `,"grants":[` + grant + `]}`
		if err := Validate(mustDecodeSource(t, sourceDoc("p", body))); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestValidateAcceptsActorsWithTheirOwnGrants(t *testing.T) {
	key := newTestKey(t).public
	body := `{"kind":"service","keys":[{"public_key":"` + key + `"}],"grants":[{"read":"/root/a/**"}],` +
		`"actors":{"alice":{"grants":[{"read":"/root/a/x/**","except":["/root/a/x/s"]}]},"default":{"grants":[{"read":"/root/a/public"}]},"nobody":{"grants":[]}}}`
	if err := Validate(mustDecodeSource(t, sourceDoc("p", body))); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAcceptsALocalPrincipalWithoutKeyAndRefusesTheReservedPrefixElsewhere(t *testing.T) {
	src := mustDecodeSource(t, `{"format":"brique-access-source","version":"1","instance_id":"i","policy_id":"main","principals":{`+
		`"local.pilot":{"kind":"local","grants":[{"read":"/root/a/**"},{"define":"/root/a/**"}]}}}`)
	if err := Validate(src); err != nil {
		t.Fatal(err)
	}
	key := newTestKey(t).public
	bad := mustDecodeSource(t, `{"format":"brique-access-source","version":"1","instance_id":"i","policy_id":"main","principals":{`+
		`"local.sneaky":{"kind":"service","keys":[{"public_key":"`+key+`"}]}}}`)
	if err := Validate(bad); err == nil {
		t.Fatal("a keyed principal took the reserved local. prefix")
	}
}
