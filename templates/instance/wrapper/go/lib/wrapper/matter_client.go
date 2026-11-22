// Copyright 2026 Nicolas Cassan
// Licensed under the Apache License, Version 2.0.

package wrapper

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The semantic Matter client of a wrapper: read, write, append and guarded update of the Matters of a context.
//
// A capacity that keeps its state in Matters does the same few things: read a Matter whose substance is served inline or through a
// one-shot HTTP lease, write it guarded by expected_revision, append lines to a journal guarded by expected_line_count, and replay a
// read-compute-write that lost a race. This does them once. The rules are shared by the three runtimes and fixed by
// vectors/matter_client.json.

// InlineAppendMax is the biggest block that matter.append takes inline (the engine takes 256 KiB); a bigger one goes through its HTTP lease.
const InlineAppendMax = 240 * 1024

// MatterView is the parsed substance of a Matter (a JSON object) with its descriptor and revision.
type MatterView struct {
	ID         string
	Content    map[string]any
	Descriptor map[string]any
	Brique     map[string]any
	Revision   int64
}

// LinesView is a range of lines of a line-framed (JSON Lines) Matter, each line parsed, with the committed dimensions.
type LinesView struct {
	Lines      []map[string]any
	TotalLines int
	Revision   int64
	Offset     int
}

type substance struct {
	kind string // "inline" or "lease"
	text string
	url  string
}

func consistency(message, cause string, extra map[string]any) *CallError {
	details := map[string]any{"reason": "consistency_failure", "cause": cause}
	for key, value := range extra {
		details[key] = value
	}
	return &CallError{Code: "conflict", Message: message, Details: details}
}

func revisionOf(brique any) int64 {
	record, _ := brique.(map[string]any)
	switch v := record["revision"].(type) {
	case json.Number:
		n, _ := v.Int64()
		return n
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}

// matterLeaseURL is the URL of a lease: the engine nests it under data.http (its descriptor documents the fields flat): both are accepted.
func matterLeaseURL(handle map[string]any) (string, *CallError) {
	if nested, ok := handle["http"].(map[string]any); ok {
		handle = nested
	}
	base, _ := handle["base_url"].(string)
	path, _ := handle["path"].(string)
	token, _ := handle["tok"].(string)
	if base == "" || path == "" || token == "" {
		return "", consistency("the Matter lease is malformed", "malformed_lease", nil)
	}
	return strings.TrimRight(base, "/") + path + "?" + url.Values{"tok": {token}}.Encode(), nil
}

func classifySubstance(data any) (substance, *CallError) {
	record, ok := data.(map[string]any)
	if !ok {
		return substance{}, consistency("the Matter has no readable substance", "no_substance", nil)
	}
	if kind, _ := record["kind"].(string); kind == "http" {
		leaseAt, err := matterLeaseURL(record)
		if err != nil {
			return substance{}, err
		}
		return substance{kind: "lease", url: leaseAt}, nil
	}
	text, ok := record["bytes"].(string)
	if !ok {
		return substance{}, consistency("the Matter substance is not text", "not_text", nil)
	}
	return substance{kind: "inline", text: text}, nil
}

func parseLines(text any) ([]map[string]any, *CallError) {
	if text == nil {
		return []map[string]any{}, nil
	}
	raw, ok := text.(string)
	if !ok {
		return nil, consistency("the journal substance is not text", "not_text", nil)
	}
	out := []map[string]any{}
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var value any
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, consistency("a journal line is not valid JSON", "invalid_line", nil)
		}
		record, ok := normalizeNumbers(value).(map[string]any)
		if !ok {
			return nil, consistency("a journal line is not a JSON object", "invalid_line", nil)
		}
		out = append(out, record)
	}
	return out, nil
}

// dumpJSON is compact (or indented) JSON without escaping of <, > and & (an object keeps its keys sorted).
func dumpJSON(value any, indent bool) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if indent {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buffer.String(), "\n"), nil
}

// dumpLines is the block appended to a journal: one compact JSON document per line, each ending with LF.
func dumpLines(lines []map[string]any) (string, error) {
	var out strings.Builder
	for _, line := range lines {
		text, err := dumpJSON(line, false)
		if err != nil {
			return "", err
		}
		out.WriteString(text)
		out.WriteByte('\n')
	}
	return out.String(), nil
}

// guardedWritePause is the pause after a lost race, attempt number attempt (0 for the first): random up to a bound that doubles up to a cap.
func guardedWritePause(attempt int, random float64, baseMs, capMs int) time.Duration {
	bound := math.Min(float64(capMs), float64(baseMs)*math.Pow(2, float64(min(attempt, 30))))
	return time.Duration(random * bound * float64(time.Millisecond))
}

