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
	"reflect"
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
			Interface: peer.Interface, Target: peer.Target, Endpoints: peer.Endpoints})
	}
	return out
}

func fromRuntimePeer(peer securityruntime.Peer) trust.ExternalPeer {
	return trust.ExternalPeer{PrincipalID: peer.PrincipalID, AudienceInstanceID: peer.AudienceInstanceID,
		Interface: peer.Interface, Target: peer.Target, Endpoints: peer.Endpoints}
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

// PeerEndpointAdd reaches a peer on one more machine: the base URL of its listener and the keys of
// the principal that live there (public keys or identifiers). It creates the peer when it does not
// exist, in which case the audience and the interface are required. A peer reached on a single
// target must be removed and added again to be reached on named machines.
func (d *Dir) PeerEndpointAdd(principalID, audience, iface, name, target string, keys []string) error {
	src, err := d.LoadSource()
	if err != nil {
		return err
	}
	principal := src.Principals[principalID]
	if principal == nil {
		return fmt.Errorf("peer %q: add the principal first", principalID)
	}
	if len(keys) == 0 {
		return fmt.Errorf("peer %q: an endpoint needs at least one key of the principal", principalID)
	}
	ids := make([]string, 0, len(keys))
	for _, key := range keys {
		id, err := keyID(key)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	endpoint := trust.PeerEndpoint{Name: name, Target: target, KeyIDs: ids}
	for index := range src.Peers {
		peer := &src.Peers[index]
		if peer.PrincipalID != principalID {
			continue
		}
		if len(peer.Endpoints) == 0 {
			return fmt.Errorf("peer %q is reached on a single target: remove it and add it again with named machines", principalID)
		}
		if (audience != "" && audience != peer.AudienceInstanceID) || (iface != "" && iface != peer.Interface) {
			return fmt.Errorf("peer %q already exists with another audience or interface", principalID)
		}
		peer.Endpoints = append(peer.Endpoints, endpoint)
		return d.savePeers(src)
	}
	if audience == "" || iface == "" {
		return fmt.Errorf("peer %q does not exist yet: the audience and the interface are required", principalID)
	}
	src.Peers = append(src.Peers, trust.ExternalPeer{PrincipalID: principalID, AudienceInstanceID: audience, Interface: iface,
		Endpoints: []trust.PeerEndpoint{endpoint}})
	return d.savePeers(src)
}

// PeerEndpointRemove stops reaching a peer on one machine. The last machine of a peer cannot be
// removed that way: remove the peer.
func (d *Dir) PeerEndpointRemove(principalID, name string) error {
	src, err := d.LoadSource()
	if err != nil {
		return err
	}
	for index := range src.Peers {
		peer := &src.Peers[index]
		if peer.PrincipalID != principalID {
			continue
		}
		kept := peer.Endpoints[:0:0]
		for _, endpoint := range peer.Endpoints {
			if endpoint.Name != name {
				kept = append(kept, endpoint)
			}
		}
		switch {
		case len(peer.Endpoints) == 0:
			return fmt.Errorf("peer %q has no named machines", principalID)
		case len(kept) == len(peer.Endpoints):
			return fmt.Errorf("peer %q has no machine named %q", principalID, name)
		case len(kept) == 0:
			return fmt.Errorf("peer %q: that is its last machine, remove the peer instead", principalID)
		}
		peer.Endpoints = kept
		return d.savePeers(src)
	}
	return fmt.Errorf("unknown peer %q", principalID)
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
		if !reflect.DeepEqual(index[peer.PrincipalID], peer) {
			return false
		}
	}
	return true
}
