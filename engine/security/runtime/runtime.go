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

// Package runtime owns the complete immutable instance security runtime that
// may be published to routing. Construction alone activates no interface.
package runtime

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"

	"brique_engine/security/pending"
	"brique_engine/security/replay"
	"brique_engine/security/signer"
	"brique_engine/security/trust"
)

var (
	identifierPattern    = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)
	interfaceNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,63}$`)
)

type Peer struct {
	PrincipalID        string `json:"principal_id"`
	AudienceInstanceID string `json:"audience_instance_id"`
	Interface          string `json:"interface"`
	Target             string `json:"target"`
}

type PeerDirectory struct {
	byPrincipal map[string]Peer
}

func NewPeerDirectory(peers []Peer) (*PeerDirectory, error) {
	if peers == nil || len(peers) > 1024 {
		return nil, fmt.Errorf("external peer inventory must be present and bounded")
	}
	directory := &PeerDirectory{byPrincipal: make(map[string]Peer, len(peers))}
	audiences := make(map[string]string, len(peers))
	for _, peer := range peers {
		if !validIdentifier(peer.PrincipalID) || !validIdentifier(peer.AudienceInstanceID) || !validInterfaceName(peer.Interface) || !validHTTPSBase(peer.Target) {
			return nil, fmt.Errorf("invalid external peer")
		}
		if _, duplicate := directory.byPrincipal[peer.PrincipalID]; duplicate {
			return nil, fmt.Errorf("duplicate external peer principal")
		}
		if owner, duplicate := audiences[peer.AudienceInstanceID]; duplicate && owner != peer.PrincipalID {
			return nil, fmt.Errorf("external audience is bound to multiple principals")
		}
		audiences[peer.AudienceInstanceID] = peer.PrincipalID
		directory.byPrincipal[peer.PrincipalID] = peer
	}
	return directory, nil
}

func validInterfaceName(value string) bool {
	return interfaceNamePattern.MatchString(value)
}

func (directory *PeerDirectory) Lookup(principalID string) (Peer, bool) {
	if directory == nil {
		return Peer{}, false
	}
	peer, exists := directory.byPrincipal[principalID]
	return peer, exists
}

func (directory *PeerDirectory) Peers() []Peer {
	if directory == nil {
		return nil
	}
	result := make([]Peer, 0, len(directory.byPrincipal))
	for _, peer := range directory.byPrincipal {
		result = append(result, peer)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PrincipalID < result[j].PrincipalID })
	return result
}

type Runtime struct {
	instanceID string
	signers    *signer.Set
	trust      *trust.Runtime
	peers      *PeerDirectory
	replay     replay.Coordinator
	pending    *pending.Registry
	closeOnce  sync.Once
	closeErr   error
}

func New(instanceID string, signers *signer.Set, trustRuntime *trust.Runtime, peers *PeerDirectory, replayCoordinator replay.Coordinator, pendingResponses *pending.Registry) (*Runtime, error) {
	if !validIdentifier(instanceID) || signers == nil || signers.Active() == nil || trustRuntime == nil || trustRuntime.InstanceID() != instanceID || peers == nil || replayCoordinator == nil || pendingResponses == nil {
		return nil, fmt.Errorf("complete security runtime is required")
	}
	return &Runtime{instanceID: instanceID, signers: signers, trust: trustRuntime, peers: peers, replay: replayCoordinator, pending: pendingResponses}, nil
}

func (runtime *Runtime) InstanceID() string                 { return runtime.instanceID }
func (runtime *Runtime) Signers() *signer.Set               { return runtime.signers }
func (runtime *Runtime) Trust() *trust.Runtime              { return runtime.trust }
func (runtime *Runtime) Peer(principal string) (Peer, bool) { return runtime.peers.Lookup(principal) }
func (runtime *Runtime) Replay() replay.Coordinator         { return runtime.replay }
func (runtime *Runtime) Pending() *pending.Registry         { return runtime.pending }

func (runtime *Runtime) Close() error {
	if runtime == nil {
		return nil
	}
	runtime.closeOnce.Do(func() {
		if runtime.replay != nil {
			runtime.closeErr = runtime.replay.Close()
		}
		if err := runtime.signers.Close(); runtime.closeErr == nil {
			runtime.closeErr = err
		}
	})
	return runtime.closeErr
}

func validIdentifier(value string) bool { return identifierPattern.MatchString(value) }

func validHTTPSBase(value string) bool {
	if strings.TrimSpace(value) != value || value == "" {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Path == "" || strings.HasPrefix(parsed.Path, "/")
}
