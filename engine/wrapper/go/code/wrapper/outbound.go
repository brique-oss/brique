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
	"time"
)

// OutboundParams describes an outbound intention emitted by hosted code.
type OutboundParams struct {
	ToContext     string
	ToCap         string
	ToType        string
	FromContext   string
	FromCap       string
	FromType      string
	Params        map[string]any
	AwaitResponse bool
}

// OutboundAPI is the hosted-code-facing API for emitting Brique intentions.
type OutboundAPI struct {
	transport   *TransportClient
	correlator  *AwaitCorrelator
	wrapperName string
	ctxID       string
}

func NewOutboundAPI(transport *TransportClient, correlator *AwaitCorrelator, wrapperName, ctxID string) *OutboundAPI {
	return &OutboundAPI{
		transport:   transport,
		correlator:  correlator,
		wrapperName: wrapperName,
		ctxID:       ctxID,
	}
}

// EmitIntention sends an outbound intention. If AwaitResponse is true it blocks until the response arrives.
func (o *OutboundAPI) EmitIntention(p OutboundParams) error {
	_, err := o.emitInternal(p)
	return err
}

// EmitAndWait sends an outbound intention and returns the response payload.
func (o *OutboundAPI) EmitAndWait(p OutboundParams) (map[string]any, error) {
	p.AwaitResponse = true
	return o.emitInternal(p)
}

func (o *OutboundAPI) emitInternal(p OutboundParams) (map[string]any, error) {
	id := newID()
	fromCtx := p.FromContext
	fromCap := p.FromCap
	fromType := p.FromType
	if fromType == "" {
		fromType = TypeExecution
	}

	env := Envelope{
		Kind: KindIntention,
		Ts:   utcNow(),
		Intention: &IntentionMsg{
			IntentionID:   id,
			AwaitResponse: p.AwaitResponse,
			To: Address{
				Context: p.ToContext,
				Cap:     p.ToCap,
				Type:    p.ToType,
			},
			From: Address{
				Context: fromCtx,
				Cap:     fromCap,
				Type:    fromType,
			},
			Identity: map[string]any{
				"id":   o.wrapperName,
				"kind": "wrapper",
			},
			Params:      p.Params,
			Correlation: Correlation{},
		},
	}

	var ch <-chan *ResponseMsg
	if p.AwaitResponse {
		ch = o.correlator.Register(id)
		defer o.correlator.Unregister(id)
	}

	if err := o.transport.Send(env); err != nil {
		return nil, fmt.Errorf("outbound send: %w", err)
	}

	if !p.AwaitResponse {
		return nil, nil
	}

	resp, ok := awaitFinalResponse(ch, 30*time.Second)
	if !ok {
		return nil, fmt.Errorf("outbound intention timeout: %s", id)
	}
	if resp == nil || (!resp.Ok && resp.Status != StatusOK) {
		errMsg := "outbound intention failed"
		if resp != nil && resp.Error != "" {
			errMsg = resp.Error
		}
		return nil, fmt.Errorf("%s", errMsg)
	}
	return resp.Payload, nil
}

// awaitFinalResponse treats running as a heartbeat and gives each heartbeat a
// fresh timeout budget. Only a terminal response completes the wait.
func awaitFinalResponse(ch <-chan *ResponseMsg, timeout time.Duration) (*ResponseMsg, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case resp, open := <-ch:
			if !open {
				return nil, false
			}
			if resp != nil && resp.Status == StatusRunning {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(timeout)
				continue
			}
			return resp, true
		case <-timer.C:
			return nil, false
		}
	}
}

func utcNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func newID() string {
	return fmt.Sprintf("w_%d", time.Now().UnixNano())
}
