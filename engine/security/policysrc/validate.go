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

// validate.go
//
// Meaning-level validation of a source. Everything the compiler cannot yet turn
// into a signed policy is REFUSED here, never accepted and ignored (review item
// I8): a source that uses it fails with an UnsupportedError naming the phase that
// delivers it.

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"

	"brique_engine/security/trust"
)

const (
	maxPrincipals       = 1024
	maxKeysPerPrincipal = 8
	maxGrants           = 256
	maxTargetsPerGrant  = 64
	maxExcepts          = 64
	maxActors           = 256
)

// UnsupportedError reports a feature of the model that is specified but not yet
// compiled.
type UnsupportedError struct {
	Feature string
	Phase   int
	Where   string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("%s: %s is not supported yet (phase %d)", e.Where, e.Feature, e.Phase)
}

// IsUnsupported reports whether err is (or wraps) an UnsupportedError.
func IsUnsupported(err error) bool {
	var u *UnsupportedError
	return errors.As(err, &u)
}

func unsupported(where, feature string, phase int) error {
	return &UnsupportedError{Feature: feature, Phase: phase, Where: where}
}

// verbGenres lists the genres a verb may name explicitly (model §5.3).
var verbGenres = map[string]map[string]bool{
	VerbRead:   {"capacity": true, "schema": true, "document": true, "matter": true, "structure": true},
	VerbWrite:  {"matter": true, "structure": true},
	VerbCreate: {"matter": true, "structure": true},
	VerbDefine: {"capacity": true, "schema": true, "document": true},
	VerbCall:   {"capacity": true},
	// control names whole contexts only: no genre.
	VerbControl: {},
}

// Validate checks a decoded source.
func Validate(src *Source) error {
	if src == nil {
		return fmt.Errorf("access source: nil")
	}
	if src.Format != Format || src.Version != Version {
		return fmt.Errorf("access source: format must be %q version %q", Format, Version)
	}
	if !identifierPattern.MatchString(src.InstanceID) {
		return fmt.Errorf("access source: instance_id %q is not a valid identifier", src.InstanceID)
	}
	if !identifierPattern.MatchString(src.PolicyID) {
		return fmt.Errorf("access source: policy_id %q is not a valid identifier", src.PolicyID)
	}
	if src.Principals == nil {
		return fmt.Errorf("access source: principals is required")
	}
	if len(src.Principals) > maxPrincipals {
		return fmt.Errorf("access source: more than %d principals", maxPrincipals)
	}
	seenKeys := map[string]string{}
	for _, id := range sortedPrincipalIDs(src) {
		if err := validatePrincipal(id, src.Principals[id], seenKeys); err != nil {
			return err
		}
	}
	if err := validateOwners(src); err != nil {
		return err
	}
	return validatePeers(src)
}

// validatePeers checks that every peer is a principal of the source that has a key to
// verify its answers with, and that none is declared twice.
func validatePeers(src *Source) error {
	seen := map[string]bool{}
	for index, peer := range src.Peers {
		principal := src.Principals[peer.PrincipalID]
		switch {
		case principal == nil:
			return fmt.Errorf("access source: peer %d: %q is not a principal", index, peer.PrincipalID)
		case principal.Kind == "local":
			return fmt.Errorf("access source: peer %d: %q is a local principal and has no remote instance", index, peer.PrincipalID)
		case seen[peer.PrincipalID]:
			return fmt.Errorf("access source: peer %d: %q is declared twice", index, peer.PrincipalID)
		case !identifierPattern.MatchString(peer.AudienceInstanceID) || peer.Interface == "":
			return fmt.Errorf("access source: peer %d (%q): audience and interface are required", index, peer.PrincipalID)
		}
		if len(peer.Endpoints) == 0 && peer.Target == "" {
			return fmt.Errorf("access source: peer %d (%q): a target, or endpoints, are required", index, peer.PrincipalID)
		}
		if err := checkPeerEndpoints(index, peer, principal); err != nil {
			return err
		}
		seen[peer.PrincipalID] = true
	}
	return nil
}

