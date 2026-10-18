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
	"path/filepath"
	"strconv"
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
	transit    *TransitService
	router     *Router
	scheduler  *WorkScheduler

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
	if !filepath.IsAbs(r.ctxDir) {
		return fmt.Errorf("BRIQUE_CTX_DIR must be an absolute path")
	}
	if strings.TrimSpace(r.ctxID) == "" || !strings.HasPrefix(r.ctxID, "/") {
		return fmt.Errorf("BRIQUE_CTX_ID must be a non-empty absolute context path")
	}
	if r.wrapperName == "" {
		return fmt.Errorf("BRIQUE_WRAPPER_NAME is not set")
	}

	url, err := ResolveWrapperURL(r.ctxDir, r.wrapperName)
	if err != nil {
		return fmt.Errorf("resolve wrapper url: %w", err)
	}

	maxConcurrency, err := positiveEnv("BRIQUE_MAX_CONCURRENCY", 16)
	if err != nil {
		return err
	}
	queueLimit, err := positiveEnv("BRIQUE_QUEUE_LIMIT", 256)
	if err != nil {
		return err
	}
	r.scheduler, err = NewWorkScheduler(maxConcurrency, queueLimit)
	if err != nil {
		return err
	}

	fragment := buildBindings(r)
	if err := r.registry.Load(fragment); err != nil {
		return fmt.Errorf("load bindings: %w", err)
	}

	r.transport = NewTransportClient(url)
	r.outbound = NewOutboundAPI(r.transport, r.correlator, r.wrapperName, r.ctxID)
	r.transit, err = NewTransitService(r)
	if err != nil {
		return fmt.Errorf("initialize transit: %w", err)
	}
	r.executor = NewCapacityExecutor(r)
	r.matters = NewMatterManager(r.registry, r.outbound)
	r.router = NewRouter(r)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := r.transport.Connect(ctx); err != nil {
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

	r.scheduler.Close()
	r.registry.RunShutdown()
	r.correlator.CancelAll("wrapper stopped")

	if receiveErr != nil && !isExpectedClose(receiveErr) {
		return receiveErr
	}
	return nil
}

func positiveEnv(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

// OutboundAPI returns the API surface for hosted code to emit intentions.
func (r *Runtime) Outbound() *OutboundAPI {
	return r.outbound
}

// Transit returns the address-driven parameter transport service.
func (r *Runtime) Transit() *TransitService { return r.transit }

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
