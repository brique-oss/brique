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
	"strconv"

	"brique_engine/security/policysrc"
)

// PrincipalAdd adds an enabled principal with one signing key.
func (d *Dir) PrincipalAdd(id, kind, publicKey string) error {
	src, err := d.LoadSource()
	if err != nil {
		return err
	}
	if _, exists := src.Principals[id]; exists {
		return fmt.Errorf("principal %q already exists", id)
	}
	if kind == "local" {
		// A restricted local component has no key: the engine admits it from its
		// wrapper channel. Validation checks the "local." prefix on save.
		if publicKey != "" {
			return fmt.Errorf("a local principal has no key")
		}
		src.Principals[id] = &policysrc.Principal{Kind: kind}
		return d.SaveSource(src)
	}
	if _, err := decodeKey(publicKey); err != nil {
		return err
	}
	src.Principals[id] = &policysrc.Principal{Kind: kind, Keys: []policysrc.Key{{PublicKey: publicKey}}}
	return d.SaveSource(src)
}

// PrincipalSetEncryption chooses how messages to and from a principal travel: "required"
// seals them, "none" sends them in the clear, "default" returns to the rule of its kind (an
// instance is sealed).
func (d *Dir) PrincipalSetEncryption(id, mode string) error {
	src, err := d.LoadSource()
	if err != nil {
		return err
	}
	principal, exists := src.Principals[id]
	if !exists || principal == nil {
		return fmt.Errorf("principal %q does not exist", id)
	}
	switch mode {
	case policysrc.EncryptionRequired, policysrc.EncryptionNone:
		principal.Encryption = mode
	case "default":
		principal.Encryption = ""
	default:
		return fmt.Errorf("encryption is %q, %q or \"default\"", policysrc.EncryptionRequired, policysrc.EncryptionNone)
	}
	return d.SaveSource(src)
}

// PrincipalSetLimits sets how fast a principal may make this instance work: "default" returns
// to the default of its kind, "unlimited" lifts the limit, and a rate with a burst (the burst
// defaults to twice the rate) sets it.
func (d *Dir) PrincipalSetLimits(id, rate, burst string) error {
	src, err := d.LoadSource()
	if err != nil {
		return err
	}
	principal, exists := src.Principals[id]
	if !exists || principal == nil {
		return fmt.Errorf("principal %q does not exist", id)
	}
	switch rate {
	case "default":
		principal.Limits = nil
	case "unlimited":
		principal.Limits = &policysrc.Limits{Unlimited: true}
	default:
		perSecond, err := strconv.Atoi(rate)
		if err != nil || perSecond < 1 {
			return fmt.Errorf("the rate is requests per second, a positive number, \"unlimited\" or \"default\"")
		}
		allowed := perSecond * 2
		if burst != "" {
			if allowed, err = strconv.Atoi(burst); err != nil {
				return fmt.Errorf("the burst is a number of requests")
			}
		}
		kept := policysrc.Limits{}
		if principal.Limits != nil && !principal.Limits.Unlimited {
			kept = *principal.Limits
		}
		kept.RatePerSecond, kept.Burst = perSecond, allowed
		principal.Limits = &kept
	}
	return d.SaveSource(src)
}

// PrincipalSetChainLimits sets how much one admitted request of the principal may set off
// inside this instance: the calls derived from it in all, and how deep they nest. A depth left
// empty keeps what the principal has. The rate of the principal is left as it is.
func (d *Dir) PrincipalSetChainLimits(id, calls, depth string) error {
	src, err := d.LoadSource()
	if err != nil {
		return err
	}
	principal, exists := src.Principals[id]
	if !exists || principal == nil {
		return fmt.Errorf("principal %q does not exist", id)
	}
	kept := policysrc.Limits{}
	if principal.Limits != nil {
		if principal.Limits.Unlimited {
			return fmt.Errorf("principal %q is unlimited: set a rate first (\"limits %s default\" lifts the exemption)", id, id)
		}
		kept = *principal.Limits
	}
	var err2 error
	if kept.ChainCalls, err2 = strconv.Atoi(calls); err2 != nil || kept.ChainCalls < 1 {
		return fmt.Errorf("the calls are a positive number")
	}
	if depth != "" {
		if kept.ChainDepth, err2 = strconv.Atoi(depth); err2 != nil || kept.ChainDepth < 1 {
			return fmt.Errorf("the depth is a positive number")
		}
	}
	principal.Limits = &kept
	return d.SaveSource(src)
}

// PrincipalDisable disables a principal without forgetting it, or enables it
// again with enable=true.
func (d *Dir) PrincipalSetDisabled(id string, disabled bool) error {
	src, err := d.LoadSource()
	if err != nil {
		return err
	}
	principal, exists := src.Principals[id]
	if !exists {
		return fmt.Errorf("unknown principal %q", id)
	}
	principal.Disabled = disabled
	return d.SaveSource(src)
}

