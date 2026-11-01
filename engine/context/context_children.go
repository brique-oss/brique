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

package context

// context_children.go
//
// Child lifecycle control of a ContextLoop: start, stop and restart of the
// direct children declared (or later started) under this context.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"brique_engine/circulation"
	"brique_engine/shared"
)

type childOp uint8

const (
	childOpStart childOp = iota
	childOpStop
	childOpRestart
)

func (o childOp) String() string {
	switch o {
	case childOpStart:
		return "start"
	case childOpStop:
		return "stop"
	default:
		return "restart"
	}
}

// childOpForCap maps a control capacity to a child operation. `start` and
// `restart` are historical aliases of `context.start` and `context.restart`.
func childOpForCap(cap string) (childOp, bool) {
	switch cap {
	case "context.start", "start":
		return childOpStart, true
	case "context.stop":
		return childOpStop, true
	case "context.restart", "restart":
		return childOpRestart, true
	}
	return 0, false
}

// validChildName accepts exactly one path segment: a child is always a direct
// sub-directory of its parent, so separators and dot segments are refused.
func validChildName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.TrimSpace(name) != name {
		return false
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return false
	}
	return filepath.Base(name) == name
}

// parentAlive reports whether this context can still accept child operations.
func (c *ContextLoop) parentAlive() (bool, shared.ContextState) {
	st := c.State()
	if st != shared.ContextRunning {
		return false, st
	}
	select {
	case <-c.done:
		return false, shared.ContextStopping
	default:
		return true, st
	}
}

// newChild builds and starts one child loop from its directory.
func (c *ContextLoop) newChild(op childOp, name string) (*ContextLoop, string) {
	child, err := NewContextLoop(filepath.Join(c.dir, name), c.frame.CtxId+"/"+name, c.frame.CtxCommReg)
	if err != nil {
		return nil, err.Error()
	}
	if child.State() == shared.ContextFailed {
		return nil, fmt.Sprintf("%s child %q failed to initialize", op, name)
	}
	return child, ""
}

// removeChild stops the child and forgets it, if it is still the registered one.
func (c *ContextLoop) removeChild(name string, old *ContextLoop) {
	old.Stop()
	c.childMu.Lock()
	if cur, ok := c.childLoops[name]; ok && cur == old {
		delete(c.childLoops, name)
	}
	c.childMu.Unlock()
}

// attachChild registers a freshly started child, or stops it when the parent
// stopped meanwhile so that no child outlives its parent.
func (c *ContextLoop) attachChild(op childOp, name string, child *ContextLoop) (bool, string) {
	if alive, st := c.parentAlive(); !alive {
		child.Stop()
		return false, fmt.Sprintf("cannot %s child %q: parent context not running anymore (state=%v)", op, name, st)
	}
	c.childMu.Lock()
	c.childLoops[name] = child
	known := false
	for _, existing := range c.Children {
		if existing == name {
			known = true
			break
		}
	}
	if !known {
		c.Children = append(c.Children, name)
	}
	c.childMu.Unlock()
	return true, ""
}

// controlOneChild applies op to one child. Callers hold c.opMu, which makes the
// lookup / stop / create / register sequence atomic with respect to other
// child operations and to Stop.
func (c *ContextLoop) controlOneChild(op childOp, name string) (bool, string) {
	if name == "" {
		return false, "child name is empty"
	}
	if !validChildName(name) {
		return false, fmt.Sprintf("invalid child name %q", name)
	}
	if alive, st := c.parentAlive(); !alive {
		return false, fmt.Sprintf("cannot %s child %q: parent context not running (state=%v)", op, name, st)
	}

	c.childMu.RLock()
	old := c.childLoops[name]
	c.childMu.RUnlock()

	switch op {
	case childOpStart:
		if old != nil {
			return false, fmt.Sprintf("child %q already loaded; use restart", name)
		}
		if _, err := os.Stat(filepath.Join(c.dir, name, briqueContextFilename)); err != nil {
			return false, fmt.Sprintf("cannot start child %q: no context.json on disk (%s)", name, err.Error())
		}
	case childOpStop, childOpRestart:
		if old == nil {
			return false, fmt.Sprintf("child %q not found", name)
		}
		c.removeChild(name, old)
		if op == childOpStop {
			return true, ""
		}
	}

	child, reason := c.newChild(op, name)
	if child == nil {
		return false, reason
	}
	return c.attachChild(op, name, child)
}

// runChildOp applies op to every named child, then answers the intention once.
func (c *ContextLoop) runChildOp(op childOp, names []string, in circulation.Intention) {
	c.runChildOpScoped(op, names, in, nil)
}

// notFoundReason is what a restricted caller is told about a child it may not
// control and about one that does not exist: the same words.
const notFoundReason = "child not found"

// runChildOpScoped is runChildOp for a caller whose rights were decided by the
// door. A child the caller may not control is never touched and is answered like
// an absent one; for such a caller the reasons that describe the host (paths,
// system errors) are replaced as well, so that absence has one wording.
func (c *ContextLoop) runChildOpScoped(op childOp, names []string, in circulation.Intention, scope *controlScope) {
	c.opMu.Lock()
	results := make([]map[string]any, 0, len(names))
	allOK := len(names) > 0
	for _, name := range names {
		var ok bool
		var reason string
		if scope.refused(name) {
			ok, reason = false, notFoundReason
		} else {
			ok, reason = c.controlOneChild(op, name)
			if scope != nil && !ok && (strings.Contains(reason, "not found") || strings.Contains(reason, "no context.json on disk")) {
				reason = notFoundReason
			}
		}
		allOK = allOK && ok
		entry := map[string]any{circulation.KeyChild: name, circulation.KeyOK: ok}
		if reason != "" {
			entry[circulation.KeyReason] = reason
		}
		results = append(results, entry)
	}
	c.opMu.Unlock()

	if allOK {
		c.respond(in, circulation.Response{
			Status:  circulation.ValueStatusOK,
			Payload: map[string]any{circulation.KeyResult: results},
		})
		return
	}
	c.respondControlError(in, circulation.ValueCodeRefused,
		"one or more children did not complete the requested control operation",
		map[string]any{circulation.KeyResult: results})
}

// StartChildren, StopChildren and RestartChildren run one child operation on
// the named children and answer the intention once.
func (c *ContextLoop) StartChildren(names []string, in circulation.Intention) {
	c.runChildOp(childOpStart, names, in)
}

func (c *ContextLoop) StopChildren(names []string, in circulation.Intention) {
	c.runChildOp(childOpStop, names, in)
}

func (c *ContextLoop) RestartChildren(names []string, in circulation.Intention) {
	c.runChildOp(childOpRestart, names, in)
}

// respondControlError answers a control intention with a protocol error.
func (c *ContextLoop) respondControlError(in circulation.Intention, code, message string, details map[string]any) {
	c.respond(in, circulation.Response{
		Status: circulation.ValueStatusError,
		Error:  &circulation.ResponseProblem{Code: code, Message: message, Details: details},
	})
}

// respond completes resp with the correlation of in and hands it to
// Communication under this context's local authority.
func (c *ContextLoop) respond(in circulation.Intention, resp circulation.Response) {
	resp.IntentionID = in.IntentionID
	resp.To = in.From
	resp.From = in.To
	if c.frame.FamIn == nil {
		return
	}
	commCh := c.frame.FamIn[shared.FamilyComm]
	if commCh == nil {
		return
	}
	select {
	case commCh <- c.frame.MustDelivery(circulation.Message{Kind: circulation.ValueKindResponse, Response: resp}):
	case <-c.done:
	}
}

// parseControlChildren reads `params.child`: a name or a list of names.
func parseControlChildren(params map[string]any) ([]string, error) {
	if params == nil {
		return nil, errors.New("params is required")
	}
	raw, ok := params[circulation.KeyChild]
	if !ok {
		return nil, errors.New("params.child is required")
	}
	switch v := raw.(type) {
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, errors.New("params.child must not be empty")
		}
		return []string{v}, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				continue
			}
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil, errors.New("params.child must contain at least one non-empty name")
		}
		return out, nil
	default:
		return nil, errors.New("params.child must be a string or an array of strings")
	}
}

// sortedNames returns the keys in a stable order.
func sortedNames(m map[string]*ContextLoop) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		if name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
