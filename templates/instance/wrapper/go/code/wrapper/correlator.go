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

package wrapper

import (
	"fmt"
	"sync"
)

// pendingEntry holds a channel waiting for a response to an outbound intention.
type pendingEntry struct {
	ch chan *ResponseMsg
}

// AwaitCorrelator tracks outbound intentions that are waiting for a response.
type AwaitCorrelator struct {
	mu      sync.Mutex
	pending map[string]*pendingEntry
}

func NewAwaitCorrelator() *AwaitCorrelator {
	return &AwaitCorrelator{pending: make(map[string]*pendingEntry)}
}

// Register creates a waiting channel for intentionID and returns it.
func (c *AwaitCorrelator) Register(intentionID string) <-chan *ResponseMsg {
	ch := make(chan *ResponseMsg, 1)
	c.mu.Lock()
	c.pending[intentionID] = &pendingEntry{ch: ch}
	c.mu.Unlock()
	return ch
}

// Resolve delivers a response to the waiting caller, if any.
func (c *AwaitCorrelator) Resolve(resp *ResponseMsg) {
	c.mu.Lock()
	entry, ok := c.pending[resp.IntentionID]
	if ok {
		delete(c.pending, resp.IntentionID)
	}
	c.mu.Unlock()
	if ok {
		entry.ch <- resp
	}
}

// CancelAll fails all pending entries with reason — called on shutdown.
func (c *AwaitCorrelator) CancelAll(reason string) {
	c.mu.Lock()
	pending := c.pending
	c.pending = make(map[string]*pendingEntry)
	c.mu.Unlock()
	cancelled := &ResponseMsg{Ok: false, Error: fmt.Sprintf("wrapper shutdown: %s", reason)}
	for _, entry := range pending {
		entry.ch <- cancelled
	}
}
