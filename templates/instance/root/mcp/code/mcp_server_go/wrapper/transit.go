/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package wrapper

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const transitInlineLimit = 64 * 1024

// MatterRef is durable semantic state. Unlike a transient @matter carrier it is
// never read or deleted implicitly.
type MatterRef struct {
	Context string
	ID      string
	Mode    string
}

type transitOrigin struct {
	From        Address
	Correlation Correlation
}

// TransitService selects and resolves a parameter carrier from its address.
type TransitService struct {
	runtime      *Runtime
	directory    string
	allowed      string
	maxBytes     int
	ttl          time.Duration
	httpClient   *http.Client
	matterInvoke func(string, string, map[string]any, transitOrigin) (map[string]any, error)
	mu           sync.Mutex
	objects      map[string]any
}

func NewTransitService(runtime *Runtime) (*TransitService, error) {
	maxBytes, err := positiveEnv("BRIQUE_TRANSIT_MAX_BYTES", 64*1024*1024)
	if err != nil {
		return nil, err
	}
	ttlSeconds, err := positiveEnv("BRIQUE_TRANSIT_TTL_SECONDS", 60*60)
	if err != nil {
		return nil, err
	}
	allowed := runtime.ctxDir
	parts := strings.FieldsFunc(strings.Trim(runtime.ctxID, "/"), func(r rune) bool { return r == '/' })
	for range parts[1:] {
		allowed = filepath.Dir(allowed)
	}
	dir := filepath.Join(runtime.ctxDir, "tmp", "wrapper_"+runtime.wrapperName, "transit")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	service := &TransitService{
		runtime: runtime, directory: dir, allowed: filepath.Clean(allowed), maxBytes: maxBytes,
		ttl: time.Duration(ttlSeconds) * time.Second, httpClient: &http.Client{Timeout: 5 * time.Minute},
		objects: make(map[string]any),
	}
	service.sweepFiles()
	return service, nil
}

func (s *TransitService) sameWrapper(target Address) bool {
	if target.Type != TypeUser || strings.HasPrefix(strings.TrimSpace(target.Context), "@ext_") {
		return false
	}
	rel := strings.Trim(target.Context, "/")
	base := strings.Trim(s.runtime.ctxID, "/")
	if strings.HasPrefix(target.Context, "/") {
		if rel == base {
			rel = ""
		} else if strings.HasPrefix(rel, base+"/") {
			rel = strings.TrimPrefix(rel, base+"/")
		} else {
			return false
		}
	}
	_, err := s.runtime.registry.ResolveCapacity(rel, target.Cap)
	return err == nil
}

// Emit returns the transport representation selected from target.
func (s *TransitService) Emit(value any, target Address, origin *IntentionMsg) (any, error) {
	if isTransitRef(value) || (isScalar(value) && !isString(value)) {
		return value, nil
	}
	if text, ok := value.(string); ok && len([]byte(text)) <= transitInlineLimit {
		return value, nil
	}
	if s.sameWrapper(target) {
		id := "obj_" + newID()
		s.mu.Lock()
		s.objects[id] = value
		s.mu.Unlock()
		return map[string]any{"@object": id, "wrapper": s.runtime.wrapperName, "type": ""}, nil
	}
	format := "json"
	var data []byte
	var err error
	if raw, ok := value.([]byte); ok {
		format, data = "bytes", append([]byte(nil), raw...)
	} else if text, ok := value.(string); ok {
		format, data = "text", []byte(text)
	} else {
		data, err = json.Marshal(value)
	}
	if err != nil {
		return nil, fmt.Errorf("encode transit value: %w", err)
	}
	if len(data) <= transitInlineLimit && format == "json" {
		return value, nil
	}
	if strings.HasPrefix(strings.TrimSpace(target.Context), "@ext_") || len(data) > s.maxBytes {
		return s.createMatter(data, format, target, origin)
	}
	return s.writeFile(data, format)
}

