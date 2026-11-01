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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"

	securityruntime "brique_engine/security/runtime"
	"brique_engine/security/trust"
)

// PeersFile lists the outbound peers of the instance (SECURITY.md §7.4). It
// is merged into the bootstrap of the next generation by apply; a directory without
// it keeps the peers its bootstrap already has.
const PeersFile = "source/peers.json"

// Peers returns the peers of the source, or nil and false when the file does not
// exist yet.
func (d *Dir) Peers() ([]securityruntime.Peer, bool, error) {
	if _, err := os.Stat(d.abs(PeersFile)); isNotExist(err) {
		return nil, false, nil
	}
	content, err := d.readProtected(PeersFile, 1<<20)
	if err != nil {
		return nil, false, err
	}
	var peers []securityruntime.Peer
	if err := trust.StrictDecodeJSON(content, &peers); err != nil {
		return nil, false, fmt.Errorf("peers file: %w", err)
	}
	return peers, true, nil
}

func (d *Dir) savePeers(peers []securityruntime.Peer) error {
	if _, err := securityruntime.NewPeerDirectory(peers); err != nil {
		return err
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].PrincipalID < peers[j].PrincipalID })
	encoded, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		return err
	}
	return d.writeFile(PeersFile, append(encoded, '\n'), 0o600)
}

// currentPeers is the peers file, or the peers of the installed bootstrap when no
// file exists yet.
func (d *Dir) currentPeers() ([]securityruntime.Peer, error) {
	peers, found, err := d.Peers()
	if err != nil || found {
		if peers == nil {
			peers = []securityruntime.Peer{}
		}
		return peers, err
	}
	rel := path.Join("source", "bootstrap.template.json")
	if head, err := d.Generations(); err == nil && len(head) > 0 {
		rel = path.Join(GenerationsDir, head[len(head)-1].Name, BootstrapFile)
	}
	document, _, err := d.loadBootstrap(rel)
	if err != nil {
		return nil, err
	}
	return append([]securityruntime.Peer{}, document.ExternalPeers...), nil
}

// PeerAdd declares the receiver-side binding of a peer principal to a remote
// instance: audience, local HTTPS adapter and exact base URL. The inventory is
// validated by the engine's own rules before anything is written.
func (d *Dir) PeerAdd(peer securityruntime.Peer) error {
	peers, err := d.currentPeers()
	if err != nil {
		return err
	}
	for _, existing := range peers {
		if existing.PrincipalID == peer.PrincipalID {
			return fmt.Errorf("peer %q already exists", peer.PrincipalID)
		}
	}
	return d.savePeers(append(peers, peer))
}

// PeerRemove deletes the peer of a principal.
func (d *Dir) PeerRemove(principalID string) error {
	peers, err := d.currentPeers()
	if err != nil {
		return err
	}
	kept := peers[:0:0]
	for _, peer := range peers {
		if peer.PrincipalID != principalID {
			kept = append(kept, peer)
		}
	}
	if len(kept) == len(peers) {
		return fmt.Errorf("unknown peer %q", principalID)
	}
	return d.savePeers(kept)
}

// PeerList returns the peers that the next generation will carry.
func (d *Dir) PeerList() ([]securityruntime.Peer, error) {
	peers, err := d.currentPeers()
	sort.Slice(peers, func(i, j int) bool { return peers[i].PrincipalID < peers[j].PrincipalID })
	return peers, err
}

func samePeers(a, b []securityruntime.Peer) bool {
	if len(a) != len(b) {
		return false
	}
	index := make(map[string]securityruntime.Peer, len(a))
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

func isNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }
