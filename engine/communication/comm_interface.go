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

package comm

import (
	"brique_engine/circulation"
	"brique_engine/junction"
)

// -----------------------------
// External interface protocol + factories (static)
// -----------------------------

type InterfaceImpl interface {
	Start() error
	ReadLoop() error
	WriteLoop() error
	Close() error
}

// -----------------------------
// Interface runtime (IO contact surface)
// -----------------------------

// IngressItem carries an ingress message along with its source.
// Endpoint is a small namespace string (e.g. ui, innerCtx, outerCtx, wrapper, ...).

type IngressItem = junction.InterfaceIngressItem

// Endpoint namespace (minimal, static).
const (
	EndpointUI       = "ui"
	EndpointInnerCtx = "innerCtx"
	EndpointOuterCtx = "outerCtx"
	EndpointWrapper  = "wrapper"
)

type InterfaceStatus int

const (
	InterfaceInit InterfaceStatus = iota
	InterfaceRunning
	InterfaceFailed
	InterfaceClosed
)

type InterfaceRuntime struct {
	Name   string
	Type   string
	Driver string
	Cfg    InterfaceCfg

	// Ingress is the global ingress channel owned by CommLoop.
	// ReadLoop must push incoming messages into this channel with Endpoint identifying where it came from (e.g. EndpointUI).
	Ingress chan<- IngressItem

	// Egress is the per-interface egress channel owned by CommLoop.
	// WriteLoop must consume outgoing messages from this channel.
	Egress chan circulation.Message

	// PrepareEgress optionally rewrites/signs the final wire message just before transport emission.
	PrepareEgress func(circulation.Message) (circulation.Message, error)

	// Status is updated by Start()/Close() and can be inspected by CommLoop.
	Status InterfaceStatus

	// Err stores the last start error (if any).
	Err error

	// Concrete implementation (protocol instance).
	Impl InterfaceImpl
}

// Close requests the concrete interface implementation to shutdown and release resources.
// Called by CommLoop.Stop().
// Close
//
// Functional role (Brique DSL):
// - close concrete interface implementation and mark interface runtime as closed.
//
// Expected Message Fields:
// - none (function does not read message envelope fields directly).
//
// Expected Params Keys/values:
// - none (no `msg.Intention.Params[...]` key consumed directly in this function).
//
// Inputs:
// - receiver `rt *InterfaceRuntime`.
//
// Outputs:
// - returns error.
//
// State/Storage Effects:
// - delegates shutdown to `rt.Impl.Close()` when a concrete implementation exists.
// - sets `rt.Status = InterfaceClosed` after the delegated close returns.
//
// Contract:
// - Nil runtime or nil implementation is treated as a successful no-op.
// - `Status` is updated only when a concrete implementation exists and `Impl.Close()` has been called.

func (rt *InterfaceRuntime) Close() error {
	if rt == nil || rt.Impl == nil {
		return nil
	}
	err := rt.Impl.Close()
	rt.Status = InterfaceClosed
	return err
}
