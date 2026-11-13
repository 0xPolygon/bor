// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package p2p

import "github.com/ethereum/go-ethereum/p2p/enode"

// Inbound RLPx handshakes only carry the identity, not the signed ENR containing
// the QUIC endpoint. Recover that record locally without delaying the handshake.
func (b *BulkSidecar) peerRecord(remote *enode.Node) *enode.Node {
	if b.srv == nil {
		return remote
	}
	if b.srv.nodedb != nil {
		remote = newerBulkPeerRecord(remote, b.srv.nodedb.Node(remote.ID()))
	}
	remote = b.discoveryPeerRecord(remote)
	for _, nodes := range [][]*enode.Node{b.srv.StaticNodes, b.srv.TrustedNodes, b.srv.BootstrapNodes, b.srv.BootstrapNodesV5} {
		for _, node := range nodes {
			remote = newerBulkPeerRecord(remote, node)
		}
	}
	return remote
}

func (b *BulkSidecar) discoveryPeerRecord(remote *enode.Node) *enode.Node {
	if b.srv.discv4 != nil {
		for _, bucket := range b.srv.discv4.TableBuckets() {
			for _, node := range bucket {
				remote = newerBulkPeerRecord(remote, node.Node)
			}
		}
	}
	if b.srv.discv5 != nil {
		remote = newerBulkPeerRecord(remote, b.srv.discv5.GetNode(remote.ID()))
	}
	return remote
}

func newerBulkPeerRecord(remote, candidate *enode.Node) *enode.Node {
	if candidate != nil && candidate.ID() == remote.ID() && candidate.Seq() > remote.Seq() {
		return candidate
	}
	return remote
}
