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

package secadmin

import (
	"fmt"
	"sort"

	"brique_engine/security/policysrc"
	securityruntime "brique_engine/security/runtime"
	"brique_engine/security/trust"
)

// Peers say where the instance reaches the principals it sends to (SECURITY.md §7.4). They
// live in the access source and compile into the signed policy: an address changes only
// with a signed generation, and travels with the instance like the rest of the policy.

func toRuntimePeers(peers []trust.ExternalPeer) []securityruntime.Peer {
	out := make([]securityruntime.Peer, 0, len(peers))
	for _, peer := range peers {
		out = append(out, securityruntime.Peer{PrincipalID: peer.PrincipalID, AudienceInstanceID: peer.AudienceInstanceID,
			Interface: peer.Interface, Target: peer.Target})
	}
	return out
}

func fromRuntimePeer(peer securityruntime.Peer) trust.ExternalPeer {
	return trust.ExternalPeer{PrincipalID: peer.PrincipalID, AudienceInstanceID: peer.AudienceInstanceID,
		Interface: peer.Interface, Target: peer.Target}
}

// PeerAdd declares the receiver-side binding of a peer principal to a remote
// instance: audience, local HTTPS adapter and exact base URL. The principal must exist, and
// the whole inventory is validated by the engine's own rules before anything is written.
func (d *Dir) PeerAdd(peer securityruntime.Peer) error {
	src, err := d.LoadSource()
	if err != nil {
		return err
	}
	if src.Principals[peer.PrincipalID] == nil {
		return fmt.Errorf("peer %q: add the principal first", peer.PrincipalID)
	}
	for _, existing := range src.Peers {
		if existing.PrincipalID == peer.PrincipalID {
			return fmt.Errorf("peer %q already exists", peer.PrincipalID)
		}
	}
	src.Peers = append(src.Peers, fromRuntimePeer(peer))
	return d.savePeers(src)
}

// PeerRemove deletes the peer of a principal.
func (d *Dir) PeerRemove(principalID string) error {
	src, err := d.LoadSource()
	if err != nil {
		return err
	}
	kept := src.Peers[:0:0]
	for _, peer := range src.Peers {
		if peer.PrincipalID != principalID {
			kept = append(kept, peer)
		}
	}
	if len(kept) == len(src.Peers) {
		return fmt.Errorf("unknown peer %q", principalID)
	}
	src.Peers = kept
	return d.savePeers(src)
}

// PeerList returns the peers that the next generation will carry.
func (d *Dir) PeerList() ([]securityruntime.Peer, error) {
	src, err := d.LoadSource()
	if err != nil {
		return nil, err
	}
	peers := toRuntimePeers(src.Peers)
	sort.Slice(peers, func(i, j int) bool { return peers[i].PrincipalID < peers[j].PrincipalID })
	return peers, nil
}

// savePeers checks the transport rules of the whole inventory (https, audience bindings)
// and writes the source.
func (d *Dir) savePeers(src *policysrc.Source) error {
	if _, err := securityruntime.NewPeerDirectory(toRuntimePeers(src.Peers)); err != nil {
		return err
	}
	sort.Slice(src.Peers, func(i, j int) bool { return src.Peers[i].PrincipalID < src.Peers[j].PrincipalID })
	return d.SaveSource(src)
}

func samePeers(a, b []trust.ExternalPeer) bool {
	if len(a) != len(b) {
		return false
	}
	index := make(map[string]trust.ExternalPeer, len(a))
	for _, peer := range a {
		index[peer.PrincipalID] = peer
	}
	for _, peer := range b {
		if index[peer.PrincipalID] != peer {
			return false
		}
	}
	return true
}