// PrincipalRemove deletes a principal from the source. The engine refuses a
// policy that drops a revoked principal's tombstone; apply reports it.
func (d *Dir) PrincipalRemove(id string) error {
	src, err := d.LoadSource()
	if err != nil {
		return err
	}
	if _, exists := src.Principals[id]; !exists {
		return fmt.Errorf("unknown principal %q", id)
	}
	delete(src.Principals, id)
	return d.SaveSource(src)
}

// GrantAdd appends one grant. The source is validated before it is written, so a
// grammar or verb error leaves the file untouched.
func (d *Dir) GrantAdd(principal, verb string, targets, except []string) (int, error) {
	return d.GrantAddFor(principal, "", verb, targets, except)
}

// GrantAddFor adds a grant to the principal, or to one of its actors when actor is
// not empty (the actor is created if needed). An actor receives the intersection
// of its grants and the principal's (model D14).
func (d *Dir) GrantAddFor(principal, actor, verb string, targets, except []string) (int, error) {
	return d.GrantAddAsOwner(principal, actor, verb, targets, except, "")
}

// GrantAddAsOwner is GrantAddFor for a call grant whose capacity runs as the owner
// (a local principal): the caller needs no direct access to what it uses. Written
// on the principal's own grants only: an actor cannot lend another identity.
func (d *Dir) GrantAddAsOwner(principal, actor, verb string, targets, except []string, owner string) (int, error) {
	if owner != "" && (verb != policysrc.VerbCall || actor != "") {
		return 0, fmt.Errorf("as_owner applies to a call grant of the principal itself")
	}
	src, err := d.LoadSource()
	if err != nil {
		return 0, err
	}
	p, exists := src.Principals[principal]
	if !exists {
		return 0, fmt.Errorf("unknown principal %q", principal)
	}
	grant := policysrc.Grant{Except: except, AsOwner: owner}
	list := policysrc.TargetList(targets)
	switch verb {
	case policysrc.VerbRead:
		grant.Read = list
	case policysrc.VerbWrite:
		grant.Write = list
	case policysrc.VerbCreate:
		grant.Create = list
	case policysrc.VerbCall:
		grant.Call = list
	case policysrc.VerbDefine:
		grant.Define = list
	case policysrc.VerbControl:
		grant.Control = list
	default:
		return 0, fmt.Errorf("unknown verb %q (read, write, create, call, define, control)", verb)
	}
	list2 := &p.Grants
	if actor != "" {
		if p.Actors == nil {
			p.Actors = map[string]*policysrc.Actor{}
		}
		if p.Actors[actor] == nil {
			p.Actors[actor] = &policysrc.Actor{Grants: []policysrc.Grant{}}
		}
		list2 = &p.Actors[actor].Grants
	}
	*list2 = append(*list2, grant)
	if err := d.SaveSource(src); err != nil {
		return 0, err
	}
	return len(*list2), nil
}

// GrantRemove deletes the grant at the 1-based rank shown by GrantList.
func (d *Dir) GrantRemove(principal string, rank int) error {
	return d.GrantRemoveFor(principal, "", rank)
}

// GrantRemoveFor removes a grant of the principal or of one of its actors.
func (d *Dir) GrantRemoveFor(principal, actor string, rank int) error {
	src, err := d.LoadSource()
	if err != nil {
		return err
	}
	p, exists := src.Principals[principal]
	if !exists {
		return fmt.Errorf("unknown principal %q", principal)
	}
	list := &p.Grants
	if actor != "" {
		if p.Actors[actor] == nil {
			return fmt.Errorf("principal %q has no actor %q", principal, actor)
		}
		list = &p.Actors[actor].Grants
	}
	if rank < 1 || rank > len(*list) {
		return fmt.Errorf("%d grants here; rank %s is out of range", len(*list), strconv.Itoa(rank))
	}
	*list = append((*list)[:rank-1], (*list)[rank:]...)
	return d.SaveSource(src)
}

// GrantLine is one grant in source order.
type GrantLine struct {
	Rank    int
	Verb    string
	Targets []string
	Except  []string
	AsOwner string
}

// GrantList returns a principal's grants in source order.
func (d *Dir) GrantList(principal string) ([]GrantLine, error) {
	return d.GrantListFor(principal, "")
}

// GrantListFor lists the grants of the principal or of one of its actors.
func (d *Dir) GrantListFor(principal, actor string) ([]GrantLine, error) {
	src, err := d.LoadSource()
	if err != nil {
		return nil, err
	}
	p, exists := src.Principals[principal]
	if !exists {
		return nil, fmt.Errorf("unknown principal %q", principal)
	}
	grants := p.Grants
	if actor != "" {
		declared := p.Actors[actor]
		if declared == nil {
			return nil, fmt.Errorf("principal %q has no actor %q", principal, actor)
		}
		grants = declared.Grants
	}
	var out []GrantLine
	for index, g := range grants {
		verb, targets, _ := g.Verb()
		out = append(out, GrantLine{Rank: index + 1, Verb: verb, Targets: targets, Except: g.Except, AsOwner: g.AsOwner})
	}
	return out, nil
}
