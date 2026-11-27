// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

// Package gateway is the outbound MCP gateway of Brique: the only client of the instance toward the
// external MCP servers its registry names. It is substrate code: the capacities tools, call and status
// of /root/mcp/client adapt their parameters to it. Its contracts are those capacities' descriptors.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Origins of a call.
const (
	OriginAgentic = "agentic"
	OriginPlan    = "plan"
)

// Reasons of a refusal or of a non-completed outcome.
const (
	reasonPolicyDenied          = "policy_denied"
	reasonDisclosureDenied      = "disclosure_denied"
	reasonInvalidArguments      = "invalid_arguments"
	reasonProjectionStale       = "projection_stale"
	reasonOverloaded            = "overloaded"
	reasonAuthenticationNeeded  = "authentication_required"
	reasonDeadline              = "deadline"
	reasonTransport             = "transport"
	reasonProtocol              = "protocol"
	reasonMandatoryUnavailable  = "mandatory_unavailable"
	reasonNotRegistered         = "not_registered"
	reasonDisabled              = "disabled"
	reasonCatalogTooLarge       = "catalog_too_large"
	maxArgumentsBytes           = 64 << 10
	defaultGlobalConcurrency    = 64
	minSecretLengthForDetection = 6
)

// testHookTransmitting runs right after transmitting is recorded; nil outside the tests.
var testHookTransmitting func()

var callIDRule = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// Problem is a request the gateway cannot take at all (malformed, or no registry). A decision of the
// gateway about a well-formed call is not a Problem: it is a state of the call.
type Problem struct {
	Code    string
	Reason  string
	Message string
}

func (p *Problem) Error() string { return p.Message }

func invalid(message string) *Problem {
	return &Problem{Code: "invalid", Reason: "invalid_request", Message: message}
}

// Config of a gateway.
type Config struct {
	StoreDir          string               // the wrapper's private store (BRIQUE_WRAPPER_STORE)
	Secrets           map[string]string    // machine secrets granted to the wrapper, by name
	GlobalConcurrency int                  // calls in flight at once, all servers together
	JournalRetention  time.Duration        // how long a terminal attempt is kept
	Now               func() time.Time     // clock, for tests
	Log               func(string, ...any) // bounded, secret-free diagnostic lines
}

// Gateway is safe for concurrent use.
type Gateway struct {
	cfg         Config
	registry    *registryStore
	journal     *journal
	pool        *sessionPool
	catalogs    *catalogCache
	projections *projectionStore
	global      chan struct{}

	mu        sync.Mutex
	perServer map[string]chan struct{}

	work       context.Context
	cancelWork context.CancelFunc
}

// SecretsFromEnvironment collects the secrets delivered as MCP_SECRET_<NAME>.
func SecretsFromEnvironment() map[string]string {
	out := map[string]string{}
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if rest, ok := strings.CutPrefix(name, "MCP_SECRET_"); ok && rest != "" && value != "" {
			out[strings.ToLower(rest)] = value
		}
	}
	return out
}

// New opens the gateway: the journal is reconciled before any call is accepted.
func New(cfg Config) (*Gateway, error) {
	if cfg.StoreDir == "" {
		return nil, errors.New("gateway: no private store (the wrapper must declare run.store)")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = func(string, ...any) {}
	}
	if cfg.GlobalConcurrency <= 0 {
		cfg.GlobalConcurrency = defaultGlobalConcurrency
	}
	if cfg.JournalRetention == 0 {
		cfg.JournalRetention = defaultJournalRetention
	}
	if cfg.Secrets == nil {
		cfg.Secrets = map[string]string{}
	}
	j, err := openJournal(cfg.StoreDir, cfg.JournalRetention, cfg.Now())
	if err != nil {
		return nil, err
	}
	projections, err := newProjectionStore(cfg.StoreDir)
	if err != nil {
		j.close()
		return nil, err
	}
	projections.prune(cfg.Now())
	work, cancel := context.WithCancel(context.Background())
	return &Gateway{
		cfg: cfg, registry: newRegistryStore(cfg.StoreDir), journal: j, pool: newSessionPool(cfg.Secrets, cfg.StoreDir, cfg.Now),
		catalogs: newCatalogCache(), projections: projections, global: make(chan struct{}, cfg.GlobalConcurrency),
		perServer: map[string]chan struct{}{}, work: work, cancelWork: cancel,
	}, nil
}