// checkPeerEndpoints says early what the signed policy would refuse: a peer is reached on one
// target or on named endpoints, and every key an endpoint names is a key of the principal.
func checkPeerEndpoints(index int, peer trust.ExternalPeer, principal *Principal) error {
	if len(peer.Endpoints) == 0 {
		return nil
	}
	if peer.Target != "" {
		return fmt.Errorf("access source: peer %d (%q): a target and endpoints are exclusive", index, peer.PrincipalID)
	}
	own := make(map[string]bool, len(principal.Keys))
	for _, key := range principal.Keys {
		raw, err := base64.RawURLEncoding.DecodeString(key.PublicKey)
		if err != nil {
			continue // the key itself is reported by the principal's own validation
		}
		if id, err := trust.SigningKeyID(ed25519.PublicKey(raw)); err == nil {
			own[id] = true
		}
	}
	bound := map[string]string{}
	for _, endpoint := range peer.Endpoints {
		for _, keyID := range endpoint.KeyIDs {
			switch {
			case !own[keyID]:
				return fmt.Errorf("access source: peer %d (%q): endpoint %q names a key (%s) that is not one of the principal's", index, peer.PrincipalID, endpoint.Name, keyID)
			case bound[keyID] != "":
				return fmt.Errorf("access source: peer %d (%q): a key (%s) lives on endpoints %q and %q", index, peer.PrincipalID, keyID, bound[keyID], endpoint.Name)
			}
			bound[keyID] = endpoint.Name
		}
	}
	return nil
}

// validateOwners checks every as_owner (model §12): it names a local principal of
// the source other than the grantee, it is written on the principal's own grants
// (an actor can only narrow, never lend another identity), and an owner does not
// itself hand authority to another owner.
func validateOwners(src *Source) error {
	owners := map[string]bool{}
	for _, id := range sortedPrincipalIDs(src) {
		p := src.Principals[id]
		for _, actor := range p.Actors {
			for i, g := range actor.Grants {
				if g.AsOwner != "" {
					return fmt.Errorf("principal %q actor grant %d: as_owner belongs to the principal's own grants, not an actor's", id, i)
				}
			}
		}
		for i, g := range p.Grants {
			if g.AsOwner == "" {
				continue
			}
			where := fmt.Sprintf("principal %q grants[%d]", id, i)
			owner := src.Principals[g.AsOwner]
			if owner == nil || owner.Kind != "local" {
				return fmt.Errorf("%s: as_owner %q must be a local principal of this source", where, g.AsOwner)
			}
			if g.AsOwner == id {
				return fmt.Errorf("%s: a principal cannot be its own owner", where)
			}
			owners[g.AsOwner] = true
		}
	}
	for id := range owners {
		for i, g := range src.Principals[id].Grants {
			if g.AsOwner != "" {
				return fmt.Errorf("principal %q grants[%d]: %q is an owner and cannot itself use as_owner", id, i, id)
			}
		}
	}
	return nil
}