// MatterClient reads and writes the Matters of one context, from inside a running capacity.
type MatterClient struct {
	call    func(cap string, params map[string]any, context string) (map[string]any, error)
	context string
	http    *http.Client
}

// Store returns the client of the Matters of context ("" for the context of the capacity being served). The calls it makes are
// children of the intention these parameters belong to.
func (p *Params) Store(context string) *MatterClient {
	origin := originFrom(p.runtime, p.origin, p.delegation)
	if context == "" {
		context = origin.From.Context
	}
	return newMatterClient(func(cap string, params map[string]any, destination string) (map[string]any, error) {
		return p.runtime.transit.matterCall(destination, cap, params, origin)
	}, context)
}

func newMatterClient(call func(cap string, params map[string]any, context string) (map[string]any, error), context string) *MatterClient {
	timeout := time.Duration(settingInt("BRIQUE_LEASE_TIMEOUT_MS")) * time.Millisecond
	return &MatterClient{call: call, context: context, http: &http.Client{Timeout: timeout}}
}

func (c *MatterClient) where(context []string) string {
	if len(context) > 0 && context[0] != "" {
		return context[0]
	}
	return c.context
}

// text returns the text of a substance; stale is true when its lease went stale (the caller reads the Matter again).
func (c *MatterClient) text(data any) (text string, stale bool, err error) {
	found, failure := classifySubstance(data)
	if failure != nil {
		return "", false, failure
	}
	if found.kind == "inline" {
		return found.text, false, nil
	}
	response, err := c.http.Get(found.url)
	if err != nil {
		return "", false, consistency(fmt.Sprintf("the substance lease could not be fetched: %v", err), "lease_unreachable", nil)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		return "", true, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", false, consistency(fmt.Sprintf("the substance lease was refused (HTTP %d)", response.StatusCode), "lease_refused", map[string]any{"status": response.StatusCode})
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", false, consistency(fmt.Sprintf("the substance lease could not be read: %v", err), "lease_unreachable", nil)
	}
	return string(body), false, nil
}

func leaseRetries() int { return settingInt("BRIQUE_LEASE_RETRIES") }

func parseContent(matterID, text string) (map[string]any, error) {
	if strings.TrimSpace(text) == "" {
		return nil, consistency("the Matter substance is empty", "empty", map[string]any{"matter_id": matterID})
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, consistency("the Matter substance is not valid JSON", "invalid_json", map[string]any{"matter_id": matterID})
	}
	content, ok := normalizeNumbers(value).(map[string]any)
	if !ok {
		return nil, consistency("the Matter substance is not a JSON object", "not_object", map[string]any{"matter_id": matterID})
	}
	return content, nil
}

func (c *MatterClient) view(matterID string, payload map[string]any) (*MatterView, error) {
	text, stale, err := c.text(payload["data"])
	if err != nil || stale {
		return nil, err
	}
	content, err := parseContent(matterID, text)
	if err != nil {
		return nil, err
	}
	brique, _ := payload["brique"].(map[string]any)
	descriptor, _ := payload["functional"].(map[string]any)
	return &MatterView{ID: matterID, Content: content, Descriptor: descriptor, Brique: brique, Revision: revisionOf(brique)}, nil
}

// Read returns the parsed substance (a JSON object) with its revision: inline up to 256 KiB, through a one-shot HTTP lease beyond.
func (c *MatterClient) Read(matterID string, context ...string) (*MatterView, error) {
	for i := 0; i < leaseRetries(); i++ {
		payload, err := c.call("matter.read", map[string]any{"matter_id": matterID, "read_mode": "functional|data|brique"}, c.where(context))
		if err != nil {
			return nil, err
		}
		view, err := c.view(matterID, payload)
		if err != nil || view != nil {
			return view, err
		}
	}
	return nil, consistency("the substance kept changing while it was being fetched", "unstable", map[string]any{"matter_id": matterID})
}

// ReadText returns the substance exactly as the text it is, nothing parsed.
func (c *MatterClient) ReadText(matterID string, context ...string) (string, error) {
	for i := 0; i < leaseRetries(); i++ {
		payload, err := c.call("matter.read", map[string]any{"matter_id": matterID, "read_mode": "data"}, c.where(context))
		if err != nil {
			return "", err
		}
		text, stale, err := c.text(payload["data"])
		if err != nil {
			return "", err
		}
		if !stale {
			return text, nil
		}
	}
	return "", consistency("the substance kept changing while it was being fetched", "unstable", map[string]any{"matter_id": matterID})
}