func (s *TransitService) writeFile(data []byte, format string) (map[string]any, error) {
	if len(data) > s.maxBytes {
		return nil, fmt.Errorf("transit payload exceeds configured file limit")
	}
	name := "transit_" + newID() + ".bin"
	path := filepath.Join(s.directory, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	return map[string]any{"@file": path, "format": format}, nil
}

// Value resolves one parameter lazily and memoizes it.
func (p *Params) Value(key string) (any, error) {
	p.mu.Lock()
	if value, ok := p.resolved[key]; ok {
		p.mu.Unlock()
		return value, nil
	}
	raw, ok := p.values[key]
	if !ok {
		if matters, yes := p.values["@matter"].(map[string]any); yes {
			if item, found := matters[key].(map[string]any); found {
				value := MatterRef{Context: asString(item["context"]), ID: asString(item["id"]), Mode: asString(item["mode"])}
				p.resolved[key], p.consumed[key] = value, true
				p.mu.Unlock()
				return value, nil
			}
		}
		p.mu.Unlock()
		return nil, fmt.Errorf("missing parameter %q", key)
	}
	p.mu.Unlock()
	value, err := p.runtime.transit.resolve(raw, p.origin)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.resolved[key], p.consumed[key] = value, true
	p.mu.Unlock()
	return value, nil
}

// Emit exposes the same address-driven selection for hosted code that needs to
// prepare a value without issuing the call immediately.
func (p *Params) Emit(value any, target Address) (any, error) {
	return p.runtime.transit.Emit(value, target, p.origin)
}

// Call emits ordinary values for target and routes the intention through the engine.
func (p *Params) Call(target Address, values map[string]any) (*Params, error) {
	emitted := make(map[string]any, len(values))
	posted := make([]any, 0)
	matters := make([]map[string]any, 0)
	for key, value := range values {
		if matter, ok := value.(MatterRef); ok {
			item := map[string]any{"context": matter.Context, "id": matter.ID, "repr": key}
			if matter.Mode != "" {
				item["mode"] = matter.Mode
			}
			matters = append(matters, item)
			continue
		}
		out, err := p.Emit(value, target)
		if err != nil {
			p.runtime.transit.releaseAll(posted, p.origin)
			return nil, err
		}
		emitted[key] = out
		if isTransitRef(out) {
			posted = append(posted, out)
		}
	}
	origin := originFrom(p.runtime, p.origin)
	payload, err := p.runtime.outbound.EmitAndWait(OutboundParams{ToContext: target.Context, ToCap: target.Cap, ToType: target.Type, FromContext: origin.From.Context, FromCap: origin.From.Cap, Params: emitted, Matters: matters, Correlation: origin.Correlation})
	if err != nil {
		return nil, err
	}
	return newParams(payload, p.runtime, p.origin), nil
}

func (p *Params) releaseUnconsumed() error {
	p.mu.Lock()
	pending := make([]any, 0)
	for key, value := range p.values {
		if key != "@matter" && !p.consumed[key] && isTransitRef(value) {
			pending = append(pending, value)
		}
	}
	p.mu.Unlock()
	for _, value := range pending {
		if err := p.runtime.transit.release(value, p.origin, true); err != nil {
			return err
		}
	}
	return nil
}

func (s *TransitService) resolve(raw any, origin *IntentionMsg) (any, error) {
	ref, ok := raw.(map[string]any)
	if !ok {
		return raw, nil
	}
	if id, ok := ref["@object"].(string); ok && exactKeys(ref, "@object", "wrapper", "type") {
		if asString(ref["wrapper"]) != s.runtime.wrapperName {
			return nil, fmt.Errorf("object belongs to another wrapper")
		}
		s.mu.Lock()
		value, found := s.objects[id]
		if found {
			delete(s.objects, id)
		}
		s.mu.Unlock()
		if !found {
			return nil, fmt.Errorf("transit object not found")
		}
		return value, nil
	}
	if path, ok := ref["@file"].(string); ok && exactKeys(ref, "@file", "format") {
		clean := filepath.Clean(path)
		rel, err := filepath.Rel(s.allowed, clean)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("transit file outside context tree")
		}
		info, err := os.Lstat(clean)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("invalid transit file")
		}
		data, err := os.ReadFile(clean)
		if err != nil {
			return nil, err
		}
		value, err := decodeTransit(data, asString(ref["format"]))
		if err != nil {
			return nil, err
		}
		if err := os.Remove(clean); err != nil {
			return nil, err
		}
		return value, nil
	}
	if _, ok := ref["@matter"].(string); ok && isTransientMatterRef(ref) {
		return s.consumeMatter(ref, origin)
	}
	return raw, nil
}