func sortedPrincipalIDs(src *Source) []string {
	ids := make([]string, 0, len(src.Principals))
	for id := range src.Principals {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func validatePrincipal(id string, p *Principal, seenKeys map[string]string) error {
	where := fmt.Sprintf("principal %q", id)
	if !identifierPattern.MatchString(id) {
		return fmt.Errorf("%s: id is not a valid identifier ([a-z][a-z0-9._-]{0,63})", where)
	}
	if p == nil {
		return fmt.Errorf("%s: is null", where)
	}
	if p.Limits != nil {
		if err := trust.ValidateLimits(&trust.Limits{RatePerSecond: p.Limits.RatePerSecond, Burst: p.Limits.Burst, ChainCalls: p.Limits.ChainCalls, ChainDepth: p.Limits.ChainDepth, Unlimited: p.Limits.Unlimited}); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	}
	if p.Encryption != "" && p.Encryption != EncryptionRequired && p.Encryption != EncryptionNone {
		return fmt.Errorf("%s: encryption is %q or %q", where, EncryptionRequired, EncryptionNone)
	}
	if p.Encryption == EncryptionRequired && p.Kind == "local" {
		return fmt.Errorf("%s: a local principal does not use the network, so it has no encryption", where)
	}
	switch p.Kind {
	case "instance", "service", "user":
		if strings.HasPrefix(id, "local.") {
			return fmt.Errorf("%s: the \"local.\" prefix is reserved for kind \"local\"", where)
		}
	case "local":
		// A restricted internal component, authenticated by its channel (the
		// wrapper secret): named local.<wrapper>, and without any key.
		if !strings.HasPrefix(id, "local.") || len(id) == len("local.") {
			return fmt.Errorf("%s: a local principal is named local.<wrapper>", where)
		}
		if len(p.Keys) != 0 {
			return fmt.Errorf("%s: a local principal has no key; the wrapper secret authenticates it", where)
		}
		if len(p.Actors) != 0 {
			return fmt.Errorf("%s: a local principal has no actors", where)
		}
	default:
		return fmt.Errorf("%s: kind must be instance, service, user or local", where)
	}
	if len(p.Actors) > maxActors {
		return fmt.Errorf("%s: more than %d actors", where, maxActors)
	}
	for actorID, actor := range p.Actors {
		if !identifierPattern.MatchString(actorID) {
			return fmt.Errorf("%s: actor %q is not a valid identifier ([a-z][a-z0-9._-]{0,63})", where, actorID)
		}
		if actor == nil {
			return fmt.Errorf("%s: actor %q is null", where, actorID)
		}
		if len(actor.Grants) > maxGrants {
			return fmt.Errorf("%s: actor %q has more than %d grants", where, actorID, maxGrants)
		}
		for i, g := range actor.Grants {
			if err := validateGrant(fmt.Sprintf("%s actors[%s] grants[%d]", where, actorID, i), g); err != nil {
				return err
			}
		}
	}
	if len(p.Keys) > maxKeysPerPrincipal {
		return fmt.Errorf("%s: more than %d keys", where, maxKeysPerPrincipal)
	}
	if len(p.Keys) == 0 && !p.Disabled && p.Kind != "local" {
		return fmt.Errorf("%s: an enabled principal needs at least one key", where)
	}
	for i, key := range p.Keys {
		raw, err := base64.RawURLEncoding.DecodeString(key.PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return fmt.Errorf("%s: keys[%d].public_key must be a base64url (no padding) Ed25519 public key", where, i)
		}
		switch key.State {
		case "", "active", "retired", "revoked":
		default:
			return fmt.Errorf("%s: keys[%d].state must be active, retired or revoked", where, i)
		}
		if owner, dup := seenKeys[key.PublicKey]; dup {
			return fmt.Errorf("%s: keys[%d] is already used by principal %q", where, i, owner)
		}
		seenKeys[key.PublicKey] = id
	}
	if len(p.Grants) > maxGrants {
		return fmt.Errorf("%s: more than %d grants", where, maxGrants)
	}
	for i, g := range p.Grants {
		if err := validateGrant(fmt.Sprintf("%s grants[%d]", where, i), g); err != nil {
			return err
		}
	}
	return nil
}

func validateGrant(where string, g Grant) error {
	verb, targets, ok := g.verbAndTargets()
	if !ok {
		return fmt.Errorf("%s: exactly one of read, write, create, call, define, control is required", where)
	}
	if g.AsOwner != "" && verb != VerbCall {
		return fmt.Errorf("%s: as_owner applies to call grants only", where)
	}
	if len(targets) == 0 || len(targets) > maxTargetsPerGrant {
		return fmt.Errorf("%s: %s needs between 1 and %d targets", where, verb, maxTargetsPerGrant)
	}
	for _, raw := range targets {
		t, err := ParseTarget(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if err := checkTargetForVerb(where, verb, t); err != nil {
			return err
		}
	}
	if len(g.Except) > maxExcepts {
		return fmt.Errorf("%s: more than %d exceptions", where, maxExcepts)
	}
	for _, raw := range g.Except {
		t, err := ParseTarget(raw)
		if err != nil {
			return fmt.Errorf("%s except: %w", where, err)
		}
		if err := checkTargetForVerb(where+" except", verb, t); err != nil {
			return err
		}
	}
	return nil
}

func checkTargetForVerb(where, verb string, t Target) error {
	if t.Genre != "" && !verbGenres[verb][t.Genre] {
		if verb == VerbControl {
			return fmt.Errorf("%s: control applies to whole contexts, not to an element (target %q)", where, t.String())
		}
		return fmt.Errorf("%s: verb %s does not apply to genre %q (target %q)", where, verb, t.Genre, t.String())
	}
	return nil
}