// ReadMany reads several Matters in ONE engine request, in the order given. A missing Matter is an IsNotFound error, like Read.
func (c *MatterClient) ReadMany(matterIDs []string, context ...string) ([]*MatterView, error) {
	ids := make([]any, len(matterIDs))
	for i, id := range matterIDs {
		ids[i] = id
	}
	payload, err := c.call("matter.read_batch", map[string]any{"matter_ids": ids, "read_mode": "functional|data|brique"}, c.where(context))
	if err != nil {
		return nil, err
	}
	items, _ := payload["result"].([]any)
	if len(items) != len(matterIDs) {
		return nil, consistency("the batch read answered another number of Matters than asked", "batch_size", nil)
	}
	views := make([]*MatterView, 0, len(items))
	for i, raw := range items {
		item, _ := raw.(map[string]any)
		if ok, _ := item["ok"].(bool); !ok {
			failure := callErrorFrom(item["error"])
			return nil, failure
		}
		view, err := c.view(matterIDs[i], item)
		if err != nil {
			return nil, err
		}
		if view == nil { // a stale lease: read that Matter again
			if view, err = c.Read(matterIDs[i], c.where(context)); err != nil {
				return nil, err
			}
		}
		views = append(views, view)
	}
	return views, nil
}

// Exists says whether the Matter exists.
func (c *MatterClient) Exists(matterID string, context ...string) (bool, error) {
	payload, err := c.call("matter.exists", map[string]any{"matter_id": matterID}, c.where(context))
	if err != nil {
		return false, err
	}
	found, _ := payload["exist"].(bool)
	return found, nil
}

// Objective returns the objective part of the meaning of a Matter (its name, its type), without reading its substance.
func (c *MatterClient) Objective(matterID string, context ...string) (map[string]any, error) {
	payload, err := c.call("matter.read", map[string]any{"matter_id": matterID, "read_mode": "meaning"}, c.where(context))
	if err != nil {
		return nil, err
	}
	meaning, _ := payload["meaning"].(map[string]any)
	objective, _ := meaning["objective"].(map[string]any)
	if objective == nil {
		objective = map[string]any{}
	}
	return objective, nil
}

// Create makes a Matter. With a nil content it is created with no substance (a journal: line-framed, empty).
func (c *MatterClient) Create(matterID string, descriptor, content map[string]any) (int64, error) {
	params := map[string]any{"matter_id": matterID, "matter": descriptor}
	if content != nil {
		text, err := dumpJSON(content, true)
		if err != nil {
			return 0, err
		}
		params["payload"] = text
	}
	payload, err := c.call("matter.create", params, c.context)
	if err != nil {
		return 0, err
	}
	return revisionOf(map[string]any{"revision": payload["revision"]}), nil
}

// writeParams are the parameters of matter.write. A revision is a nanosecond timestamp (above 2^53): its decimal string is exact whatever the
// JSON number handling of the caller. A nil expectedRevision writes without a guard.
func writeParams(matterID, data string, expectedRevision *int64) map[string]any {
	params := map[string]any{"matter_id": matterID, "data": data}
	if expectedRevision != nil {
		params["expected_revision"] = strconv.FormatInt(*expectedRevision, 10)
	}
	return params
}

// Write makes one atomic write of the whole content, guarded by expectedRevision (IsRevisionMismatch when the Matter moved on); nil for no guard.
func (c *MatterClient) Write(matterID string, content map[string]any, expectedRevision *int64) (int64, error) {
	data, err := dumpJSON(content, true)
	if err != nil {
		return 0, err
	}
	payload, err := c.call("matter.write", writeParams(matterID, data, expectedRevision), c.context)
	if err != nil {
		return 0, err
	}
	return revisionOf(map[string]any{"revision": payload["revision"]}), nil
}

// Delete removes the Matter.
func (c *MatterClient) Delete(matterID string) error {
	_, err := c.call("matter.delete", map[string]any{"matter_id": matterID}, c.context)
	return err
}

// Stat returns the revision and the total of lines of a line-framed Matter, without reading its substance.
func (c *MatterClient) Stat(matterID string) (revision int64, totalLines int, err error) {
	payload, err := c.call("matter.stat", map[string]any{"matter_id": matterID}, c.context)
	if err != nil {
		return 0, 0, err
	}
	return revisionOf(map[string]any{"revision": payload["revision"]}), asInt(payload["total_lines"]), nil
}