func (s *TransitService) createMatter(data []byte, format string, target Address, intention *IntentionMsg) (map[string]any, error) {
	origin := originFrom(s.runtime, intention)
	expires := time.Now().Add(s.ttl).Unix()
	descriptor := map[string]any{"brique": map[string]any{"kind": "matter", "substance_mode": "brique", "substance_type": "binary", "substance_framing": "bytes", "transient": true, "transient_expires_at": expires}, "objective": map[string]any{}, "functional": map[string]any{}, "subjective": map[string]any{}}
	created, err := s.matterCall(target.Context, "matter.create", map[string]any{"matter": descriptor}, origin)
	if err != nil {
		return nil, err
	}
	id := asString(created["matter_id"])
	revision := asInt(created["revision"])
	if id == "" || revision < 1 {
		return nil, fmt.Errorf("matter.create returned incomplete identity")
	}
	opened, err := s.matterCall(target.Context, "matter.write", map[string]any{"matter_id": id, "expected_revision": revision, "http_data": true}, origin)
	if err != nil {
		s.deleteMatter(target.Context, id, revision, -1, origin)
		return nil, err
	}
	handle, ok := opened["http_data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("matter.write returned no upload lease")
	}
	committed, err := s.httpPut(handle, data)
	if err != nil {
		s.deleteMatter(target.Context, id, revision, -1, origin)
		return nil, err
	}
	revision = asInt(committed["revision"])
	if revision < 1 {
		return nil, fmt.Errorf("matter upload returned no revision")
	}
	ref := map[string]any{"@matter": id, "context": destinationContext(target.Context), "owner": target.Context, "format": format, "consume": true, "revision": revision, "size": len(data)}
	if s.ttl > 0 {
		compact := origin
		committedSize := len(data)
		go func() {
			timer := time.NewTimer(s.ttl)
			defer timer.Stop()
			<-timer.C
			_ = s.deleteMatter(asString(ref["owner"]), id, revision, committedSize, compact)
		}()
	}
	return ref, nil
}

func (s *TransitService) consumeMatter(ref map[string]any, intention *IntentionMsg) (any, error) {
	origin := originFrom(s.runtime, intention)
	context, id := asString(ref["context"]), asString(ref["@matter"])
	revision, size := asInt(ref["revision"]), asInt(ref["size"])
	payload, err := s.matterCall(context, "matter.read", map[string]any{"matter_id": id, "read_mode": "brique|data"}, origin)
	if err != nil {
		return nil, err
	}
	brique, ok := payload["brique"].(map[string]any)
	if !ok || brique["transient"] != true || asInt(brique["revision"]) != revision {
		return nil, fmt.Errorf("Matter is not the expected transient carrier")
	}
	dataInfo, ok := payload["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("matter.read returned no data")
	}
	data, err := s.readMatterData(dataInfo, size)
	if err != nil {
		return nil, err
	}
	if len(data) != size {
		return nil, fmt.Errorf("transient Matter size mismatch")
	}
	value, err := decodeTransit(data, asString(ref["format"]))
	if err != nil {
		return nil, err
	}
	if err := s.deleteMatter(context, id, revision, size, origin); err != nil {
		return nil, err
	}
	return value, nil
}

func (s *TransitService) release(value any, intention *IntentionMsg, received bool) error {
	ref, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if id, ok := ref["@object"].(string); ok {
		s.mu.Lock()
		delete(s.objects, id)
		s.mu.Unlock()
		return nil
	}
	if path, ok := ref["@file"].(string); ok {
		_ = os.Remove(path)
		return nil
	}
	if !isTransientMatterRef(ref) {
		return nil
	}
	context := asString(ref["owner"])
	if received {
		context = asString(ref["context"])
	}
	origin := originFrom(s.runtime, intention)
	if received {
		payload, err := s.matterCall(context, "matter.read", map[string]any{"matter_id": asString(ref["@matter"]), "read_mode": "brique"}, origin)
		if err != nil {
			return nil
		}
		b, ok := payload["brique"].(map[string]any)
		if !ok || b["transient"] != true || asInt(b["revision"]) != asInt(ref["revision"]) {
			return nil
		}
	}
	return s.deleteMatter(context, asString(ref["@matter"]), asInt(ref["revision"]), asInt(ref["size"]), origin)
}

func (s *TransitService) releaseAll(values []any, intention *IntentionMsg) {
	for _, value := range values {
		_ = s.release(value, intention, false)
	}
}

func (s *TransitService) matterCall(context, cap string, params map[string]any, origin transitOrigin) (map[string]any, error) {
	if s.matterInvoke != nil {
		return s.matterInvoke(context, cap, params, origin)
	}
	return s.runtime.outbound.EmitAndWait(OutboundParams{ToContext: context, ToCap: cap, ToType: TypeMatter, FromContext: origin.From.Context, FromCap: origin.From.Cap, Params: params, Correlation: origin.Correlation})
}

