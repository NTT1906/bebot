package main

import (
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// serverProtocol lets gophertunnel speak the exact protocol advertised by the
// server. This is important for 1.26.x servers: using only the library's
// default protocol can get through RakNet but fail during the login/start-game
// handshake.
type serverProtocol struct {
	id      int32
	version string
}

func (p serverProtocol) ID() int32   { return p.id }
func (p serverProtocol) Ver() string { return p.version }
func (p serverProtocol) Packets(listener bool) packet.Pool {
	if listener {
		return packet.NewClientPool()
	}
	return packet.NewServerPool()
}
func (p serverProtocol) NewReader(r minecraft.ByteReader, shieldID int32, limits bool) protocol.IO {
	return protocol.NewReader(r, shieldID, limits)
}
func (p serverProtocol) NewWriter(w minecraft.ByteWriter, shieldID int32) protocol.IO {
	return protocol.NewWriter(w, shieldID)
}

func (p serverProtocol) ConvertToLatest(pk packet.Packet, _ *minecraft.Conn) []packet.Packet {
	// The bot does not install server packs. Replying with an empty pack list
	// mirrors the working gophertunnel example and prevents BDS 1.26.x from
	// repeating ResourcePackStack forever before StartGame.
	if _, ok := pk.(*packet.ResourcePacksInfo); ok {
		return []packet.Packet{&packet.ResourcePacksInfo{TexturePackRequired: false, HasAddons: false, HasScripts: false}}
	}
	if stack, ok := pk.(*packet.ResourcePackStack); ok {
		stack.TexturePacks = nil
		return []packet.Packet{stack}
	}
	return []packet.Packet{pk}
}
func (p serverProtocol) ConvertFromLatest(pk packet.Packet, _ *minecraft.Conn) []packet.Packet {
	if _, ok := pk.(*packet.ResourcePacksInfo); ok {
		return []packet.Packet{&packet.ResourcePacksInfo{TexturePackRequired: false, HasAddons: false, HasScripts: false}}
	}
	return []packet.Packet{pk}
}
