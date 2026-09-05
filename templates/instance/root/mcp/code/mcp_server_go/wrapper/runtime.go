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
	"context"
	"fmt"
	"log"
	"os"
	"strings"
)

// Runtime is the top-level wrapper process coordinator.
type Runtime struct {
	ctxDir      string
	ctxID       string
	wrapperName string

	transport  *TransportClient
	correlator *AwaitCorrelator
	registry   *Registry
	executor   *CapacityExecutor
	outbound   *OutboundAPI
	matters    *MatterManager
	router     *Router

	stopCh chan struct{}
}

// NewRuntime creates a Runtime from environment variables injected by the Brique engine.
func NewRuntime() *Runtime {
	ctxDir := strings.TrimSpace(os.Getenv("BRIQUE_CTX_DIR"))
	ctxID := strings.TrimSpace(os.Getenv("BRIQUE_CTX_ID"))
	wrapperName := strings.TrimSpace(os.Getenv("BRIQUE_WRAPPER_NAME"))

	r := &Runtime{
		ctxDir:      ctxDir,
		ctxID:       ctxID,
		wrapperName: wrapperName,
		correlator:  NewAwaitCorrelator(),
		registry:    NewRegistry(),
		stopCh:      make(chan struct{}),
	}
	return r
}

// Run is the main entry point: load, connect, announce ready, receive loop.
func (r *Runtime) Run(buildBindings func(*Runtime) BindingsFragment) error {
	if r.ctxDir == "" {
		return fmt.Errorf("BRIQUE_CTX_DIR is not set")
	}
	if r.wrapperName == "" {
		return fmt.Errorf("BRIQUE_WRAPPER_NAME is not set")
	}

	url, err := ResolveWrapperURL(r.ctxDir, r.wrapperName)
	if err != nil {
		return fmt.Errorf("resolve wrapper url: %w", err)
	}

	r.transport = NewTransportClient(url)
	r.outbound = NewOutboundAPI(r.transport, r.correlator, r.wrapperName, r.ctxID)
	r.executor = NewCapacityExecutor(r.registry)
	r.matters = NewMatterManager(r.registry, r.outbound)
	r.router = NewRouter(r)

	fragment := buildBindings(r)
	r.registry.Load(fragment)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := r.transport.Connect(ctx); err != nil {
		if isRefused(err) {
			return nil
		}
		return fmt.Errorf("transport connect: %w", err)
	}
	defer r.transport.Close()

	if err := r.transport.Send(r.wrapperReadyMessage()); err != nil {
		return fmt.Errorf("wrapper_ready send: %w", err)
	}

	go func() {
		<-r.stopCh
		cancel()
		r.transport.Close()
	}()

	receiveErr := r.transport.ReceiveLoop(ctx, func(raw []byte) {
		r.router.Route(raw)
	})

	r.registry.RunShutdown()
	r.correlator.CancelAll("wrapper stopped")

	if receiveErr != nil && !isExpectedClose(receiveErr) {
		return receiveErr
	}
	return nil
}

// OutboundAPI returns the API surface for hosted code to emit intentions.
func (r *Runtime) Outbound() *OutboundAPI {
	return r.outbound
}

func (r *Runtime) requestStop() {
	select {
	case <-r.stopCh:
	default:
		close(r.stopCh)
	}
}

func (r *Runtime) wrapperReadyMessage() Envelope {
	return Envelope{
		Kind: KindIntention,
		Ts:   utcNow(),
		Intention: &IntentionMsg{
			IntentionID:   newID(),
			AwaitResponse: false,
			To: Address{
				Context: r.ctxID,
				Cap:     CapWrapperReady,
				Type:    TypeExecution,
			},
			From: Address{
				Context: "",
				Cap:     "",
				Type:    TypeExecution,
			},
			Identity: map[string]any{
				"id":   r.wrapperName,
				"kind": "wrapper",
			},
			Params: map[string]any{
				"wrapper": r.wrapperName,
			},
			Correlation: Correlation{},
		},
	}
}

func (r *Runtime) log(format string, args ...any) {
	log.Printf("[wrapper:"+r.wrapperName+"] "+format, args...)
}
