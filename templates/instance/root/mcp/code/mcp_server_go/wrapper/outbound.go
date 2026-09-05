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
	"encoding/json"
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

// SendRawIntention sends a raw Brique intention map as-is and waits for the response.
func (o *OutboundAPI) SendRawIntention(intention map[string]any) (map[string]any, error) {
	id, _ := intention["intention_id"].(string)
	if id == "" {
		id = newID()
	}
	awaitResponse, _ := intention["await_response"].(bool)

	ch := o.correlator.Register(id)

	env := map[string]any{
		"kind":      KindIntention,
		"ts":        utcNow(),
		"intention": intention,
	}

	if err := o.transport.Send(env); err != nil {
		return nil, fmt.Errorf("outbound send: %w", err)
	}

	if !awaitResponse {
		return nil, nil
	}

	select {
	case resp := <-ch:
		if resp == nil || !resp.Ok {
			errMsg := "outbound intention failed"
			if resp != nil && resp.Error != "" {
				errMsg = resp.Error
			}
			if resp != nil && len(resp.ErrorDetails) > 0 {
				if b, err := json.Marshal(resp.ErrorDetails); err == nil {
					errMsg = fmt.Sprintf("%s | details: %s", errMsg, string(b))
				}
			}
			return nil, fmt.Errorf("%s", errMsg)
		}
		return resp.Payload, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("outbound intention timeout: %s", id)
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
			Params:    p.Params,
			Correlation: Correlation{},
		},
	}

	var ch <-chan *ResponseMsg
	if p.AwaitResponse {
		ch = o.correlator.Register(id)
	}

	if err := o.transport.Send(env); err != nil {
		return nil, fmt.Errorf("outbound send: %w", err)
	}

	if !p.AwaitResponse {
		return nil, nil
	}

	select {
	case resp := <-ch:
		if resp == nil || !resp.Ok {
			errMsg := "outbound intention failed"
			if resp != nil && resp.Error != "" {
				errMsg = resp.Error
			}
			if resp != nil && len(resp.ErrorDetails) > 0 {
				if b, err := json.Marshal(resp.ErrorDetails); err == nil {
					errMsg = fmt.Sprintf("%s | details: %s", errMsg, string(b))
				}
			}
			return nil, fmt.Errorf("%s", errMsg)
		}
		return resp.Payload, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("outbound intention timeout: %s", id)
	}
}

func utcNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func newID() string {
	return fmt.Sprintf("w_%d", time.Now().UnixNano())
}