// Cancel stops the work in flight (first phase of a stop): calls end and commit their state.
func (g *Gateway) Cancel() { g.cancelWork() }

// Close releases the sessions and the journal (second phase, after the handlers returned).
func (g *Gateway) Close() {
	g.cancelWork()
	g.pool.closeAll()
	_ = g.journal.close()
}

// bound returns a context that ends with the caller's and with the gateway's work.
func (g *Gateway) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	merged, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(g.work, cancel)
	return merged, func() { stop(); cancel() }
}

func (g *Gateway) serverSlots(server *Server) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	slots, ok := g.perServer[server.key]
	if !ok {
		slots = make(chan struct{}, server.concurrency())
		g.perServer[server.key] = slots
	}
	return slots
}

// admit takes a global and a server slot at once, or refuses: no call waits for a slot.
func (g *Gateway) admit(server *Server) (func(), bool) {
	select {
	case g.global <- struct{}{}:
	default:
		return nil, false
	}
	slots := g.serverSlots(server)
	select {
	case slots <- struct{}{}:
	default:
		<-g.global
		return nil, false
	}
	return func() { <-slots; <-g.global }, true
}

func (g *Gateway) listFunc(server *Server) func(context.Context) (*catalog, error) {
	return func(ctx context.Context) (*catalog, error) {
		session, err := g.pool.session(ctx, server)
		if err != nil {
			return nil, err
		}
		cat, err := listCatalog(ctx, session, server, g.cfg.Now())
		if err != nil && !errors.Is(err, errCatalogTooLarge) && !isRemoteAnswer(err) {
			g.pool.drop(server, session)
		}
		return cat, err
	}
}

// ---- tools --------------------------------------------------------------------------------------

// ToolsRequest is the input of the capacity tools.
type ToolsRequest struct {
	Servers   []string
	Mandatory []string
}

// Tools prepares and persists a projection.
func (g *Gateway) Tools(ctx context.Context, req ToolsRequest) (map[string]any, error) {
	reg, err := g.registry.get()
	if err != nil {
		g.cfg.Log("event=tools_refused reason=registry error=%q", err.Error())
		return nil, &Problem{Code: "refused", Reason: reasonPolicyDenied, Message: "no usable MCP registry is installed"}
	}
	requested := req.Servers
	if len(requested) == 0 {
		requested = reg.sortedAliases()
	}
	seen := map[string]bool{}
	for _, alias := range requested {
		if !aliasPattern.MatchString(alias) || seen[alias] {
			return nil, invalid("servers must be distinct server aliases")
		}
		seen[alias] = true
	}
	for _, alias := range req.Mandatory {
		if !seen[alias] {
			return nil, invalid("mandatory lists only servers that are requested")
		}
	}
	sort.Strings(requested)
	ctx, cancel := g.bound(ctx)
	defer cancel()

	type prepared struct {
		cat    *catalog
		reason string
	}
	results := make([]prepared, len(requested))
	var wg sync.WaitGroup
	for i, alias := range requested {
		server := reg.Servers[alias]
		switch {
		case server == nil:
			results[i].reason = reasonNotRegistered
			continue
		case !server.enabled():
			results[i].reason = reasonDisabled
			continue
		}
		wg.Add(1)
		go func(i int, server *Server) {
			defer wg.Done()
			cat, err := g.catalogs.get(ctx, 0, g.cfg.Now(), g.listFunc(server), server)
			if err != nil {
				results[i].reason = failureReason(err, nil)
				g.cfg.Log("event=catalog_unavailable server=%s reason=%s detail=%q", server.alias, results[i].reason, describeTransportError(err))
				return
			}
			results[i].cat = cat
		}(i, server)
	}
	wg.Wait()

	unavailable := []map[string]any{}
	var pairs [][2]string
	var projected []*catalogTool
	full := false
	for i, alias := range requested {
		if results[i].cat == nil {
			for _, m := range req.Mandatory {
				if m == alias {
					return nil, &Problem{Code: "refused", Reason: reasonMandatoryUnavailable, Message: "mandatory server " + alias + " is unavailable: " + results[i].reason}
				}
			}
			unavailable = append(unavailable, map[string]any{"server": alias, "reason": results[i].reason})
			continue
		}
		for _, name := range results[i].cat.sortedToolNames() {
			if len(pairs) == maxProjectionTools {
				full = true
				break
			}
			pairs = append(pairs, [2]string{alias, name})
			projected = append(projected, results[i].cat.tools[name])
		}
		if full {
			unavailable = append(unavailable, map[string]any{"server": alias, "reason": reasonCatalogTooLarge})
		}
	}
	now := g.cfg.Now()
	projection := &Projection{ID: newProjectionID(), RegistryVersion: reg.Version, CreatedAt: now, ExpiresAt: now.Add(projectionLife)}
	tools := make([]map[string]any, len(pairs))
	for i, name := range modelNames(pairs) {
		tool := projected[i]
		projection.Tools = append(projection.Tools, ProjectedTool{ModelName: name, Server: pairs[i][0], Tool: pairs[i][1],
			Fingerprint: tool.Fingerprint, Description: tool.Description, InputSchema: tool.InputSchema})
		tools[i] = map[string]any{"model_name": name, "description": tool.Description, "input_schema": tool.InputSchema}
	}
	if err := g.projections.save(projection); err != nil {
		return nil, fmt.Errorf("projection cannot be saved: %w", err)
	}
	go g.projections.prune(now)
	return map[string]any{
		"projection_id": projection.ID, "registry_version": reg.Version, "tools": tools,
		"unavailable": unavailable, "expires_at": projection.ExpiresAt.UTC().Format(time.RFC3339),
	}, nil
}

// ---- call ---------------------------------------------------------------------------------------

// CallRequest is the input of the capacity call. CallerContext and CallerCapacity are the origin the
// engine set on the intention, never a parameter.
type CallRequest struct {
	CallID         string
	Origin         string
	Envelope       string
	ProjectionID   string
	Tool           string
	ArgumentsJSON  string
	Server         string
	Arguments      map[string]any
	CallerContext  string
	CallerCapacity string
}

// resolved is a call once its caller is admitted and its target known from trusted state.
type resolved struct {
	server      *Server
	tool        *catalogTool
	arguments   map[string]any
	requestHash string
	pilot       string
	continuity  string
}

func refusal(callID, reason string, registryVersion int64) map[string]any {
	return map[string]any{"call_id": callID, "attempt_id": "", "state": stateRefused, "reason": reason,
		"transmission": transmissionNone, "result": nil, "registry_version": registryVersion, "catalog_fingerprint": ""}
}

func attemptPayload(a Attempt) map[string]any {
	var result any
	if len(a.Result) > 0 {
		result = a.Result
	}
	return map[string]any{"call_id": a.CallID, "attempt_id": a.AttemptID, "state": a.State, "reason": a.Reason,
		"transmission": a.Transmission, "result": result, "registry_version": a.RegistryVersion,
		"catalog_fingerprint": a.CatalogFingerprint}
}

// Call makes one call, at most once per call_id.
func (g *Gateway) Call(ctx context.Context, req CallRequest) (map[string]any, error) {
	if !callIDRule.MatchString(req.CallID) {
		return nil, invalid("call_id must match [A-Za-z0-9_.-]{1,128}")
	}
	if req.Origin != OriginAgentic && req.Origin != OriginPlan {
		return nil, invalid(`origin must be "agentic" or "plan"`)
	}
	reg, err := g.registry.get()
	if err != nil {
		g.cfg.Log("event=call_refused call_id=%s reason=registry error=%q", req.CallID, err.Error())
		return refusal(req.CallID, reasonPolicyDenied, 0), nil
	}
	ctx, cancel := g.bound(ctx)
	defer cancel()

	var r *resolved
	var reason string
	if req.Origin == OriginAgentic {
		r, reason, err = g.resolveAgentic(ctx, reg, req)
	} else {
		r, reason, err = g.resolvePlan(ctx, reg, req)
	}
	if err != nil {
		return nil, err
	}
	if reason != "" {
		return refusal(req.CallID, reason, reg.Version), nil
	}
	if reason := g.checkArguments(r); reason != "" {
		return refusal(req.CallID, reason, reg.Version), nil
	}

	// A call_id already reserved: the same request gets the same attempt, never a second transmission.
	if existing, ok := g.journal.get(req.CallID); ok {
		return g.existing(ctx, existing, r.requestHash, reg.Version)
	}
	release, ok := g.admit(r.server)
	if !ok {
		return refusal(req.CallID, reasonOverloaded, reg.Version), nil
	}
	defer release()
	now := g.cfg.Now()
	attempt, created, err := g.journal.reserve(Attempt{
		CallID: req.CallID, AttemptID: newAttemptID(), Origin: req.Origin, Server: r.server.alias, Tool: r.tool.Name,
		RequestHash: r.requestHash, RegistryVersion: reg.Version, CatalogFingerprint: r.tool.Fingerprint,
		Pilot: r.pilot, Continuity: r.continuity, ReservedAt: now, At: now,
	})
	if err != nil {
		g.cfg.Log("event=journal_failed call_id=%s", req.CallID)
		return nil, &Problem{Code: "internal", Reason: "journal_unavailable", Message: "the gateway journal cannot be written; nothing was sent"}
	}
	if !created {
		return g.existing(ctx, attempt, r.requestHash, reg.Version)
	}
	return g.transmit(ctx, attempt, r)
}

func (g *Gateway) existing(ctx context.Context, a Attempt, requestHash string, registryVersion int64) (map[string]any, error) {
	if a.RequestHash != requestHash {
		return refusal(a.CallID, reasonPolicyDenied, registryVersion), nil // the call_id names another request
	}
	done, err := g.journal.waitTerminal(ctx, a.CallID)
	if err != nil {
		return nil, &Problem{Code: "refused", Reason: "in_progress", Message: "the call is still in progress; ask status later"}
	}
	return attemptPayload(done), nil
}

func (g *Gateway) resolveAgentic(ctx context.Context, reg *Registry, req CallRequest) (*resolved, string, error) {
	if req.Envelope == "" || req.ProjectionID == "" || req.Tool == "" || req.ArgumentsJSON == "" {
		return nil, "", invalid("origin agentic needs envelope, projection_id, tool and arguments_json")
	}
	claims, err := verifyEnvelope(reg.publicKey, req.Envelope, g.cfg.Now())
	if err != nil {
		g.cfg.Log("event=call_refused call_id=%s reason=envelope detail=%q", req.CallID, err.Error())
		return nil, reasonPolicyDenied, nil
	}
	if claims.CallID != req.CallID || claims.ProjectionID != req.ProjectionID || claims.Tool != req.Tool ||
		claims.ArgsSHA256 != sha256Hex([]byte(req.ArgumentsJSON)) {
		g.cfg.Log("event=call_refused call_id=%s reason=claims_mismatch", req.CallID)
		return nil, reasonPolicyDenied, nil
	}
	if len(req.ArgumentsJSON) > maxArgumentsBytes {
		return nil, reasonInvalidArguments, nil
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(req.ArgumentsJSON), &arguments); err != nil || arguments == nil {
		return nil, reasonInvalidArguments, nil
	}
	projection, err := g.projections.load(req.ProjectionID, g.cfg.Now())
	if err != nil {
		g.cfg.Log("event=call_refused call_id=%s reason=projection detail=%q", req.CallID, err.Error())
		return nil, reasonProjectionStale, nil
	}
	projected, ok := projection.lookup(req.Tool)
	if !ok {
		return nil, reasonPolicyDenied, nil
	}
	server := reg.Servers[projected.Server]
	if server == nil || !server.enabled() || !server.offers(projected.Tool) {
		return nil, reasonPolicyDenied, nil
	}
	tool, reason := g.currentTool(ctx, server, projected.Tool)
	if reason != "" {
		return nil, reason, nil
	}
	if tool.Fingerprint != projected.Fingerprint {
		return nil, reasonProjectionStale, nil
	}
	return &resolved{server: server, tool: tool, arguments: arguments, requestHash: sha256Hex([]byte(req.Envelope)),
		pilot: claims.Pilot, continuity: claims.Continuity}, "", nil
}

func (g *Gateway) resolvePlan(ctx context.Context, reg *Registry, req CallRequest) (*resolved, string, error) {
	if req.Server == "" || req.Tool == "" {
		return nil, "", invalid("origin plan needs server, tool and arguments")
	}
	if !reg.planAllowed(req.CallerContext, req.CallerCapacity) {
		g.cfg.Log("event=call_refused call_id=%s reason=plan_not_registered", req.CallID)
		return nil, reasonPolicyDenied, nil
	}
	server := reg.Servers[req.Server]
	if server == nil || !server.enabled() || !server.offers(req.Tool) {
		return nil, reasonPolicyDenied, nil
	}
	arguments := req.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	request, err := json.Marshal([]any{planReference(req.CallerContext, req.CallerCapacity), req.Server, req.Tool, arguments})
	if err != nil || len(request) > maxArgumentsBytes {
		return nil, reasonInvalidArguments, nil
	}
	tool, reason := g.currentTool(ctx, server, req.Tool)
	if reason != "" {
		return nil, reason, nil
	}
	return &resolved{server: server, tool: tool, arguments: arguments, requestHash: sha256Hex(request)}, "", nil
}

