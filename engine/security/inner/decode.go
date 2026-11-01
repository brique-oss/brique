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

// Package inner strictly decodes authenticated external payloads into Brique
// messages. It is deliberately transport-agnostic and does not dispatch.
package inner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"brique_engine/circulation"
	"brique_engine/security/trust"
	"brique_engine/security/wire"
	"brique_engine/shared"
)

var (
	ErrInvalidMessage = errors.New("invalid authenticated inner message")
	segmentPattern    = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	versionPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

const (
	maxContextBytes  = 1024
	maxContextDepth  = 32
	maxReferences    = 1024
	maxIdentifierLen = 64
)

type rawMessage struct {
	Kind      string          `json:"kind"`
	TS        string          `json:"ts,omitempty"`
	Intention json.RawMessage `json:"intention,omitempty"`
	Response  json.RawMessage `json:"response,omitempty"`
}

type rawIntention struct {
	IntentionID   string          `json:"intention_id"`
	AwaitResponse *bool           `json:"await_response"`
	To            json.RawMessage `json:"to"`
	From          json.RawMessage `json:"from"`
	Identity      json.RawMessage `json:"identity,omitempty"`
	Matters       json.RawMessage `json:"matters,omitempty"`
	ElementRefs   json.RawMessage `json:"element_refs,omitempty"`
	Params        json.RawMessage `json:"params,omitempty"`
	Correlation   json.RawMessage `json:"correlation,omitempty"`
}

type rawResponse struct {
	IntentionID string          `json:"intention_id"`
	To          json.RawMessage `json:"to"`
	From        json.RawMessage `json:"from"`
	Identity    json.RawMessage `json:"identity,omitempty"`
	Status      string          `json:"status"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Error       json.RawMessage `json:"error,omitempty"`
}

type rawAddress struct {
	Context string `json:"context"`
	Version string `json:"version,omitempty"`
	Cap     string `json:"cap"`
	Type    string `json:"type"`
}

type rawIdentity struct {
	ID        string `json:"id,omitempty"`
	Kind      string `json:"kind,omitempty"`
	PubKey    string `json:"pubkey,omitempty"`
	Signature string `json:"signature,omitempty"`
}

type rawMatterRef struct {
	Context string `json:"context"`
	ID      string `json:"id"`
	Repr    string `json:"repr,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

type rawElementRef struct {
	Context string `json:"context"`
	ID      string `json:"id"`
	Repr    string `json:"repr,omitempty"`
	Kind    string `json:"kind"`
}

type rawCorrelation struct {
	RootIntentionID   string `json:"root_intention_id,omitempty"`
	ParentIntentionID string `json:"parent_intention_id,omitempty"`
}

type rawProblem struct {
	Origin  string          `json:"origin,omitempty"`
	Code    string          `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
	Details json.RawMessage `json:"details,omitempty"`
}

// DecodedResponse intentionally does not expose the untrusted inner target.
// BindTarget must overwrite it with the address authenticated by the pending
// response registry before the message can be dispatched.
type DecodedResponse struct {
	message circulation.Message
}

func (decoded DecodedResponse) IntentionID() string { return decoded.message.Response.IntentionID }
func (decoded DecodedResponse) Status() string      { return decoded.message.Response.Status }

func (decoded DecodedResponse) BindTarget(target circulation.Address) circulation.Message {
	message := decoded.message
	message.Response.To = target
	return message
}

func DecodeIntention(verified *wire.Verified) (circulation.Message, error) {
	message, err := decodeEnvelope(verified, wire.PurposeIntention)
	if err != nil {
		return circulation.Message{}, invalid(err)
	}
	var raw rawIntention
	if err := decodeObject(message.Intention, &raw, "intention"); err != nil {
		return circulation.Message{}, invalid(err)
	}
	if len(message.Response) != 0 {
		return circulation.Message{}, invalidf("response payload is forbidden for intention")
	}
	protected := verified.Protected()
	if raw.IntentionID != protected.IntentionID || raw.AwaitResponse == nil {
		return circulation.Message{}, invalidf("intention metadata does not match protected header")
	}
	to, err := decodeAddress(raw.To, true, true)
	if err != nil {
		return circulation.Message{}, invalid(fmt.Errorf("to: %w", err))
	}
	from, err := decodeAddress(raw.From, false, false)
	if err != nil {
		return circulation.Message{}, invalid(fmt.Errorf("from: %w", err))
	}
	if err := validateEmptyIdentity(raw.Identity); err != nil {
		return circulation.Message{}, invalid(err)
	}
	matters, err := decodeMatters(raw.Matters)
	if err != nil {
		return circulation.Message{}, invalid(err)
	}
	elements, err := decodeElements(raw.ElementRefs)
	if err != nil {
		return circulation.Message{}, invalid(err)
	}
	params, err := decodeMap(raw.Params, "params", false)
	if err != nil {
		return circulation.Message{}, invalid(err)
	}
	correlation, err := decodeCorrelation(raw.Correlation)
	if err != nil {
		return circulation.Message{}, invalid(err)
	}
	from.Context = circulation.ContextID(projectExternal(verified.Admission().PrincipalID(), string(from.Context)))
	return circulation.Message{
		Kind: circulation.ValueKindIntention,
		TS:   message.TS,
		Intention: circulation.Intention{
			IntentionID: raw.IntentionID, AwaitResponse: *raw.AwaitResponse,
			To: to, From: from, Matters: matters, ElementRefs: elements,
			Params: params, Correlation: correlation,
		},
	}, nil
}

func DecodeResponse(verified *wire.Verified) (DecodedResponse, error) {
	message, err := decodeEnvelope(verified, wire.PurposeResponse)
	if err != nil {
		return DecodedResponse{}, invalid(err)
	}
	var raw rawResponse
	if err := decodeObject(message.Response, &raw, "response"); err != nil {
		return DecodedResponse{}, invalid(err)
	}
	if len(message.Intention) != 0 {
		return DecodedResponse{}, invalidf("intention payload is forbidden for response")
	}
	protected := verified.Protected()
	if raw.IntentionID != protected.IntentionID || !validResponseStatus(raw.Status, protected.ResponsePhase) {
		return DecodedResponse{}, invalidf("response metadata does not match protected header")
	}
	// The inner target is syntactically checked but never trusted for routing.
	to, err := decodeAddress(raw.To, false, false)
	if err != nil {
		return DecodedResponse{}, invalid(fmt.Errorf("to: %w", err))
	}
	from, err := decodeAddress(raw.From, false, false)
	if err != nil {
		return DecodedResponse{}, invalid(fmt.Errorf("from: %w", err))
	}
	if err := validateEmptyIdentity(raw.Identity); err != nil {
		return DecodedResponse{}, invalid(err)
	}
	payload, err := decodeMap(raw.Payload, "payload", true)
	if err != nil {
		return DecodedResponse{}, invalid(err)
	}
	problem, err := decodeProblem(raw.Error)
	if err != nil {
		return DecodedResponse{}, invalid(err)
	}
	if raw.Status == circulation.ValueStatusError && problem == nil {
		return DecodedResponse{}, invalidf("error response requires error object")
	}
	if raw.Status == circulation.ValueStatusError && presentNonNull(raw.Payload) {
		return DecodedResponse{}, invalidf("error response cannot contain payload")
	}
	if raw.Status != circulation.ValueStatusError && problem != nil {
		return DecodedResponse{}, invalidf("non-error response cannot contain error object")
	}
	from.Context = circulation.ContextID(projectExternal(verified.Admission().PrincipalID(), string(from.Context)))
	return DecodedResponse{message: circulation.Message{
		Kind: circulation.ValueKindResponse,
		TS:   message.TS,
		Response: circulation.Response{
			IntentionID: raw.IntentionID, To: to, From: from, Status: raw.Status,
			Payload: payload, Error: problem,
		},
	}}, nil
}

func decodeEnvelope(verified *wire.Verified, purpose string) (rawMessage, error) {
	if verified == nil || verified.Protected().Purpose != purpose {
		return rawMessage{}, fmt.Errorf("verified envelope purpose mismatch")
	}
	var message rawMessage
	if err := trust.StrictDecodeApplicationJSON(verified.Payload(), &message); err != nil {
		return rawMessage{}, err
	}
	if message.Kind != purpose {
		return rawMessage{}, fmt.Errorf("inner kind does not match envelope purpose")
	}
	if message.TS != "" {
		if len(message.TS) > 64 {
			return rawMessage{}, fmt.Errorf("inner timestamp is too long")
		}
		if _, err := time.Parse(time.RFC3339Nano, message.TS); err != nil {
			return rawMessage{}, fmt.Errorf("invalid inner timestamp")
		}
	}
	return message, nil
}

func decodeAddress(data json.RawMessage, local, destination bool) (circulation.Address, error) {
	var raw rawAddress
	if err := decodeObject(data, &raw, "address"); err != nil {
		return circulation.Address{}, err
	}
	if local {
		if err := validateLocalPath(raw.Context); err != nil {
			return circulation.Address{}, err
		}
	} else if err := validateAbsolutePath(raw.Context); err != nil {
		return circulation.Address{}, err
	}
	if destination && (!validIdentifier(raw.Cap, false) || !validIdentifier(raw.Type, false)) {
		return circulation.Address{}, fmt.Errorf("destination family and capacity are required")
	}
	if !destination && (!validIdentifier(raw.Cap, true) || !validIdentifier(raw.Type, false)) {
		return circulation.Address{}, fmt.Errorf("invalid source address")
	}
	if raw.Version != "" && !versionPattern.MatchString(raw.Version) {
		return circulation.Address{}, fmt.Errorf("invalid address version")
	}
	return circulation.Address{Context: circulation.ContextID(raw.Context), Version: raw.Version, Cap: raw.Cap, Type: raw.Type}, nil
}

func validateEmptyIdentity(data json.RawMessage) error {
	if len(data) == 0 {
		return nil
	}
	var identity rawIdentity
	if err := decodeObject(data, &identity, "identity"); err != nil {
		return err
	}
	if identity.ID != "" || identity.Kind != "" {
		return fmt.Errorf("inner identity must be absent or empty")
	}
	return nil
}

func decodeMatters(data json.RawMessage) ([]circulation.MatterRef, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var raw []rawMatterRef
	if err := decodeArray(data, &raw, "matters"); err != nil {
		return nil, err
	}
	if len(raw) > maxReferences {
		return nil, fmt.Errorf("too many matter references")
	}
	result := make([]circulation.MatterRef, len(raw))
	for index, ref := range raw {
		if err := validateLocalPath(ref.Context); err != nil || !validIdentifier(ref.ID, false) || !validIdentifier(ref.Repr, true) || !oneOfOrEmpty(ref.Mode, circulation.ValueModeRead, circulation.ValueModeWrite) {
			return nil, fmt.Errorf("invalid matters[%d]", index)
		}
		result[index] = circulation.MatterRef{Context: circulation.ContextID(ref.Context), ID: ref.ID, Repr: ref.Repr, Mode: ref.Mode}
	}
	return result, nil
}

func decodeElements(data json.RawMessage) ([]circulation.ElementRef, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var raw []rawElementRef
	if err := decodeArray(data, &raw, "element_refs"); err != nil {
		return nil, err
	}
	if len(raw) > maxReferences {
		return nil, fmt.Errorf("too many element references")
	}
	result := make([]circulation.ElementRef, len(raw))
	for index, ref := range raw {
		if err := validateLocalPath(ref.Context); err != nil || !validIdentifier(ref.ID, false) || !validIdentifier(ref.Repr, true) || !oneOf(ref.Kind, "schema", "structure", "document", "capacity") {
			return nil, fmt.Errorf("invalid element_refs[%d]", index)
		}
		result[index] = circulation.ElementRef{Context: circulation.ContextID(ref.Context), ID: ref.ID, Repr: ref.Repr, Kind: ref.Kind}
	}
	return result, nil
}

func decodeCorrelation(data json.RawMessage) (*circulation.Correlation, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var raw rawCorrelation
	if err := decodeObject(data, &raw, "correlation"); err != nil {
		return nil, err
	}
	if raw.RootIntentionID == "" && raw.ParentIntentionID == "" {
		return nil, fmt.Errorf("empty correlation object")
	}
	if len(raw.RootIntentionID) > maxIdentifierLen || len(raw.ParentIntentionID) > maxIdentifierLen {
		return nil, fmt.Errorf("correlation identifier is too long")
	}
	return &circulation.Correlation{RootIntentionID: raw.RootIntentionID, ParentIntentionID: raw.ParentIntentionID}, nil
}

func decodeProblem(data json.RawMessage) (*circulation.ResponseProblem, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var raw rawProblem
	if err := decodeObject(data, &raw, "error"); err != nil {
		return nil, err
	}
	details, err := decodeMap(raw.Details, "error.details", false)
	if err != nil {
		return nil, err
	}
	return &circulation.ResponseProblem{Origin: raw.Origin, Code: raw.Code, Message: raw.Message, Details: details}, nil
}

func decodeMap(data json.RawMessage, name string, allowNull bool) (map[string]any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if allowNull && !presentNonNull(data) {
		return nil, nil
	}
	if !startsWith(data, '{') {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil || result == nil {
		return nil, fmt.Errorf("invalid %s", name)
	}
	return result, nil
}

func decodeObject(data json.RawMessage, dst any, name string) error {
	if len(data) == 0 || !startsWith(data, '{') {
		return fmt.Errorf("%s must be an object", name)
	}
	if err := trust.StrictDecodeApplicationJSON(data, dst); err != nil {
		return fmt.Errorf("invalid %s: %w", name, err)
	}
	return nil
}

func decodeArray(data json.RawMessage, dst any, name string) error {
	if !startsWith(data, '[') {
		return fmt.Errorf("%s must be an array", name)
	}
	if err := trust.StrictDecodeApplicationJSON(data, dst); err != nil {
		return fmt.Errorf("invalid %s: %w", name, err)
	}
	return nil
}

func startsWith(data []byte, want byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && trimmed[0] == want
}

func presentNonNull(data json.RawMessage) bool {
	return len(data) != 0 && !bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}

func validateLocalPath(path string) error {
	if err := validateAbsolutePath(path); err != nil {
		return err
	}
	if path != shared.RootContextID && !strings.HasPrefix(path, shared.RootContextID+"/") {
		return fmt.Errorf("context is outside the local root")
	}
	return nil
}

// ValidateLocalContextPath exposes the protocol's canonical direct-context
// rule to the outbound signed membrane so it cannot emit bytes that another
// Brique decoder must reject.
func ValidateLocalContextPath(path string) error { return validateLocalPath(path) }

func validateAbsolutePath(path string) error {
	if path == "" || len(path) > maxContextBytes || path[0] != '/' || path == "/" || strings.HasSuffix(path, "/") || strings.Contains(path, "//") || strings.Contains(path, "@") {
		return fmt.Errorf("context path is not canonical")
	}
	segments := strings.Split(path[1:], "/")
	if len(segments) == 0 || len(segments) > maxContextDepth {
		return fmt.Errorf("context path depth is invalid")
	}
	for _, segment := range segments {
		if !segmentPattern.MatchString(segment) {
			return fmt.Errorf("context path segment is invalid")
		}
	}
	return nil
}

func projectExternal(principalID, path string) string {
	return "@ext_" + principalID + ":" + path
}

func validIdentifier(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	return len(value) <= maxIdentifierLen && identifierPattern.MatchString(value)
}

func validResponseStatus(status, phase string) bool {
	return phase == "running" && status == circulation.ValueStatusRunning ||
		phase == "final" && oneOf(status, circulation.ValueStatusOK, circulation.ValueStatusError)
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func oneOfOrEmpty(value string, allowed ...string) bool {
	return value == "" || oneOf(value, allowed...)
}

func invalid(err error) error {
	return fmt.Errorf("%w: %v", ErrInvalidMessage, err)
}

func invalidf(format string, args ...any) error {
	return invalid(fmt.Errorf(format, args...))
}
