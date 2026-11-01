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

package trust

import (
	"crypto/ed25519"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxPrincipals          = 1024
	maxSigningKeysPerUser  = 8
	maxSigningKeysTotal    = 4096
	maxScopes              = 2048
	maxAssignedScopes      = 128
	maxSourcePermissions   = 16384
	maxAtomicPermissions   = 65536
	maxContextSegments     = 32
	maxContextBytes        = 1024
	maxPermissionListItems = 256
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

func validatePolicy(policy *Policy) error {
	if policy.Format != PolicyFormat || !oneOf(policy.FormatVersion, PolicyFormatVersion, PolicyFormatVersionV2, PolicyFormatVersionV3) {
		return fmt.Errorf("unsupported trust policy format %q version %q", policy.Format, policy.FormatVersion)
	}
	if err := validateIdentifier("policy_id", policy.PolicyID); err != nil {
		return err
	}
	if err := validateIdentifier("instance_id", policy.InstanceID); err != nil {
		return err
	}
	if err := validateRevision(policy.Revision); err != nil {
		return err
	}
	if policy.PreviousPolicyHash != "" && !validDigestID(policy.PreviousPolicyHash, "bp1_") {
		return fmt.Errorf("previous_policy_hash is not a canonical policy hash")
	}
	if _, err := parseUTCTime("issued_at", policy.IssuedAt); err != nil {
		return err
	}
	if policy.Principals == nil || len(policy.Principals) > maxPrincipals {
		return fmt.Errorf("principals must be present and contain at most %d entries", maxPrincipals)
	}
	if policy.Scopes == nil || len(policy.Scopes) > maxScopes {
		return fmt.Errorf("scopes must be present and contain at most %d entries", maxScopes)
	}

	scopeIDs := make(map[string]struct{}, len(policy.Scopes))
	permissionIDs := make(map[string]struct{})
	sourcePermissions := 0
	atomicPermissions := 0
	for index := range policy.Scopes {
		scope := &policy.Scopes[index]
		if err := validateIdentifier("scope_id", scope.ScopeID); err != nil {
			return fmt.Errorf("scope[%d]: %w", index, err)
		}
		if _, exists := scopeIDs[scope.ScopeID]; exists {
			return fmt.Errorf("duplicate scope_id %q", scope.ScopeID)
		}
		scopeIDs[scope.ScopeID] = struct{}{}
		sets := []struct {
			label string
			set   *PermissionSet
		}{{"allow", &scope.Allow}, {"deny", &scope.Deny}}
		for _, candidate := range sets {
			source, atomic, err := validatePermissionSet(policy.FormatVersion, scope.ScopeID, candidate.label, candidate.set, permissionIDs)
			if err != nil {
				return err
			}
			sourcePermissions += source
			atomicPermissions += atomic
			if sourcePermissions > maxSourcePermissions || atomicPermissions > maxAtomicPermissions {
				return fmt.Errorf("policy permission expansion exceeds its hard bound")
			}
		}
	}

	principalIDs := make(map[string]struct{}, len(policy.Principals))
	keyIDs := make(map[string]string)
	totalKeys := 0
	for index := range policy.Principals {
		principal := &policy.Principals[index]
		if err := validatePrincipal(policy.FormatVersion, principal, scopeIDs, keyIDs); err != nil {
			return fmt.Errorf("principal[%d]: %w", index, err)
		}
		if _, exists := principalIDs[principal.PrincipalID]; exists {
			return fmt.Errorf("duplicate principal_id %q", principal.PrincipalID)
		}
		principalIDs[principal.PrincipalID] = struct{}{}
		totalKeys += len(principal.SigningKeys)
		if totalKeys > maxSigningKeysTotal {
			return fmt.Errorf("policy has more than %d signing keys", maxSigningKeysTotal)
		}
	}
	return validateOwners(policy)
}

// validateOwners checks every as_owner against the principals: the owner must be
// a local principal of the policy, and a principal that is an owner cannot itself
// hand authority to another owner (no chain of facades).
func validateOwners(policy *Policy) error {
	kinds := make(map[string]string, len(policy.Principals))
	for index := range policy.Principals {
		kinds[policy.Principals[index].PrincipalID] = policy.Principals[index].Kind
	}
	owners := map[string]struct{}{}
	byScope := map[string][]string{} // scope id -> owners its permissions name
	for index := range policy.Scopes {
		scope := &policy.Scopes[index]
		for _, permission := range scope.Allow.Resources {
			if permission.AsOwner == "" {
				continue
			}
			if kinds[permission.AsOwner] != PrincipalKindLocal {
				return fmt.Errorf("permission %q: as_owner %q must be a local principal of the policy", permission.PermissionID, permission.AsOwner)
			}
			owners[permission.AsOwner] = struct{}{}
			byScope[scope.ScopeID] = append(byScope[scope.ScopeID], permission.AsOwner)
		}
	}
	for index := range policy.Principals {
		principal := &policy.Principals[index]
		if _, isOwner := owners[principal.PrincipalID]; !isOwner {
			continue
		}
		assigned := append([]string(nil), principal.ScopeIDs...)
		for _, actor := range principal.Actors {
			assigned = append(assigned, actor.ScopeIDs...)
		}
		for _, scopeID := range assigned {
			if len(byScope[scopeID]) > 0 {
				return fmt.Errorf("principal %q is an owner and cannot itself use as_owner", principal.PrincipalID)
			}
		}
	}
	return nil
}

func validatePrincipal(version string, principal *Principal, scopes map[string]struct{}, keys map[string]string) error {
	if err := validateIdentifier("principal_id", principal.PrincipalID); err != nil {
		return err
	}
	if !oneOf(principal.Kind, "instance", "service", "user", PrincipalKindLocal) {
		return fmt.Errorf("principal %q has invalid kind %q", principal.PrincipalID, principal.Kind)
	}
	isLocalName := strings.HasPrefix(principal.PrincipalID, LocalPrincipalPrefix)
	if (principal.Kind == PrincipalKindLocal) != isLocalName {
		return fmt.Errorf("principal %q: the %q prefix and the %q kind go together", principal.PrincipalID, LocalPrincipalPrefix, PrincipalKindLocal)
	}
	if principal.Kind == PrincipalKindLocal && version == PolicyFormatVersion {
		return fmt.Errorf("principal %q: local principals require format version %s", principal.PrincipalID, PolicyFormatVersionV2)
	}
	if !oneOf(principal.Status, "active", "disabled", "revoked") {
		return fmt.Errorf("principal %q has invalid status %q", principal.PrincipalID, principal.Status)
	}
	if principal.ScopeIDs == nil || len(principal.ScopeIDs) > maxAssignedScopes {
		return fmt.Errorf("principal %q scope_ids must be present and bounded", principal.PrincipalID)
	}
	seenScopes := make(map[string]struct{}, len(principal.ScopeIDs))
	for _, scopeID := range principal.ScopeIDs {
		if _, exists := scopes[scopeID]; !exists {
			return fmt.Errorf("principal %q references unknown scope %q", principal.PrincipalID, scopeID)
		}
		if _, duplicate := seenScopes[scopeID]; duplicate {
			return fmt.Errorf("principal %q repeats scope %q", principal.PrincipalID, scopeID)
		}
		seenScopes[scopeID] = struct{}{}
	}
	if err := validateActors(version, principal, scopes); err != nil {
		return err
	}
	if principal.SigningKeys == nil || len(principal.SigningKeys) > maxSigningKeysPerUser {
		return fmt.Errorf("principal %q signing_keys must be present and bounded", principal.PrincipalID)
	}
	if principal.Kind == PrincipalKindLocal && len(principal.SigningKeys) != 0 {
		// A local principal is authenticated by its channel (the wrapper secret),
		// never by a signature: a key here would let a signed message impersonate it.
		return fmt.Errorf("local principal %q must not have a signing key", principal.PrincipalID)
	}
	if principal.Kind != PrincipalKindLocal && principal.Status == "active" && len(principal.SigningKeys) == 0 {
		return fmt.Errorf("active principal %q has no signing key", principal.PrincipalID)
	}
	for index := range principal.SigningKeys {
		if err := validateSigningKey(principal.PrincipalID, &principal.SigningKeys[index], keys); err != nil {
			return err
		}
	}
	return nil
}

const maxActorsPerPrincipal = 256

func validateActors(version string, principal *Principal, scopes map[string]struct{}) error {
	if len(principal.Actors) == 0 {
		return nil
	}
	if version == PolicyFormatVersion {
		return fmt.Errorf("principal %q: actors require format version %s", principal.PrincipalID, PolicyFormatVersionV2)
	}
	if len(principal.Actors) > maxActorsPerPrincipal {
		return fmt.Errorf("principal %q declares more than %d actors", principal.PrincipalID, maxActorsPerPrincipal)
	}
	seen := make(map[string]struct{}, len(principal.Actors))
	for _, actor := range principal.Actors {
		if err := validateIdentifier("actor_id", actor.ActorID); err != nil {
			return fmt.Errorf("principal %q: %w", principal.PrincipalID, err)
		}
		if _, duplicate := seen[actor.ActorID]; duplicate {
			return fmt.Errorf("principal %q repeats actor %q", principal.PrincipalID, actor.ActorID)
		}
		seen[actor.ActorID] = struct{}{}
		if actor.ScopeIDs == nil || len(actor.ScopeIDs) > maxAssignedScopes {
			return fmt.Errorf("principal %q actor %q scope_ids must be present and bounded", principal.PrincipalID, actor.ActorID)
		}
		repeated := make(map[string]struct{}, len(actor.ScopeIDs))
		for _, scopeID := range actor.ScopeIDs {
			if _, exists := scopes[scopeID]; !exists {
				return fmt.Errorf("principal %q actor %q references unknown scope %q", principal.PrincipalID, actor.ActorID, scopeID)
			}
			if _, duplicate := repeated[scopeID]; duplicate {
				return fmt.Errorf("principal %q actor %q repeats scope %q", principal.PrincipalID, actor.ActorID, scopeID)
			}
			repeated[scopeID] = struct{}{}
		}
	}
	return nil
}

func validateSigningKey(principalID string, key *SigningKey, keys map[string]string) error {
	if key.Algorithm != SigningAlgorithm {
		return fmt.Errorf("principal %q key %q uses unsupported algorithm %q", principalID, key.KeyID, key.Algorithm)
	}
	if !oneOf(key.Status, "active", "disabled", "retired", "revoked") {
		return fmt.Errorf("principal %q key %q has invalid status %q", principalID, key.KeyID, key.Status)
	}
	publicKey, err := rawBase64.DecodeString(key.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("principal %q key %q has invalid public_key", principalID, key.KeyID)
	}
	derived, _ := SigningKeyID(ed25519.PublicKey(publicKey))
	if key.KeyID != derived {
		return fmt.Errorf("principal %q key_id is not derived from public_key", principalID)
	}
	if owner, duplicate := keys[key.KeyID]; duplicate {
		return fmt.Errorf("signing key %q belongs to both %q and %q", key.KeyID, owner, principalID)
	}
	keys[key.KeyID] = principalID

	var notBefore, notAfter time.Time
	if key.NotBefore != "" {
		notBefore, err = parseUTCTime("not_before", key.NotBefore)
		if err != nil {
			return fmt.Errorf("principal %q key %q: %w", principalID, key.KeyID, err)
		}
	}
	if key.NotAfter != "" {
		notAfter, err = parseUTCTime("not_after", key.NotAfter)
		if err != nil {
			return fmt.Errorf("principal %q key %q: %w", principalID, key.KeyID, err)
		}
	}
	if !notBefore.IsZero() && !notAfter.IsZero() && !notBefore.Before(notAfter) {
		return fmt.Errorf("principal %q key %q validity window is empty", principalID, key.KeyID)
	}
	return nil
}

func validatePermissionSet(version, scopeID, label string, set *PermissionSet, permissionIDs map[string]struct{}) (int, int, error) {
	if set.Capabilities == nil || set.Resources == nil {
		return 0, 0, fmt.Errorf("scope %q %s permission lists must be present", scopeID, label)
	}
	if version == PolicyFormatVersion && len(set.Verbs) > 0 {
		return 0, 0, fmt.Errorf("scope %q %s: verbs require format version %s", scopeID, label, PolicyFormatVersionV2)
	}
	source := len(set.Capabilities) + len(set.Resources) + len(set.Verbs)
	atomic := 0
	for index := range set.Verbs {
		permission := &set.Verbs[index]
		if err := registerPermissionID(permission.PermissionID, permissionIDs); err != nil {
			return 0, 0, fmt.Errorf("scope %q %s verb[%d]: %w", scopeID, label, index, err)
		}
		if err := validateContextSelector(permission.Context); err != nil {
			return 0, 0, fmt.Errorf("permission %q: %w", permission.PermissionID, err)
		}
		if permission.Verbs == nil || len(permission.Verbs) == 0 || len(permission.Verbs) > maxPermissionListItems {
			return 0, 0, fmt.Errorf("permission %q: verbs must contain between 1 and %d entries", permission.PermissionID, maxPermissionListItems)
		}
		seen := make(map[string]struct{}, len(permission.Verbs))
		for _, verb := range permission.Verbs {
			if !oneOf(verb, VerbRead, VerbWrite, VerbCreate, VerbDefine, VerbControl) {
				return 0, 0, fmt.Errorf("permission %q: unknown verb %q", permission.PermissionID, verb)
			}
			if _, duplicate := seen[verb]; duplicate {
				return 0, 0, fmt.Errorf("permission %q: duplicate verb %q", permission.PermissionID, verb)
			}
			seen[verb] = struct{}{}
		}
		atomic += len(permission.Verbs)
	}
	for index := range set.Capabilities {
		permission := &set.Capabilities[index]
		if err := registerPermissionID(permission.PermissionID, permissionIDs); err != nil {
			return 0, 0, fmt.Errorf("scope %q %s capability[%d]: %w", scopeID, label, index, err)
		}
		if err := validateContextSelector(permission.Context); err != nil {
			return 0, 0, fmt.Errorf("permission %q: %w", permission.PermissionID, err)
		}
		if err := validateIdentifier("family", permission.Family); err != nil {
			return 0, 0, fmt.Errorf("permission %q: %w", permission.PermissionID, err)
		}
		if version != PolicyFormatVersion {
			return 0, 0, fmt.Errorf("permission %q: capacity names are not allowed in format version %s; grant verbs and capacity/execute resources", permission.PermissionID, PolicyFormatVersionV2)
		}
		if err := validateIdentifierList("names", permission.Names, false); err != nil {
			return 0, 0, fmt.Errorf("permission %q: %w", permission.PermissionID, err)
		}
		atomic += len(permission.Names)
	}
	for index := range set.Resources {
		permission := &set.Resources[index]
		if err := registerPermissionID(permission.PermissionID, permissionIDs); err != nil {
			return 0, 0, fmt.Errorf("scope %q %s resource[%d]: %w", scopeID, label, index, err)
		}
		if !oneOf(permission.Kind, "context", "matter", "schema", "structure", "document", "capacity", "trace") {
			return 0, 0, fmt.Errorf("permission %q has invalid resource kind %q", permission.PermissionID, permission.Kind)
		}
		if err := validateContextSelector(permission.Context); err != nil {
			return 0, 0, fmt.Errorf("permission %q: %w", permission.PermissionID, err)
		}
		if permission.Kind == "context" {
			if permission.Selector != nil {
				return 0, 0, fmt.Errorf("permission %q: context resource must omit selector", permission.PermissionID)
			}
		} else if err := validateResourceSelector(permission.PermissionID, permission.Selector); err != nil {
			return 0, 0, err
		}
		if err := validateIdentifierList("actions", permission.Actions, false); err != nil {
			return 0, 0, fmt.Errorf("permission %q: %w", permission.PermissionID, err)
		}
		resourceCount := 1
		if permission.Selector != nil && permission.Selector.All == nil {
			resourceCount = len(permission.Selector.IDs)
		}
		atomic += resourceCount * len(permission.Actions)
		if permission.AsOwner != "" {
			if version != PolicyFormatVersionV3 {
				return 0, 0, fmt.Errorf("permission %q: as_owner requires format version %s", permission.PermissionID, PolicyFormatVersionV3)
			}
			if label != "allow" || permission.Kind != "capacity" || len(permission.Actions) != 1 || permission.Actions[0] != "execute" {
				return 0, 0, fmt.Errorf("permission %q: as_owner belongs to an allow capacity/execute permission", permission.PermissionID)
			}
			if err := validateIdentifier("as_owner", permission.AsOwner); err != nil {
				return 0, 0, fmt.Errorf("permission %q: %w", permission.PermissionID, err)
			}
		}
	}
	return source, atomic, nil
}

func validateResourceSelector(permissionID string, selector *ResourceSelector) error {
	if selector == nil {
		return fmt.Errorf("permission %q requires a resource selector", permissionID)
	}
	hasIDs := selector.IDs != nil
	hasAll := selector.All != nil
	if hasIDs == hasAll || (hasAll && !*selector.All) {
		return fmt.Errorf("permission %q selector must contain exactly ids or all=true", permissionID)
	}
	if hasIDs {
		if err := validateIdentifierList("ids", selector.IDs, false); err != nil {
			return fmt.Errorf("permission %q: %w", permissionID, err)
		}
	}
	return nil
}

func validateContextSelector(selector ContextSelector) error {
	if selector.IncludeDescendants == nil {
		return fmt.Errorf("context.include_descendants is required")
	}
	if len(selector.Path) == 0 || len(selector.Path) > maxContextBytes || !utf8.ValidString(selector.Path) {
		return fmt.Errorf("context path has invalid size or encoding")
	}
	if selector.Path == "/root" {
		return nil
	}
	if !strings.HasPrefix(selector.Path, "/root/") || strings.HasSuffix(selector.Path, "/") {
		return fmt.Errorf("context path %q is not canonical and absolute", selector.Path)
	}
	segments := strings.Split(strings.TrimPrefix(selector.Path, "/"), "/")
	if len(segments) > maxContextSegments {
		return fmt.Errorf("context path %q has too many segments", selector.Path)
	}
	for _, segment := range segments[1:] {
		if !identifierPattern.MatchString(segment) || segment == "." || segment == ".." {
			return fmt.Errorf("context path %q has invalid segment %q", selector.Path, segment)
		}
	}
	return nil
}

func registerPermissionID(id string, seen map[string]struct{}) error {
	if err := validateIdentifier("permission_id", id); err != nil {
		return err
	}
	if _, duplicate := seen[id]; duplicate {
		return fmt.Errorf("duplicate permission_id %q", id)
	}
	seen[id] = struct{}{}
	return nil
}

func validateIdentifierList(label string, values []string, allowEmpty bool) error {
	if values == nil || (!allowEmpty && len(values) == 0) || len(values) > maxPermissionListItems {
		return fmt.Errorf("%s must contain between 1 and %d entries", label, maxPermissionListItems)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateIdentifier(label, value); err != nil {
			return err
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%s contains duplicate %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateIdentifier(label, value string) error {
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s %q is not a canonical identifier", label, value)
	}
	return nil
}

func validateRevision(value string) error {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return fmt.Errorf("revision %q is not canonical decimal", value)
	}
	revision, err := strconv.ParseUint(value, 10, 64)
	if err != nil || revision == 0 {
		return fmt.Errorf("revision %q is not a positive canonical decimal", value)
	}
	return nil
}

func parseUTCTime(label, value string) (time.Time, error) {
	if value == "" || !strings.HasSuffix(value, "Z") {
		return time.Time{}, fmt.Errorf("%s must be an RFC3339 UTC timestamp", label)
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC {
		return time.Time{}, fmt.Errorf("%s must be an RFC3339 UTC timestamp", label)
	}
	return parsed, nil
}

func validDigestID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	decoded, err := rawBase64.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(decoded) == 32
}

func oneOf(value string, accepted ...string) bool {
	for _, candidate := range accepted {
		if value == candidate {
			return true
		}
	}
	return false
}