// currentTool is the tool as listed recently; a tool the server no longer offers is refused.
func (g *Gateway) currentTool(ctx context.Context, server *Server, name string) (*catalogTool, string) {
	cat, err := g.catalogs.get(ctx, catalogTTL, g.cfg.Now(), g.listFunc(server), server)
	if err != nil {
		return nil, failureReason(err, nil)
	}
	tool, ok := cat.tools[name]
	if !ok {
		return nil, reasonProjectionStale
	}
	return tool, ""
}

// checkArguments applies the registry's denied fields, the secret check and the schema.
func (g *Gateway) checkArguments(r *resolved) string {
	if _, denied := r.server.deniedArgument(r.tool.Name, r.arguments); denied {
		return reasonDisclosureDenied
	}
	if g.containsSecret(r.arguments) {
		return reasonDisclosureDenied
	}
	validator, err := r.tool.validator()
	if err != nil {
		return reasonInvalidArguments
	}
	if err := validator.Validate(r.arguments); err != nil {
		return reasonInvalidArguments
	}
	return ""
}

func (g *Gateway) containsSecret(value any) bool {
	switch v := value.(type) {
	case string:
		for _, secret := range g.cfg.Secrets {
			if len(secret) >= minSecretLengthForDetection && strings.Contains(v, secret) {
				return true
			}
		}
	case map[string]any:
		for key, item := range v {
			if g.containsSecret(key) || g.containsSecret(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if g.containsSecret(item) {
				return true
			}
		}
	}
	return false
}

// transmit sends the reserved call once and commits its terminal state.
func (g *Gateway) transmit(ctx context.Context, attempt Attempt, r *resolved) (payload map[string]any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			g.cfg.Log("event=call_panic call_id=%s type=%T", attempt.CallID, recovered)
			current, _ := g.journal.get(attempt.CallID)
			if !terminal(current.State) {
				if current.State == stateTransmitting {
					current.State, current.Transmission = stateOutcomeUnknown, transmissionPossible
				} else {
					current.State, current.Transmission = stateFailedBeforeTransmit, transmissionNone
				}
				current.Reason, current.At = reasonInternal, g.cfg.Now()
				_ = g.journal.record(current)
			}
			payload, err = nil, &Problem{Code: "internal", Reason: "gateway_panic", Message: "the gateway failed; ask status for the state of the call"}
		}
	}()
	finish := func(state, reason, transmission string, result json.RawMessage) (map[string]any, error) {
		attempt.State, attempt.Reason, attempt.Transmission, attempt.Result, attempt.At = state, reason, transmission, result, g.cfg.Now()
		if err := g.journal.record(attempt); err != nil {
			g.cfg.Log("event=journal_failed call_id=%s state=%s", attempt.CallID, state)
			return nil, &Problem{Code: "internal", Reason: "journal_unavailable", Message: "the outcome could not be recorded; ask status"}
		}
		return attemptPayload(attempt), nil
	}

	session, err := g.pool.session(ctx, r.server)
	if err != nil {
		g.cfg.Log("event=connect_failed call_id=%s server=%s detail=%q", attempt.CallID, r.server.alias, describeTransportError(err))
		return finish(stateFailedBeforeTransmit, failureReason(err, nil), transmissionNone, nil)
	}
	attempt.State, attempt.Transmission, attempt.At = stateTransmitting, transmissionPossible, g.cfg.Now()
	if err := g.journal.record(attempt); err != nil {
		attempt.State, attempt.Transmission = stateReserved, transmissionNone
		return finish(stateFailedBeforeTransmit, reasonInternal, transmissionNone, nil)
	}

	if testHookTransmitting != nil {
		testHookTransmitting()
	}
	callCtx, cancel := context.WithTimeout(ctx, r.server.callDeadline())
	defer cancel()
	callCtx, recorder := withTransmission(callCtx)
	result, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: r.tool.Name, Arguments: r.arguments})
	if err == nil {
		state := stateCompleted
		reason := ""
		if result.IsError {
			state, reason = stateRemoteError, "tool_error"
		}
		return finish(state, reason, transmissionComplete, boundResult(result, r.server.resultInlineBytes()))
	}
	if isRemoteAnswer(err) {
		return finish(stateRemoteError, reasonProtocol, transmissionComplete, nil)
	}
	g.pool.drop(r.server, session)
	switch recorder.status() {
	case 401, 403:
		return finish(stateRemoteError, reasonAuthenticationNeeded, transmissionComplete, nil)
	case 404:
		return finish(stateRemoteError, reasonProtocol, transmissionComplete, nil) // the session was gone: the server refused the request
	}
	reason := failureReason(err, callCtx)
	g.cfg.Log("event=call_failed call_id=%s server=%s detail=%q", attempt.CallID, r.server.alias, describeTransportError(err))
	if transmission := recorder.state(); transmission != transmissionNone {
		return finish(stateOutcomeUnknown, reason, transmission, nil)
	}
	return finish(stateFailedBeforeTransmit, reason, transmissionNone, nil)
}

