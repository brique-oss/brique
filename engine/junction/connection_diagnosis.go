/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package junction

import (
	"context"
	"sync"
	"time"

	"brique_engine/contracts/connectiondiagnosis"
)

// ConnectionDiagnoser probes one endpoint through the interface that owns the real
// client: same roots, same server name, same dialer as an actual send.
type ConnectionDiagnoser func(ctx context.Context, peerID, endpointName, target string) connectiondiagnosis.Result

// ConnectionDiagnoserRegistry is what an interface registers itself with. Registries
// that do not diagnose (routing-only implementations, tests) need not implement it.
type ConnectionDiagnoserRegistry interface {
	RegisterConnectionDiagnoser(interfaceName string, diagnoser ConnectionDiagnoser)
	UnregisterConnectionDiagnoser(interfaceName string)
}

const (
	// DiagnosisRatePerMinute bounds the diagnoses of one peer: a diagnosis opens a real
	// connection to a remote machine, so it must not become a way to hammer it.
	DiagnosisRatePerMinute = 6
	diagnosisWindow        = time.Minute
)

type diagnosisState struct {
	mu        sync.Mutex
	diagnoser map[string]ConnectionDiagnoser
	running   map[string]bool
	history   map[string][]time.Time
}

func (r *ContextCommRegistry) diagnosis() *diagnosisState {
	r.diagnosisOnce.Do(func() {
		r.diagnosisData = &diagnosisState{diagnoser: map[string]ConnectionDiagnoser{},
			running: map[string]bool{}, history: map[string][]time.Time{}}
	})
	return r.diagnosisData
}

func (r *ContextCommRegistry) RegisterConnectionDiagnoser(interfaceName string, diagnoser ConnectionDiagnoser) {
	if r == nil || interfaceName == "" || diagnoser == nil {
		return
	}
	state := r.diagnosis()
	state.mu.Lock()
	state.diagnoser[interfaceName] = diagnoser
	state.mu.Unlock()
}

func (r *ContextCommRegistry) UnregisterConnectionDiagnoser(interfaceName string) {
	if r == nil {
		return
	}
	state := r.diagnosis()
	state.mu.Lock()
	delete(state.diagnoser, interfaceName)
	state.mu.Unlock()
}

// DiagnoseConnection diagnoses the connection to a peer declared in the signed policy.
// It never takes an address from its caller: the target is the endpoint the policy
// declares for the peer. At most one diagnosis runs per peer, and at most
// DiagnosisRatePerMinute per peer and minute.
func (r *ContextCommRegistry) DiagnoseConnection(ctx context.Context, peerID, endpointName string) connectiondiagnosis.Result {
	refuse := func(code, message string) connectiondiagnosis.Result {
		return connectiondiagnosis.Refused(peerID, endpointName, connectiondiagnosis.StepConfiguration, code, message)
	}
	runtime := r.SecurityRuntime()
	if r == nil || runtime == nil {
		return refuse(connectiondiagnosis.CodeUnavailable, "signed communication is not active on this engine")
	}
	peer, found := runtime.Peer(peerID)
	if !found {
		return refuse(connectiondiagnosis.CodeUnknownPeer, "the peer is not declared in the policy")
	}
	machines := peer.Machines()
	if len(machines) == 0 {
		return refuse(connectiondiagnosis.CodeUnknownEndpoint, "the peer declares no endpoint")
	}
	chosen := machines[0]
	if endpointName != "" {
		found = false
		for _, machine := range machines {
			if machine.Name == endpointName {
				chosen, found = machine, true
				break
			}
		}
		if !found {
			return refuse(connectiondiagnosis.CodeUnknownEndpoint, "the peer has no endpoint with this name")
		}
	}
	state := r.diagnosis()
	state.mu.Lock()
	diagnoser := state.diagnoser[peer.Interface]
	if diagnoser == nil {
		state.mu.Unlock()
		return refuse(connectiondiagnosis.CodeNoHTTPSInterface, "the interface of the peer does not send over HTTPS on this engine")
	}
	if state.running[peerID] {
		state.mu.Unlock()
		return refuse(connectiondiagnosis.CodeBusy, "a diagnosis of this peer is already running")
	}
	now := time.Now()
	recent := state.history[peerID][:0:0]
	for _, at := range state.history[peerID] {
		if now.Sub(at) < diagnosisWindow {
			recent = append(recent, at)
		}
	}
	if len(recent) >= DiagnosisRatePerMinute {
		state.history[peerID] = recent
		state.mu.Unlock()
		return refuse(connectiondiagnosis.CodeRateLimited, "too many diagnoses of this peer in the last minute")
	}
	state.history[peerID] = append(recent, now)
	state.running[peerID] = true
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		delete(state.running, peerID)
		state.mu.Unlock()
	}()
	return diagnoser(ctx, peerID, chosen.Name, chosen.Target)
}
