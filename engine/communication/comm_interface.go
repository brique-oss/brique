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
	"errors"
	"fmt"
	"sync"

	"brique_engine/circulation"
	"brique_engine/junction"
)

// -----------------------------
// External interface protocol + factories (static)
// -----------------------------

type InterfaceImpl interface {
	Directions() InterfaceDirections
	Start() error
	ReadLoop() error
	WriteLoop() error
	Close() error
}

// InterfaceDirections declares the long-lived loops required by an interface.
// A direction can be operational without a context-owned loop: websocket
// ingress, for example, is owned by the instance-wide shared listener.
type InterfaceDirections struct {
	ReadLoop  bool
	WriteLoop bool
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
	InterfaceStarting
	InterfaceRunning
	InterfaceFailed
	InterfaceClosing
	InterfaceClosed
)

func (s InterfaceStatus) String() string {
	switch s {
	case InterfaceInit:
		return "init"
	case InterfaceStarting:
		return "starting"
	case InterfaceRunning:
		return "running"
	case InterfaceFailed:
		return "failed"
	case InterfaceClosing:
		return "closing"
	case InterfaceClosed:
		return "closed"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

type InterfaceState struct {
	Status  InterfaceStatus
	LastErr error
}

// InterfaceEgressOutcome reports what the physical transport knows about one
// message after it was accepted by the local interface queue.
type InterfaceEgressOutcome struct {
	Definitive bool
	StatusCode int
	Err        error
}

type interfaceEgressReport struct {
	Message circulation.Message
	Outcome InterfaceEgressOutcome
}

type signedExternalEgressItem struct {
	Key           string
	Endpoint      string
	Envelope      []byte
	Message       circulation.Message
	Purpose       string
	RequestHash   string
	AwaitResponse bool
}

type signedExternalEgressReport struct {
	Item    signedExternalEgressItem
	Outcome InterfaceEgressOutcome
}

type signedExternalIngressItem struct {
	Interface string
	Envelope  []byte
}

type wrapperInterfaceEgressItem struct {
	Connection junction.WrapperConnection
	Frame      junction.WrapperFrame
}

type InterfaceRuntime struct {
	Name   string
	Type   string
	Driver string
	Cfg    InterfaceCfg

	// Ingress is the global ingress channel owned by CommLoop.
	// ReadLoop must push incoming messages into this channel with Endpoint identifying where it came from (e.g. EndpointUI).
	Ingress chan<- IngressItem
	// SignedExternalIngress is a root-only raw-byte membrane. It is installed
	// only when a complete security runtime exists before Comm construction.
	SignedExternalIngress chan<- signedExternalIngressItem

	// Egress is the per-interface egress channel owned by CommLoop.
	// WriteLoop must consume outgoing messages from this channel.
	Egress chan circulation.Message
	// SignedExternalEgress is physically separate from ordinary Brique
	// messages. Only the verified-TLS external adapter may consume it.
	SignedExternalEgress chan signedExternalEgressItem

	// WrapperEgress is a separate transport-metadata queue. Only wrapper
	// WebSocket runtimes receive one; UI and external interfaces remain raw.
	WrapperEgress chan wrapperInterfaceEgressItem

	// PrepareEgress optionally rewrites/signs the final wire message just before transport emission.
	PrepareEgress func(circulation.Message) (circulation.Message, error)

	// ReportEgressOutcome returns transport failures to CommLoop. A definitive
	// failure means the peer explicitly rejected the message before enqueueing it;
	// network failures remain ambiguous and must not create a terminal response.
	ReportEgressOutcome         func(circulation.Message, InterfaceEgressOutcome)
	ReportSignedExternalOutcome func(signedExternalEgressItem, InterfaceEgressOutcome)

	// Concrete implementation (protocol instance).
	Impl InterfaceImpl

	// machineDirectory is the machine's security directory, "" when there is none; an HTTPS
	// interface looks there for the files its configuration does not name.
	machineDirectory string

	stateMu   sync.RWMutex
	status    InterfaceStatus
	lastErr   error
	closeOnce sync.Once
	closeErr  error
}

func (rt *InterfaceRuntime) State() InterfaceState {
	if rt == nil {
		return InterfaceState{Status: InterfaceClosed}
	}
	rt.stateMu.RLock()
	defer rt.stateMu.RUnlock()
	return InterfaceState{Status: rt.status, LastErr: rt.lastErr}
}

func (rt *InterfaceRuntime) beginStart() error {
	if rt == nil {
		return errors.New("interface runtime is nil")
	}
	rt.stateMu.Lock()
	defer rt.stateMu.Unlock()
	if rt.status != InterfaceInit {
		return errors.New("interface is not in init state")
	}
	rt.status = InterfaceStarting
	return nil
}

func (rt *InterfaceRuntime) markRunning() bool {
	if rt == nil {
		return false
	}
	rt.stateMu.Lock()
	defer rt.stateMu.Unlock()
	if rt.status != InterfaceStarting {
		return false
	}
	rt.status = InterfaceRunning
	return true
}

func (rt *InterfaceRuntime) markFailed(err error) bool {
	if rt == nil {
		return false
	}
	if err == nil {
		err = errors.New("interface failed")
	}
	rt.stateMu.Lock()
	defer rt.stateMu.Unlock()
	if rt.status == InterfaceClosing || rt.status == InterfaceClosed || rt.status == InterfaceFailed {
		return false
	}
	rt.status = InterfaceFailed
	rt.lastErr = err
	return true
}

func (rt *InterfaceRuntime) isRunning() bool {
	return rt.State().Status == InterfaceRunning
}

func (rt *InterfaceRuntime) closeImpl() error {
	if rt == nil || rt.Impl == nil {
		return nil
	}
	rt.closeOnce.Do(func() { rt.closeErr = rt.Impl.Close() })
	return rt.closeErr
}

// Close requests the concrete interface implementation to shutdown and release resources.
// Called by CommLoop.Stop().
// Close
//
// Functional role (Brique DSL):
// - stop accepting deliveries, close the concrete implementation exactly once,
//   and mark the runtime closed while retaining the last failure.
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
// - transitions the synchronized runtime state through Closing to Closed.
//
// Contract:
// - Nil runtime is treated as a successful no-op.
// - Concurrent and repeated calls are safe; the implementation is closed once.

func (rt *InterfaceRuntime) Close() error {
	if rt == nil {
		return nil
	}
	rt.stateMu.Lock()
	if rt.status == InterfaceClosed {
		err := rt.closeErr
		rt.stateMu.Unlock()
		return err
	}
	rt.status = InterfaceClosing
	rt.stateMu.Unlock()

	err := rt.closeImpl()
	if err != nil {
		rt.stateMu.Lock()
		if rt.lastErr == nil {
			rt.lastErr = err
		}
		rt.stateMu.Unlock()
	}
	rt.stateMu.Lock()
	rt.status = InterfaceClosed
	rt.stateMu.Unlock()
	return err
}