// isRemoteAnswer reports whether the server answered with a JSON-RPC error: a definite answer. The SDK
// also wraps its own transport and closing failures in JSON-RPC errors, with the codes below; those are
// not answers of the server, and are classified by what was transmitted.
func isRemoteAnswer(err error) bool {
	var wire *jsonrpc.Error
	if !errors.As(err, &wire) {
		return false
	}
	switch wire.Code {
	case sdkUnknownError, sdkClientClosing, sdkServerClosing, sdkRejectedByTransport:
		return false
	}
	return true
}

// Codes of the JSON-RPC errors the SDK makes itself (internal/jsonrpc2/wire.go, v1.8.0).
const (
	sdkUnknownError        = -32001
	sdkClientClosing       = -32003
	sdkServerClosing       = -32004
	sdkRejectedByTransport = -32005
)

func failureReason(err error, ctx context.Context) string {
	switch {
	case errors.Is(err, errCatalogTooLarge):
		return reasonCatalogTooLarge
	case errors.Is(err, errAuthenticationRequired):
		return reasonAuthenticationNeeded
	case errors.Is(err, context.DeadlineExceeded) || (ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)):
		return reasonDeadline
	case isRemoteAnswer(err):
		return reasonProtocol
	}
	return reasonTransport
}

func newAttemptID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "a_" + hex.EncodeToString(b[:])
}

// boundResult turns a tool result into what Brique returns: text blocks, other blocks described and
// never inlined, everything within the server's inline limit, and truncation always said.
func boundResult(result *mcp.CallToolResult, inline int) json.RawMessage {
	budget := inline - 1024 // room for the envelope of the result
	content := []map[string]any{}
	truncated := false
	add := func(text string) {
		if truncated {
			return
		}
		if len(text) > budget {
			text, truncated = truncateUTF8(text, max(budget, 0)), true
		}
		budget -= len(text) + 32
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	for _, block := range result.Content {
		switch b := block.(type) {
		case *mcp.TextContent:
			add(b.Text)
		case *mcp.ImageContent:
			add(fmt.Sprintf("[image content not included: %s, %d bytes]", b.MIMEType, len(b.Data)))
		case *mcp.AudioContent:
			add(fmt.Sprintf("[audio content not included: %s, %d bytes]", b.MIMEType, len(b.Data)))
		default:
			add("[non-text content not included]")
		}
	}
	out := map[string]any{"content": content, "is_error": result.IsError, "truncated": truncated, "externalized": nil}
	if result.StructuredContent != nil && !truncated {
		if structured, err := json.Marshal(result.StructuredContent); err == nil && len(structured) <= budget {
			out["structured"] = json.RawMessage(structured)
		} else {
			out["truncated"] = true
		}
	}
	if out["truncated"] == true {
		content = append(content, map[string]any{"type": "text", "text": "[result truncated by the Brique gateway]"})
		out["content"] = content
	}
	raw, _ := json.Marshal(out)
	return raw
}

// ---- status -------------------------------------------------------------------------------------

// Status tells what the journal holds for a call_id.
func (g *Gateway) Status(callID string) (map[string]any, error) {
	if !callIDRule.MatchString(callID) {
		return nil, invalid("call_id must match [A-Za-z0-9_.-]{1,128}")
	}
	a, ok := g.journal.get(callID)
	if !ok {
		return map[string]any{"call_id": callID, "known": false}, nil
	}
	payload := map[string]any{"call_id": callID, "known": true, "attempt_id": a.AttemptID, "state": a.State,
		"reason": a.Reason, "transmission": a.Transmission, "result": nil}
	if terminal(a.State) && len(a.Result) > 0 {
		payload["result"] = a.Result
	}
	return payload, nil
}