func (s *TransitService) deleteMatter(context, id string, revision, size int, origin transitOrigin) error {
	params := map[string]any{"matter_id": id, "expected_revision": revision}
	if size >= 0 {
		params["expected_byte_size"] = size
	}
	_, err := s.matterCall(context, "matter.delete", params, origin)
	return err
}

func originFrom(runtime *Runtime, msg *IntentionMsg) transitOrigin {
	if msg == nil {
		return transitOrigin{From: Address{Context: "", Type: TypeExecution}}
	}
	root := msg.Correlation.RootIntentionID
	if root == "" {
		root = msg.IntentionID
	}
	return transitOrigin{From: Address{Context: runtime.responseContext(msg.To.Context), Cap: msg.To.Cap, Type: TypeExecution}, Correlation: Correlation{RootIntentionID: root, ParentIntentionID: msg.IntentionID}}
}

func (r *Runtime) responseContext(raw string) string {
	prefix := "@wrapper_" + r.wrapperName + ":/"
	if strings.HasPrefix(raw, prefix) {
		return strings.Trim(strings.TrimPrefix(raw, prefix), "/")
	}
	return strings.Trim(raw, "/")
}

func destinationContext(raw string) string {
	if !strings.HasPrefix(strings.TrimSpace(raw), "@ext_") {
		if strings.HasPrefix(raw, "/") {
			return "/" + strings.Trim(raw, "/")
		}
		return strings.Trim(raw, "/")
	}
	_, path, ok := strings.Cut(raw, ":")
	if !ok || strings.TrimSpace(path) == "" {
		return "/root"
	}
	return "/" + strings.Trim(path, "/")
}

func (s *TransitService) httpPut(handle map[string]any, data []byte) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodPut, leaseURL(handle), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Matter upload HTTP %d", resp.StatusCode)
	}
	var out map[string]any
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	return out, err
}
func (s *TransitService) httpGet(handle map[string]any, size int) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, leaseURL(handle), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Matter download HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, int64(size)+1))
}
func leaseURL(handle map[string]any) string {
	base := strings.TrimRight(asString(handle["base_url"]), "/")
	path := asString(handle["path"])
	token := url.QueryEscape(asString(handle["tok"]))
	return base + path + "?tok=" + token
}
func (s *TransitService) readMatterData(data map[string]any, size int) ([]byte, error) {
	switch asString(data["kind"]) {
	case "inline":
		raw := asString(data["bytes"])
		if asString(data["encoding"]) == "base64" {
			return base64.StdEncoding.DecodeString(raw)
		}
		return []byte(raw), nil
	case "http":
		h, ok := data["http"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("missing download lease")
		}
		return s.httpGet(h, size)
	}
	return nil, fmt.Errorf("unsupported Matter data kind")
}

func decodeTransit(data []byte, format string) (any, error) {
	if format == "bytes" {
		return data, nil
	}
	if format == "text" {
		return string(data), nil
	}
	if format == "json" {
		var out any
		err := json.Unmarshal(data, &out)
		return out, err
	}
	return nil, fmt.Errorf("unknown transit format %q", format)
}
func isScalar(value any) bool {
	switch value.(type) {
	case nil, bool, string, float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		return true
	}
	return false
}
func isString(value any) bool   { _, ok := value.(string); return ok }
func asString(value any) string { v, _ := value.(string); return strings.TrimSpace(v) }
func asInt(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
}
func exactKeys(ref map[string]any, keys ...string) bool {
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	for key := range ref {
		if !allowed[key] {
			return false
		}
	}
	return true
}
func isTransientMatterRef(ref map[string]any) bool {
	return asString(ref["@matter"]) != "" && ref["consume"] == true && exactKeys(ref, "@matter", "context", "owner", "format", "consume", "revision", "size")
}
func isTransitRef(value any) bool {
	ref, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if _, ok := ref["@file"].(string); ok && exactKeys(ref, "@file", "format") {
		return true
	}
	if _, ok := ref["@object"].(string); ok && exactKeys(ref, "@object", "wrapper", "type") {
		return true
	}
	return isTransientMatterRef(ref)
}

func (s *TransitService) sweepFiles() {
	entries, _ := os.ReadDir(s.directory)
	cutoff := time.Now().Add(-s.ttl)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "transit_") {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(s.directory, entry.Name()))
		}
	}
}