// ReadLines returns a range of lines (offset is 0-based): inline up to 256 KiB, through a one-shot HTTP lease beyond.
func (c *MatterClient) ReadLines(matterID string, offset, length int) (*LinesView, error) {
	params := map[string]any{"matter_id": matterID, "read_mode": "data|brique", "data_range": map[string]any{"unit": "lines", "offset": offset, "length": length}}
	for i := 0; i < leaseRetries(); i++ {
		payload, err := c.call("matter.read", params, c.context)
		if err != nil {
			return nil, err
		}
		text, stale, err := c.text(payload["data"])
		if err != nil {
			return nil, err
		}
		if stale {
			continue
		}
		lines, failure := parseLines(text)
		if failure != nil {
			return nil, failure
		}
		brique, _ := payload["brique"].(map[string]any)
		data, _ := payload["data"].(map[string]any)
		total, present := data["total_lines"]
		if !present {
			total = brique["substance_line_count"]
		}
		return &LinesView{Lines: lines, TotalLines: asInt(total), Revision: revisionOf(brique), Offset: offset}, nil
	}
	return nil, consistency("the substance kept changing while it was being fetched", "unstable", map[string]any{"matter_id": matterID})
}

// AppendLines appends a block of lines in one atomic commit, guarded by expectedLineCount, and returns the new total of lines
// (IsLineCountMismatch when the journal moved on). A block above InlineAppendMax goes through the engine's HTTP lease.
func (c *MatterClient) AppendLines(matterID string, lines []map[string]any, expectedLineCount int) (int, error) {
	text, err := dumpLines(lines)
	if err != nil {
		return 0, err
	}
	params := map[string]any{"matter_id": matterID, "expected_line_count": expectedLineCount}
	if len(text) <= InlineAppendMax {
		params["data"] = text
		payload, err := c.call("matter.append", params, c.context)
		if err != nil {
			return 0, err
		}
		if total, present := payload["total_lines"]; present {
			return asInt(total), nil
		}
		return expectedLineCount + len(lines), nil
	}
	params["http_data"] = true
	payload, err := c.call("matter.append", params, c.context)
	if err != nil {
		return 0, err
	}
	handle, _ := payload["http_data"].(map[string]any)
	leaseAt, failure := matterLeaseURL(handle)
	if failure != nil {
		return 0, failure
	}
	request, err := http.NewRequest(http.MethodPut, leaseAt, strings.NewReader(text))
	if err != nil {
		return 0, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, consistency(fmt.Sprintf("the append lease could not be used: %v", err), "lease_unreachable", nil)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		return 0, &CallError{Code: "refused", Message: "the journal moved on while the append lease was in use", Details: map[string]any{"reason": "line_count_mismatch"}}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, consistency(fmt.Sprintf("the append lease was refused (HTTP %d)", response.StatusCode), "lease_refused", map[string]any{"status": response.StatusCode})
	}
	return expectedLineCount + len(lines), nil
}

// ErrConcurrencyExhausted: a guarded update lost the race on every attempt.
var ErrConcurrencyExhausted = errors.New("revision conflicts exhausted the attempts")

// Update reads, recomputes and writes guarded by expected_revision, and starts again after a short random pause when another writer got there
// first. compute gets the content and returns the new content (nil when there is nothing to write: the update is already done) and a result.
// It returns the result, or an error that wraps ErrConcurrencyExhausted after BRIQUE_CAS_ATTEMPTS lost races. Run it inside Params.Critical
// when the writers of this process should take turns instead of racing.
func (c *MatterClient) Update(matterID string, compute func(content map[string]any) (map[string]any, any, error)) (any, error) {
	attempts := settingInt("BRIQUE_CAS_ATTEMPTS")
	for attempt := 0; attempt < attempts; attempt++ {
		view, err := c.Read(matterID)
		if err != nil {
			return nil, err
		}
		next, result, err := compute(view.Content)
		if err != nil {
			return nil, err
		}
		if next == nil {
			return result, nil
		}
		if _, err = c.Write(matterID, next, &view.Revision); err != nil {
			if IsRevisionMismatch(err) {
				time.Sleep(guardedWritePause(attempt, rand.Float64(), settingInt("BRIQUE_CAS_PAUSE_BASE_MS"), settingInt("BRIQUE_CAS_PAUSE_CAP_MS")))
				continue
			}
			return nil, err
		}
		return result, nil
	}
	return nil, fmt.Errorf("%w: %s after %d attempts", ErrConcurrencyExhausted, matterID, attempts)
}
