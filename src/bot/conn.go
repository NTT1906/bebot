package bot

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"math/rand/v2"
	"net"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	bauth "bebot/src/auth"
	"bebot/src/config"
	netproto "bebot/src/protocol"
	bskin "bebot/src/skin"

	"github.com/df-mc/go-nethernet"
	"github.com/df-mc/go-nethernet/discovery"
	"github.com/google/uuid"
	"github.com/sandertv/go-raknet"
	"github.com/sandertv/gophertunnel/minecraft"
	mauth "github.com/sandertv/gophertunnel/minecraft/auth"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// pingServer pings the given Bedrock address with a short per-attempt deadline
// and parses the RakNet or NetherNet pong.
func pingServer(ctx context.Context, address string) (netproto.ServerPong, string, string, error) {
	// 1. Try RakNet first (with a short 3s timeout)
	rakCtx, rakCancel := context.WithTimeout(ctx, 3*time.Second)
	defer rakCancel()
	pongBytes, rakErr := raknet.PingContext(rakCtx, address)
	if rakErr == nil {
		if p, err := netproto.ParseServerPong(pongBytes); err == nil {
			return p, "raknet", address, nil
		}
	}

	// 2. If RakNet fails/times out, try NetherNet (HTTPS / HTTP)
	netherCtx, netherCancel := context.WithTimeout(ctx, 5*time.Second)
	defer netherCancel()
	n := minecraft.NetherNet{}
	for _, scheme := range []string{"https://", "http://"} {
		target := scheme + address
		pongBytes, nErr := n.PingContext(netherCtx, target)
		if nErr == nil {
			if p, err := netproto.ParseServerPong(pongBytes); err == nil {
				return p, "nethernet", target, nil
			}
		}
	}

	// 3. If local address (127.0.0.1 or localhost) on port 19132 failed, check if Bedrock LAN opened on 19133..19140
	host, portStr, splitErr := net.SplitHostPort(address)
	if splitErr == nil && (host == "127.0.0.1" || host == "localhost") && portStr == "19132" {
		for testPort := 19133; testPort <= 19140; testPort++ {
			testAddr := fmt.Sprintf("%s:%d", host, testPort)
			tCtx, tCancel := context.WithTimeout(ctx, 500*time.Millisecond)
			pBytes, pErr := raknet.PingContext(tCtx, testAddr)
			tCancel()
			if pErr == nil {
				if p, err := netproto.ParseServerPong(pBytes); err == nil {
					log.Printf("bebot: discovered local LAN world on port %d\n", testPort)
					return p, "raknet", testAddr, nil
				}
			}
		}
	}

	return netproto.ServerPong{}, "", "", fmt.Errorf("ping %s: raknet (%v) and nethernet failed", address, rakErr)
}

func (b *Bot) connectLocal(ctx context.Context) error {
	cfg := discovery.ListenConfig{
		NetworkID: rand.Uint64(),
	}
	d, err := cfg.Listen("0.0.0.0:0")
	if err != nil {
		return fmt.Errorf("lan discovery listen: %w", err)
	}
	defer d.Close()

	log.Println("bebot: discovering local Bedrock world on LAN (port 7551)...")
	var (
		serverData discovery.ServerData
		networkID  uint64
		found      bool
	)

	timeout := time.After(8 * time.Second)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

DiscoveryLoop:
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			break DiscoveryLoop
		case <-ticker.C:
			for id, resp := range d.Responses() {
				if err := serverData.UnmarshalBinary(resp); err == nil {
					networkID = id
					found = true
					break DiscoveryLoop
				}
			}
		}
	}

	if !found {
		return fmt.Errorf("no local Bedrock world found on LAN (make sure your world has Multiplayer & Visible to LAN enabled)")
	}

	log.Printf("bebot: found local world %q by %s (version %s, protocol %d, players %d/%d)\n",
		serverData.LevelName, serverData.ServerName, serverData.Version, serverData.Protocol, serverData.PlayerCount, serverData.MaxPlayerCount)

	botUUID := b.offlineUUID
	if botUUID == uuid.Nil {
		botUUID = bauth.LoadOrGenerateOfflineUUID(b.offlineUUIDPath, b.botName, false)
		b.offlineUUID = botUUID
	}
	b.selfUUID = botUUID
	name := b.botName
	if name == "" {
		name = "Bebot"
	}

	clientData := login.ClientData{
		DeviceOS: protocol.DeviceOrbis, DeviceModel: "playstation_5_emu", DeviceID: login.DeviceID(uuid.NewString()),
		LanguageCode: "en_US", GameVersion: serverData.Version, CurrentInputMode: packet.InputModeGamePad,
		DefaultInputMode: packet.InputModeGamePad, UIProfile: 0, MaxViewDistance: 16, MemoryTier: 3,
		PlatformType: 2, GraphicsMode: 1, TrustedSkin: true, CompatibleWithClientSideChunkGen: true,
		SelfSignedID: botUUID.String(), ArmSize: "slim", SkinID: "Standard_Alex",
		Nonce: serverData.Nonce,
	}
	if skin, err := bskin.LoadSkinFromConfig(b.cfg.Skin); err != nil {
		log.Println("bebot: skin disabled:", err)
	} else {
		bskin.FillClientDataSkin(&clientData, skin)
	}

	logger := slog.New(slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{Level: slog.LevelDebug}))
	dialer := minecraft.Dialer{
		ErrorLog: logger,
		ClientData: clientData,
		IdentityData: login.IdentityData{
			DisplayName: name,
			Identity:    botUUID.String(),
		},
		Protocol:                   netproto.NewServerProtocol(serverData.Protocol, serverData.Version),
		DisconnectOnUnknownPackets: false,
		DisconnectOnInvalidPackets: false,
		EnableClientCache:          false,
		PacketFunc:                 TracePacket,
		DownloadResourcePack: func(id uuid.UUID, version string, current, total int) bool {
			return false
		},
	}

	log.Println("bebot: stage: connecting via NetherNet LAN...")
	network := minecraft.NetherNet{
		Signaling: d,
		Dialer: nethernet.Dialer{
			AllowIdentitylessServer: true,
		},
	}

	dialCtx, cancelDial := context.WithTimeout(ctx, 30*time.Second)
	defer cancelDial()
	c, err := dialer.DialContextNetwork(dialCtx, network, strconv.FormatUint(networkID, 10))
	if err != nil {
		return fmt.Errorf("dial local: %s", ExplainError(err))
	}

	log.Println("bebot: stage: login complete; finishing loading screen...")
	b.mu.Lock()
	b.conn = c
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		connToClose := b.conn
		b.conn = nil
		b.mu.Unlock()
		if connToClose != nil {
			_ = connToClose.Close()
		}
		runtime.GC()
	}()

	if err := c.WritePacket(&packet.ServerBoundLoadingScreen{Type: packet.LoadingScreenTypeEnd}); err != nil {
		return fmt.Errorf("finish loading screen: %s", ExplainError(err))
	}
	if err := c.Flush(); err != nil {
		return fmt.Errorf("flush loading screen: %s", ExplainError(err))
	}

	log.Println("bebot: stage: waiting for world spawn...")
	spawnCtx, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()
	if err := c.DoSpawnContext(spawnCtx); err != nil {
		return fmt.Errorf("spawn: %s", ExplainError(err))
	}
	log.Println("bebot: stage: world spawn ok")

	renderDist := b.cfg.RenderDistance()
	_ = c.WritePacket(&packet.RequestChunkRadius{ChunkRadius: renderDist, MaxChunkRadius: uint8(renderDist)})
	b.markPacketReceived()

	b.pos = c.GameData().PlayerPosition
	b.last = b.pos
	b.entityID = c.GameData().EntityRuntimeID
	b.gameMode = c.GameData().PlayerGameMode
	b.tick = uint64(c.GameData().Time)
	b.serverCustomBlocks = c.GameData().CustomBlocks
	b.initAirRuntimeIDs(c.GameData().UseBlockNetworkIDHashes)
	if b.blockRegistry != nil {
		b.blockRegistry.InitFromGameData(c.GameData().UseBlockNetworkIDHashes, c.GameData().CustomBlocks, b.blockPalettePath)
	}
	b.players = make(map[string]player)
	b.playersByID = make(map[uint64]string)
	b.playersByUniqueID = make(map[int64]string)
	b.waypointGroups = make(map[uuid.UUID]int64)
	b.playerListReady = false
	b.playerEventsAfter = time.Now().Add(2 * time.Second)
	b.mode = modeIdle
	b.lookAtTarget = ""
	b.lookAtPos = nil
	b.mimicPosSet = false
	b.mimicSneaking = false
	b.mimicSwimming = false
	b.sentSneaking = false
	b.itemRequestID = -859
	b.stackPending = make(map[int32]chan uint8)
	b.containerOpen = make(chan struct{}, 1)
	b.containerContent = make(chan struct{}, 1)
	b.containerItems = make(map[byte]protocol.ItemInstance)
	b.containerTileEntities = make(map[protocol.BlockPos]map[string]any)
	b.dynamicContainerItems = make(map[uint32][]protocol.ItemInstance)
	b.containerRuntimeIDUpdated = make(chan uint32, 1)
	b.blockRuntimeIDs = make(map[protocol.BlockPos]uint32)
	b.mineActive = false
	b.mineFaceAccurate = false
	b.mineQueue = nil
	b.mineCurrent = nil
	b.mineStuckTicks = 0
	b.stopDropLoop()
	game := c.GameData()
	b.serverAuthoritativeInventory = game.ServerAuthoritativeInventory
	b.worldTime = game.Time
	b.startGameTime = game.Time
	b.itemNamesByID = make(map[int32]string)
	for _, it := range game.Items {
		b.itemNamesByID[int32(it.RuntimeID)] = it.Name
	}

	log.Printf("bebot: joined local world %q at %.1f %.1f %.1f entity=%d gamemode=%d\n",
		game.WorldName, b.pos.X(), b.pos.Y(), b.pos.Z(), b.entityID, game.PlayerGameMode)
	b.schedulePostJoinMemoryFree()

	done := make(chan error, 1)
	go func() { done <- b.readPackets(c) }()
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			b.logDisconnectDiagnostics(err)
			return fmt.Errorf("connection reader: %s", ExplainError(err))
		case <-t.C:
			b.moveTick(c, false)
		}
	}
}

func (b *Bot) connect(ctx context.Context) error {
	if b.local {
		return b.connectLocal(ctx)
	}
	address := fmt.Sprintf("%s:%d", b.cfg.Server.Host, b.cfg.Server.Port)
	log.Printf("bebot: pinging %s...\n", address)
	pong, transport, dialAddress, err := pingServer(ctx, address)
	if err != nil {
		return err
	}
	version := pong.Version
	if b.cfg.Server.Version != "" && b.cfg.Server.Version != pong.Version {
		log.Printf("bebot: warning: config version %s differs from server %s; using server version\n", b.cfg.Server.Version, pong.Version)
	}
	log.Printf("bebot: server=%s motd=%q protocol=%d version=%s transport=%s players=%d/%d\n", pong.Edition, pong.MOTD, pong.ProtocolID, pong.Version, transport, pong.Players, pong.MaxPlayers)

	offline := strings.EqualFold(strings.TrimSpace(pong.MOTD), "offline") || strings.Contains(strings.ToLower(pong.MOTD), "lobby")
	if offline {
		log.Println("bebot: server is offline (lobby); waiting for it to come online...")
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			pong, transport, dialAddress, err = pingServer(ctx, address)
			if err != nil {
				log.Printf("bebot:   ping failed: %v (retrying)\n", err)
				continue
			}
			offline = strings.EqualFold(strings.TrimSpace(pong.MOTD), "offline") || strings.Contains(strings.ToLower(pong.MOTD), "lobby")
			log.Printf("bebot:   still offline: motd=%q players=%d/%d\n", pong.MOTD, pong.Players, pong.MaxPlayers)
			if !offline {
				version = pong.Version
				log.Printf("bebot: server is now online: motd=%q version=%s players=%d/%d\n", pong.MOTD, pong.Version, pong.Players, pong.MaxPlayers)
				break
			}
		}
	}
	log.Printf("bebot: connecting to %s via %s using Minecraft %s...\n", dialAddress, transport, version)
	clientData := login.ClientData{
		DeviceOS: protocol.DeviceOrbis, DeviceModel: "playstation_5_emu", DeviceID: login.DeviceID(uuid.NewString()),
		LanguageCode: "en_US", GameVersion: version, CurrentInputMode: packet.InputModeGamePad,
		DefaultInputMode: packet.InputModeGamePad, UIProfile: 0, MaxViewDistance: 16, MemoryTier: 3,
		PlatformType: 2, GraphicsMode: 1, TrustedSkin: true, CompatibleWithClientSideChunkGen: true,
		SelfSignedID: uuid.NewString(), ArmSize: "slim", SkinID: "Standard_Alex",
	}
	if skin, err := bskin.LoadSkinFromConfig(b.cfg.Skin); err != nil {
		log.Println("bebot: skin disabled:", err)
	} else {
		bskin.FillClientDataSkin(&clientData, skin)
		log.Printf("bebot: loaded skin type=%s path=%s (%dx%d)\n", b.cfg.Skin.Type, b.cfg.Skin.Path, clientData.SkinImageWidth, clientData.SkinImageHeight)
	}

	var identityData login.IdentityData
	if b.offline || b.source == nil {
		botUUID := b.offlineUUID
		if botUUID == uuid.Nil {
			botUUID = bauth.LoadOrGenerateOfflineUUID(b.offlineUUIDPath, b.botName, false)
			b.offlineUUID = botUUID
		}
		b.selfUUID = botUUID
		name := b.botName
		if name == "" {
			name = "Bebot"
		}
		identityData = login.IdentityData{
			DisplayName: name,
			Identity:    botUUID.String(),
		}
		clientData.SelfSignedID = botUUID.String()
	}

	logger := slog.New(slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{Level: slog.LevelDebug}))
	d := minecraft.Dialer{
		ErrorLog:                   logger,
		TokenSource:                b.source,
		IdentityData:               identityData,
		ClientData:                 clientData,
		Protocol:                   netproto.NewServerProtocol(pong.ProtocolID, pong.Version),
		DisconnectOnUnknownPackets: false,
		DisconnectOnInvalidPackets: false,
		EnableClientCache:          false,
		PacketFunc:                 TracePacket,
		DownloadResourcePack: func(id uuid.UUID, version string, current, total int) bool {
			log.Printf("bebot: skipping resource pack %s version %s (%d/%d)\n", id, version, current, total)
			return false
		},
	}

	log.Printf("bebot: stage: %s handshake + login...\n", transport)
	dialCtx, cancelDial := context.WithTimeout(ctx, 5*time.Minute)
	if b.source != nil && b.xblCache != nil {
		dialCtx = mauth.WithXBLTokenCache(dialCtx, b.xblCache)
	}
	defer cancelDial()
	c, err := d.DialContext(dialCtx, transport, dialAddress)
	if err != nil {
		return fmt.Errorf("dial: %s", ExplainError(err))
	}
	if b.source != nil && b.xblCache != nil {
		bauth.SaveXBLCache(b.xstsCachePath, b.xblCache)
	}
	log.Println("bebot: stage: login complete; finishing loading screen...")
	b.mu.Lock()
	b.conn = c
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		connToClose := b.conn
		b.conn = nil
		b.mu.Unlock()
		if connToClose != nil {
			_ = connToClose.Close()
		}
		runtime.GC()
	}()

	if err := c.WritePacket(&packet.ServerBoundLoadingScreen{Type: packet.LoadingScreenTypeEnd}); err != nil {
		return fmt.Errorf("finish loading screen: %s", ExplainError(err))
	}
	if err := c.Flush(); err != nil {
		return fmt.Errorf("flush loading screen: %s", ExplainError(err))
	}

	log.Println("bebot: stage: waiting for world spawn...")
	spawnCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := c.DoSpawnContext(spawnCtx); err != nil {
		return fmt.Errorf("spawn: %s", ExplainError(err))
	}
	log.Println("bebot: stage: world spawn ok")

	renderDist := b.cfg.RenderDistance()
	_ = c.WritePacket(&packet.RequestChunkRadius{ChunkRadius: renderDist, MaxChunkRadius: uint8(renderDist)})
	b.markPacketReceived()
	log.Printf("bebot: requested updated chunk radius of %d chunks\n", renderDist)
	if tracePackets {
		log.Printf("bebot: server UseBlockNetworkIDHashes = %v (ServerBlockStateChecksum = %d)\n", c.GameData().UseBlockNetworkIDHashes, c.GameData().ServerBlockStateChecksum)
	}

	b.pos = c.GameData().PlayerPosition
	b.last = b.pos
	b.entityID = c.GameData().EntityRuntimeID
	if id, err := uuid.Parse(c.IdentityData().Identity); err == nil {
		b.selfUUID = id
	}
	b.gameMode = c.GameData().PlayerGameMode
	b.tick = uint64(c.GameData().Time)
	b.serverCustomBlocks = c.GameData().CustomBlocks
	b.initAirRuntimeIDs(c.GameData().UseBlockNetworkIDHashes)
	if b.blockRegistry != nil {
		b.blockRegistry.InitFromGameData(c.GameData().UseBlockNetworkIDHashes, c.GameData().CustomBlocks, b.blockPalettePath)
	}
	b.players = make(map[string]player)
	b.playersByID = make(map[uint64]string)
	b.playersByUniqueID = make(map[int64]string)
	b.waypointGroups = make(map[uuid.UUID]int64)
	b.playerListReady = false
	b.playerEventsAfter = time.Now().Add(2 * time.Second)
	b.mode = modeIdle
	b.lookAtTarget = ""
	b.lookAtPos = nil
	b.mimicPosSet = false
	b.mimicSneaking = false
	b.mimicSwimming = false
	b.sentSneaking = false
	b.itemRequestID = -859
	b.stackPending = make(map[int32]chan uint8)
	b.containerOpen = make(chan struct{}, 1)
	b.containerContent = make(chan struct{}, 1)
	b.containerItems = make(map[byte]protocol.ItemInstance)
	b.containerTileEntities = make(map[protocol.BlockPos]map[string]any)
	b.dynamicContainerItems = make(map[uint32][]protocol.ItemInstance)
	b.containerRuntimeIDUpdated = make(chan uint32, 1)
	b.blockRuntimeIDs = make(map[protocol.BlockPos]uint32)
	b.mineActive = false
	b.mineFaceAccurate = false
	b.mineQueue = nil
	b.mineCurrent = nil
	b.mineStuckTicks = 0
	b.stopDropLoop()
	game := c.GameData()
	b.serverAuthoritativeInventory = game.ServerAuthoritativeInventory
	b.worldTime = game.Time
	b.startGameTime = game.Time
	b.itemNamesByID = make(map[int32]string)
	for _, it := range game.Items {
		b.itemNamesByID[int32(it.RuntimeID)] = it.Name
	}
	var daylightCycle bool
	for _, gr := range game.GameRules {
		if strings.EqualFold(gr.Name, "dodaylightcycle") {
			if v, ok := gr.Value.(bool); ok {
				daylightCycle = v
			}
		}
	}
	log.Printf("bebot: world time: startGameTime=%d dayCycleLockTime=%d doDaylightCycle=%v\n", game.Time, game.DayCycleLockTime, daylightCycle)
	log.Printf("bebot: joined %s at %.1f %.1f %.1f entity=%d gamemode=%d dimension=%d seed=%d\n", address, b.pos.X(), b.pos.Y(), b.pos.Z(), b.entityID, game.PlayerGameMode, game.Dimension, game.WorldSeed)
	b.schedulePostJoinMemoryFree()
	log.Printf("bebot: movement settings rewind=%d server-authoritative-block-breaking=%v server-authoritative-inventory=%v interactions-disabled=%v chunk-radius=%d\n", game.PlayerMovementSettings.RewindHistorySize, game.PlayerMovementSettings.ServerAuthoritativeBlockBreaking, game.ServerAuthoritativeInventory, game.DisablePlayerInteractions, game.ChunkRadius)
	done := make(chan error, 1)
	go func() { done <- b.readPackets(c) }()
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			b.logDisconnectDiagnostics(err)
			return fmt.Errorf("connection reader: %s", ExplainError(err))
		case <-t.C:
			b.moveTick(c, false)
		}
	}
}

// schedulePostJoinMemoryFree waits for initial chunk/spawn allocations to settle, then trims OS memory.
func (b *Bot) schedulePostJoinMemoryFree() {
	if config.FreeMemAfterJoinOn(b.cfg) {
		go func() {
			time.Sleep(4 * time.Second)
			b.mu.Lock()
			conn := b.conn
			b.mu.Unlock()
			if conn == nil {
				return
			}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			debug.FreeOSMemory()
			runtime.ReadMemStats(&after)
			log.Printf("bebot: post-join memory trim completed (Alloc: %.2f MB -> %.2f MB, Sys: %.2f MB -> %.2f MB)\n",
				float64(before.Alloc)/(1024*1024), float64(after.Alloc)/(1024*1024),
				float64(before.Sys)/(1024*1024), float64(after.Sys)/(1024*1024),
			)
		}()
	}
}

func (b *Bot) readPackets(c *minecraft.Conn) error {
	for {
		pk, err := c.ReadPacket()
		if err != nil {
			return err
		}
		b.markPacketReceivedDesc(fmt.Sprintf("%T", pk))
		switch p := pk.(type) {
		case *packet.Text:
			switch p.TextType {
			case packet.TextTypeChat:
				if from, msg, ok := parseWhisper(p.Message); ok {
					b.handleWhisper(from, msg)
					break
				}
				fmt.Printf("[chat - %s] <%s> %s\n", time.Now().Format("2006-01-02 15:04:05"), cleanChatName(p.SourceName), p.Message)
				b.commandFromChat(p.SourceName, p.Message)
			case packet.TextTypeWhisper:
				b.handleWhisper(p.SourceName, p.Message)
			case packet.TextTypeTranslation, packet.TextTypeTip, packet.TextTypeSystem, packet.TextTypePopup:
				if strings.Contains(p.Message, "tile.bed") {
					log.Printf("bebot: bed message: %s params=%v\n", p.Message, p.Parameters)
					if bedMsg := formatBedTranslation(p.Message); bedMsg != "" {
						b.reply(bedMsg)
					}
				}
			}
		case *packet.Transfer:
			return fmt.Errorf("server requested transfer to %s:%d; reconnecting to configured target", p.Address, p.Port)
		case *packet.Disconnect:
			b.mu.Lock()
			b.lastDisconnectMsg = p.Message
			b.mu.Unlock()
			log.Printf("bebot: server disconnected: %q\n", p.Message)
		case *packet.LevelChunk:
			if b.chunkCache != nil {
				b.chunkCache.OnLevelChunk(p)
			}
		case *packet.SubChunk:
			if b.chunkCache != nil {
				b.chunkCache.OnSubChunk(p)
			}
		case *packet.UpdateBlock:
			b.handleUpdateBlock(p.Position, p.NewBlockRuntimeID)
			if b.chunkCache != nil {
				b.chunkCache.OnBlockUpdate(p.Position, p.NewBlockRuntimeID)
			}
		case *packet.UpdateBlockSynced:
			b.handleUpdateBlock(p.Position, p.NewBlockRuntimeID)
			if b.chunkCache != nil {
				b.chunkCache.OnBlockUpdate(p.Position, p.NewBlockRuntimeID)
			}
		case *packet.UpdateSubChunkBlocks:
			for _, entry := range p.Blocks {
				pos := b.resolveSubChunkBlockPos(p.Position, entry.BlockPos)
				b.handleUpdateBlock(pos, entry.BlockRuntimeID)
			}
			for _, entry := range p.Extra {
				pos := b.resolveSubChunkBlockPos(p.Position, entry.BlockPos)
				b.handleUpdateBlock(pos, entry.BlockRuntimeID)
			}
			if b.chunkCache != nil {
				b.chunkCache.OnSubChunkBlocks(p.Position, p.Blocks)
				b.chunkCache.OnSubChunkBlocks(p.Position, p.Extra)
			}
		case *packet.BlockActorData:
			if tracePackets {
				log.Printf("bebot: BlockActorData pos=%v id=%v\n", p.Position, p.NBTData["id"])
			}
			b.mu.Lock()
			if b.containerTileEntities == nil {
				b.containerTileEntities = make(map[protocol.BlockPos]map[string]any)
			}
			b.containerTileEntities[p.Position] = p.NBTData
			b.mu.Unlock()
		case *packet.BlockEvent:
			if tracePackets {
				log.Printf("bebot: BlockEvent pos=%v type=%d data=%d\n", p.Position, p.EventType, p.EventData)
			}
		case *packet.CommandOutput:
			msgs := make([]string, 0, len(p.OutputMessages))
			var capKey string
			var capParams []string
			for _, m := range p.OutputMessages {
				if capKey == "" && m.Message != "" {
					capKey = m.Message
					capParams = m.Parameters
				}
				text := m.Message
				if len(m.Parameters) > 0 {
					text += " " + strings.Join(m.Parameters, " ")
				}
				msgs = append(msgs, text)
			}
			log.Printf("bebot: command output (success=%d): %s\n", p.SuccessCount, strings.Join(msgs, " | "))
			b.mu.Lock()
			cap := b.capture
			b.capture = nil
			b.mu.Unlock()
			if cap != nil {
				select {
				case cap <- commandResult{key: capKey, params: capParams, text: strings.Join(msgs, " ")}:
				default:
				}
			}
		case *packet.SetTime:
			b.mu.Lock()
			b.worldTime = int64(p.Time)
			b.mu.Unlock()
		case *packet.AddPlayer:
			b.mu.Lock()
			name := strings.ToLower(p.Username)
			v := b.players[name]
			v.name, v.id, v.online = p.Username, p.EntityRuntimeID, true
			v.accountUUID = p.UUID
			v.position, v.yaw, v.pitch = p.Position, p.Yaw, p.Pitch
			v.hasLocation = true
			v.sneaking, v.sprinting, v.swimming, v.usingItem = playerMetadataState(p.EntityMetadata)
			b.players[name] = v
			b.playersByID[p.EntityRuntimeID] = name
			b.mu.Unlock()
		case *packet.MovePlayer:
			b.mu.Lock()
			if p.EntityRuntimeID == b.entityID || p.EntityRuntimeID == 0 {
				b.pos = p.Position
				b.last = p.Position
				b.velocityY = 0
				if b.sentSneaking {
					log.Printf("bebot: own MovePlayer tick=%d -> (%.2f, %.2f, %.2f) mode=%d\n", p.Tick, p.Position.X(), p.Position.Y(), p.Position.Z(), p.Mode)
				}
			}
			if name, ok := b.playersByID[p.EntityRuntimeID]; ok {
				v := b.players[name]
				oldY := v.position.Y()
				v.position, v.yaw, v.pitch = p.Position, p.Yaw, p.Pitch
				v.jumping = p.Position.Y()-oldY > .25
				v.hasLocation = true
				b.players[name] = v
			}
			b.mu.Unlock()
			b.updateChunkCenterIfMoved()
		case *packet.MoveActorAbsolute:
			b.mu.Lock()
			if name, ok := b.playersByID[p.EntityRuntimeID]; ok {
				v := b.players[name]
				v.position = p.Position
				v.pitch, v.yaw = p.Rotation[0], p.Rotation[1]
				v.hasLocation = true
				b.players[name] = v
			}
			b.mu.Unlock()
		case *packet.MoveActorDelta:
			b.mu.Lock()
			if name, ok := b.playersByID[p.EntityRuntimeID]; ok {
				v := b.players[name]
				pos := v.position
				if val, ok := p.PositionX.Value(); ok {
					pos[0] = val
				}
				if val, ok := p.PositionY.Value(); ok {
					pos[1] = val
				}
				if val, ok := p.PositionZ.Value(); ok {
					pos[2] = val
				}
				v.position = pos
				if val, ok := p.RotationY.Value(); ok {
					v.yaw = val
				}
				if val, ok := p.RotationX.Value(); ok {
					v.pitch = val
				}
				v.hasLocation = true
				b.players[name] = v
			}
			b.mu.Unlock()
		case *packet.PlayerLocation:
			b.mu.Lock()
			matchedName := ""
			if name, ok := b.playersByUniqueID[p.EntityUniqueID]; ok {
				matchedName = name
			} else {
				for name, v := range b.players {
					if v.uniqueID == p.EntityUniqueID || (v.id != 0 && uint64(p.EntityUniqueID) == v.id) {
						matchedName = name
						break
					}
				}
			}
			if matchedName != "" {
				v := b.players[matchedName]
				if p.Type == packet.PlayerLocationTypeCoordinates {
					v.position = p.Position
					v.hasLocation = true
					b.players[matchedName] = v
					log.Printf("bebot: PlayerLocation updated %s -> pos=(%.1f, %.1f, %.1f)\n",
						matchedName, p.Position.X(), p.Position.Y(), p.Position.Z())
				}
			}
			b.mu.Unlock()
		case *packet.LocatorBar:
			b.mu.Lock()
			for _, wpEntry := range p.Waypoints {
				wp := wpEntry.Waypoint
				uniqueID, hasActorID := wp.ActorUniqueID.Value()
				posInfo, hasPos := wp.WorldPosition.Value()

				// If ActorUniqueID is present, map GroupHandle to ActorUniqueID
				if hasActorID && uniqueID != 0 {
					b.waypointGroups[wpEntry.GroupHandle] = uniqueID
				}

				// If Action is remove, clean up waypoint group mapping
				if wpEntry.Action == protocol.WaypointActionRemove {
					delete(b.waypointGroups, wpEntry.GroupHandle)
					continue
				}

				// Position is required to update location
				if !hasPos {
					continue
				}

				// Resolve target UniqueID from explicit ActorUniqueID or cached GroupHandle mapping
				targetUniqueID := uniqueID
				if targetUniqueID == 0 && wpEntry.GroupHandle != uuid.Nil {
					if id, ok := b.waypointGroups[wpEntry.GroupHandle]; ok {
						targetUniqueID = id
					}
				}

				matchedName := ""
				if targetUniqueID != 0 {
					if name, ok := b.playersByUniqueID[targetUniqueID]; ok {
						matchedName = name
					} else {
						for name, v := range b.players {
							if v.uniqueID == targetUniqueID || (v.id != 0 && uint64(targetUniqueID) == v.id) {
								matchedName = name
								break
							}
						}
					}
				}

				// Fallback: check if GroupHandle matches any player's accountUUID
				if matchedName == "" && wpEntry.GroupHandle != uuid.Nil {
					for name, v := range b.players {
						if v.accountUUID == wpEntry.GroupHandle {
							matchedName = name
							break
						}
					}
				}

				if matchedName != "" {
					v := b.players[matchedName]
					v.position = posInfo.Position
					v.hasLocation = true
					b.players[matchedName] = v
					log.Printf("bebot: LocatorBar updated %s -> pos=(%.1f, %.1f, %.1f) action=%d group=%s\n",
						matchedName, posInfo.Position.X(), posInfo.Position.Y(), posInfo.Position.Z(), wpEntry.Action, wpEntry.GroupHandle)
				} else {
					log.Printf("bebot: LocatorBar received action=%d group=%s uniqueID=%d pos=(%.1f, %.1f, %.1f) (no matched player)\n",
						wpEntry.Action, wpEntry.GroupHandle, targetUniqueID, posInfo.Position.X(), posInfo.Position.Y(), posInfo.Position.Z())
				}
			}
			b.mu.Unlock()
		case *packet.CorrectPlayerMovePrediction:
			if p.PredictionType == packet.PredictionTypePlayer {
				b.mu.Lock()
				b.pos = p.Position
				b.last = p.Position
				b.mu.Unlock()
			}
		case *packet.SetHealth:
			log.Printf("bebot: health update=%d\n", p.Health)
			b.mu.Lock()
			b.lastHealth = p.Health
			b.mu.Unlock()
		case *packet.MobEffect:
			if p.EntityRuntimeID == b.entityID {
				b.mu.Lock()
				if p.EffectType == packet.EffectHaste || p.EffectType == packet.EffectConduitPower {
					if p.Operation == packet.MobEffectAdd || p.Operation == packet.MobEffectModify {
						lvl := p.Amplifier + 1
						if lvl != b.hasteLevel {
							b.hasteLevel = lvl
							log.Printf("bebot: haste effect active level=%d (remaining: %ds)\n", b.hasteLevel, p.Duration/20)
						}
					} else if p.Operation == packet.MobEffectRemove {
						if b.hasteLevel != 0 {
							b.hasteLevel = 0
							log.Printf("bebot: haste effect removed\n")
						}
					}
				}
				b.mu.Unlock()
			}
		case *packet.HurtArmour:
			log.Printf("bebot: armour damage cause=%d damage=%d slots=0x%x\n", p.Cause, p.Damage, p.ArmourSlots)
		case *packet.SetPlayerGameType:
			b.mu.Lock()
			b.gameMode = p.GameType
			b.mu.Unlock()
			log.Printf("bebot: received SetPlayerGameType: gameMode=%d\n", p.GameType)
		case *packet.Respawn:
			if p.State == packet.RespawnStateReadyToSpawn {
				log.Printf("bebot: respawn state=%d position=(%.2f, %.2f, %.2f)\n", p.State, p.Position.X(), p.Position.Y(), p.Position.Z())
			}
			b.handleRespawn(c, p)
		case *packet.PlayerList:
			b.mu.Lock()
			for _, entry := range p.Entries {
				name := strings.ToLower(entry.Username)
				if entry.ActionType == protocol.PlayerListActionAdd && name != "" {
					v := b.players[name]
					wasOnline := v.online
					v.name, v.online, v.accountUUID = entry.Username, true, entry.UUID
					v.uniqueID = entry.EntityUniqueID
					b.players[name] = v
					if entry.EntityUniqueID != 0 {
						b.playersByUniqueID[entry.EntityUniqueID] = name
					}
					if b.playerListReady && !wasOnline && time.Now().After(b.playerEventsAfter) {
						b.logPlayerEvent("joined", entry.Username)
					}
				} else if entry.ActionType == protocol.PlayerListActionRemove {
					for key, v := range b.players {
						if v.accountUUID == entry.UUID {
							if v.id != 0 {
								delete(b.playersByID, v.id)
							}
							if v.uniqueID != 0 {
								delete(b.playersByUniqueID, v.uniqueID)
							}
							delete(b.players, key)
							if v.name != "" {
								b.logPlayerEvent("left", v.name)
							}
						}
					}
				}
			}
			b.playerListReady = true
			b.mu.Unlock()
		case *packet.AddActor:
			b.mu.Lock()
			b.entities[p.EntityUniqueID] = p.EntityType
			if p.EntityType == "minecraft:fishing_hook" {
				if ownerID, ok := p.EntityMetadata[protocol.EntityDataKeyOwner]; ok {
					if id, ok := ownerID.(int64); ok && uint64(id) == b.entityID {
						b.fishingHookID = p.EntityRuntimeID
						if b.autoFish {
							log.Printf("bebot: registered fishing hook %d\n", b.fishingHookID)
						}
					}
				}
			}
			b.mu.Unlock()
		case *packet.RemoveActor:
			b.mu.Lock()
			removedID := uint64(p.EntityUniqueID)
			if _, ok := b.playersByID[removedID]; ok {
				delete(b.playersByID, removedID)
			} else {
				for _, v := range b.players {
					if v.uniqueID == p.EntityUniqueID && v.id != 0 {
						delete(b.playersByID, v.id)
					}
				}
			}
			delete(b.entities, p.EntityUniqueID)
			if uint64(p.EntityUniqueID) == b.fishingHookID {
				b.fishingHookID = 0
			}
			b.mu.Unlock()
		case *packet.ActorEvent:
			if p.EventType == packet.ActorEventFishhookTease || p.EventType == packet.ActorEventFishhookBubble {
				b.mu.Lock()
				isOurHook := p.EntityRuntimeID == b.fishingHookID
				autoFishOn := b.autoFish
				b.mu.Unlock()
				if isOurHook && autoFishOn {
					log.Println("bebot: fish bit! reeling in...")
					b.useItemInHand()

					go func() {
						time.Sleep(1 * time.Second)
						b.mu.Lock()
						autoFishOn := b.autoFish
						b.mu.Unlock()
						if autoFishOn {
							log.Println("bebot: autofish recasting...")
							b.useItemInHand()
						}
					}()
				}
			} else if p.EventType == packet.ActorEventHurt {
				b.mu.Lock()
				isUs := p.EntityRuntimeID == b.entityID
				paybackOn := b.payback
				b.mu.Unlock()
				if isUs && paybackOn {
					targetID, targetPos, found := b.nearestAttacker(4.5)
					if found {
						log.Printf("bebot: payback! attacking entity %d at %v\n", targetID, targetPos)
						b.attack(targetID, targetPos)
					}
				}
			} else if p.EventType == packet.ActorEventDeath {
				b.mu.Lock()
				if name, ok := b.playersByID[p.EntityRuntimeID]; ok {
					if b.killTarget == name {
						b.killTarget = ""
						log.Printf("bebot: target %s died! stopping kill mode\n", name)
					}
				}
				b.mu.Unlock()
			}
		case *packet.Animate:
			if name, ok := b.playersByID[p.EntityRuntimeID]; ok && p.ActionType == packet.AnimateActionSwingArm {
				b.mu.Lock()
				v := b.players[name]
				v.jumping = false
				v.swinging = true
				v.usingItem = p.SwingSource == packet.AnimateSwingSourceUseItem || p.SwingSource == packet.AnimateSwingSourceInteract
				v.lastSwing = time.Now()
				b.players[name] = v
				b.mu.Unlock()
			}
		case *packet.SetActorData:
			if name, ok := b.playersByID[p.EntityRuntimeID]; ok {
				sneaking, sprinting, swimming, using := playerMetadataState(p.EntityMetadata)
				_, hasFlags := p.EntityMetadata[protocol.EntityDataKeyFlags]
				pose, hasPose := entityPose(p.EntityMetadata)
				b.mu.Lock()
				v := b.players[name]
				if hasFlags || (hasPose && (pose == 4 || pose == 2)) {
					v.sneaking, v.sprinting, v.swimming = sneaking, sprinting, swimming
					v.usingItem = using
				}
				b.players[name] = v
				b.mu.Unlock()
			}
		case *packet.PlayerAction:
			if name, ok := b.playersByID[p.EntityRuntimeID]; ok {
				b.mu.Lock()
				v := b.players[name]
				switch p.ActionType {
				case protocol.PlayerActionStartSneak:
					v.sneaking = true
				case protocol.PlayerActionStopSneak:
					v.sneaking = false
				case protocol.PlayerActionStartSwimming:
					v.swimming = true
				case protocol.PlayerActionStopSwimming:
					v.swimming = false
				}
				b.players[name] = v
				b.mu.Unlock()
			}
		case *packet.MobEquipment:
			if p.EntityRuntimeID == b.entityID {
				b.hotbarSlot = p.HotBarSlot
				b.heldItem = p.NewItem
			}
		case *packet.ContainerOpen:
			log.Printf("bebot: ContainerOpen WindowID=%d ContainerType=%d Pos=%v\n", p.WindowID, p.ContainerType, p.ContainerPosition)
			b.mu.Lock()
			b.containerActive = true
			b.containerWindowID = p.WindowID
			b.containerType = p.ContainerType
			b.containerPos = p.ContainerPosition
			b.containerItems = make(map[byte]protocol.ItemInstance)
			b.containerContainerID = protocol.ContainerLevelEntity
			b.containerContentReceived = false
			b.mu.Unlock()
			select {
			case b.containerOpen <- struct{}{}:
			default:
			}
		case *packet.ContainerClose:
			log.Printf("bebot: ContainerClose WindowID=%d ServerSide=%v\n", p.WindowID, p.ServerSide)
			b.mu.Lock()
			if b.containerActive && (p.WindowID == b.containerWindowID || p.WindowID == 0) {
				b.containerActive = false
			}
			b.mu.Unlock()
		case *packet.PacketViolationWarning:
			log.Printf("bebot: PacketViolationWarning Type=%d Severity=%d PacketID=%d Context=%q\n",
				p.Type, p.Severity, p.PacketID, p.ViolationContext)
		case *packet.ItemStackResponse:
			for _, response := range p.Responses {
				if response.Status == protocol.ItemStackResponseStatusOK {
					b.mu.Lock()
					for _, cInfo := range response.ContainerInfo {
						for _, sInfo := range cInfo.SlotInfo {
							slotIdx := -1
							switch cInfo.Container.ContainerID {
							case protocol.ContainerHotBar:
								slotIdx = int(sInfo.Slot)
							case protocol.ContainerInventory:
								slotIdx = int(sInfo.Slot) + 9
							case protocol.ContainerCombinedHotBarAndInventory:
								slotIdx = int(sInfo.Slot)
							}
							if slotIdx >= 0 && slotIdx < len(b.inventory) {
								b.inventory[slotIdx].StackNetworkID = sInfo.StackNetworkID
								if sInfo.Count == 0 {
									b.inventory[slotIdx] = protocol.ItemInstance{}
								} else {
									b.inventory[slotIdx].Stack.Count = uint16(sInfo.Count)
								}
								log.Printf("bebot: inventory slot %d updated from ItemStackResponse: count=%d stackNetID=%d\n", slotIdx, sInfo.Count, sInfo.StackNetworkID)
							}
						}
					}
					b.mu.Unlock()
				}
			}
			for _, response := range p.Responses {
				b.mu.Lock()
				ch, ok := b.stackPending[response.RequestID]
				if ok {
					delete(b.stackPending, response.RequestID)
				}
				b.mu.Unlock()
				if response.Status != protocol.ItemStackResponseStatusOK {
					log.Printf("bebot: inventory request=%d rejected status=%d\n", response.RequestID, response.Status)
				}
				if ok && ch != nil {
					select {
					case ch <- response.Status:
					default:
					}
				}
			}
		case *packet.InventoryContent:
			dynID := uint32(0)
			if d, ok := p.Container.DynamicContainerID.Value(); ok && d != 0 {
				dynID = d
				b.mu.Lock()
				if b.dynamicContainerItems == nil {
					b.dynamicContainerItems = make(map[uint32][]protocol.ItemInstance)
				}
				b.dynamicContainerItems[dynID] = append([]protocol.ItemInstance(nil), p.Content...)
				b.mu.Unlock()
				log.Printf("bebot: received dynamic InventoryContent dynID=%d items=%d ContainerID=%d\n", dynID, len(p.Content), p.Container.ContainerID)
			}
			if tracePackets {
				log.Printf("bebot: trace: InventoryContent WindowID=%d ContainerID=%d DynamicID=%d items=%d\n", p.WindowID, p.Container.ContainerID, dynID, len(p.Content))
			}
			if p.WindowID == 0 {
				b.mu.Lock()
				b.inventory = append([]protocol.ItemInstance(nil), p.Content...)
				if int(b.hotbarSlot) < len(b.inventory) {
					b.heldItem = b.inventory[b.hotbarSlot]
				}
				b.mu.Unlock()
				b.sendHeldItem(c)
			} else if p.WindowID < 119 {
				b.mu.Lock()
				if b.containerActive || b.containerOpening {
					b.containerActive = true
					b.containerWindowID = byte(p.WindowID)
					if b.containerItems == nil {
						b.containerItems = make(map[byte]protocol.ItemInstance)
					}
					for slot, item := range p.Content {
						b.containerItems[byte(slot)] = item
					}
					if p.Container.ContainerID != 0 {
						b.containerContainerID = p.Container.ContainerID
					}
					b.containerContentReceived = true
				}
				b.mu.Unlock()
				select {
				case b.containerOpen <- struct{}{}:
				default:
				}
				select {
				case b.containerContent <- struct{}{}:
				default:
				}
			}
		case *packet.InventorySlot:
			if tracePackets {
				log.Printf("bebot: trace: InventorySlot WindowID=%d Slot=%d Item=%v\n", p.WindowID, p.Slot, p.NewItem.Stack.ItemType.NetworkID)
			}
			if p.WindowID == 0 {
				b.mu.Lock()
				if int(p.Slot) >= len(b.inventory) {
					grown := make([]protocol.ItemInstance, int(p.Slot)+1)
					copy(grown, b.inventory)
					b.inventory = grown
				}
				b.inventory[p.Slot] = p.NewItem
				isHotbar := byte(p.Slot) == b.hotbarSlot
				if isHotbar {
					b.heldItem = p.NewItem
				}
				b.mu.Unlock()
				if isHotbar {
					b.sendHeldItem(c)
				}
			} else if p.WindowID < 119 {
				b.mu.Lock()
				if (b.containerActive || b.containerOpening) && (byte(p.WindowID) == b.containerWindowID || b.containerWindowID == 0) {
					b.containerActive = true
					b.containerWindowID = byte(p.WindowID)
					if b.containerItems == nil {
						b.containerItems = make(map[byte]protocol.ItemInstance)
					}
					b.containerItems[byte(p.Slot)] = p.NewItem
					if container, ok := p.Container.Value(); ok {
						if container.ContainerID != 0 {
							b.containerContainerID = container.ContainerID
						}
						if dyn, hasDyn := container.DynamicContainerID.Value(); hasDyn && dyn != 0 {
							log.Printf("bebot: received dynamic InventorySlot dynID=%d slot=%d item=%v netID=%d\n", dyn, p.Slot, p.NewItem.Stack.ItemType.NetworkID, p.NewItem.StackNetworkID)
						}
					}
				}
				b.mu.Unlock()
			}
		case *packet.PlayerHotBar:
			if p.SelectHotBarSlot && p.SelectedHotBarSlot < 10 {
				b.mu.Lock()
				b.hotbarSlot = byte(p.SelectedHotBarSlot)
				if int(b.hotbarSlot) < len(b.inventory) {
					b.heldItem = b.inventory[b.hotbarSlot]
				}
				b.mu.Unlock()
				b.sendHeldItem(c)
			}
		case *packet.InventoryTransaction:
			if _, ok := p.TransactionData.(*protocol.NormalTransactionData); ok {
				b.mu.Lock()
				heldChanged := false
				for _, action := range p.Actions {
					if action.SourceType == protocol.InventoryActionSourceContainer {
						if windowID, ok := action.WindowID.Value(); ok && windowID == protocol.WindowIDInventory {
							if int(action.InventorySlot) >= len(b.inventory) {
								grown := make([]protocol.ItemInstance, int(action.InventorySlot)+1)
								copy(grown, b.inventory)
								b.inventory = grown
							}
							b.inventory[action.InventorySlot] = action.NewItem
							if byte(action.InventorySlot) == b.hotbarSlot {
								b.heldItem = action.NewItem
								heldChanged = true
							}
						}
					}
				}
				b.mu.Unlock()
				if heldChanged {
					b.sendHeldItem(c)
				}
			}
		}
	}
}

func (b *Bot) handleRespawn(c *minecraft.Conn, p *packet.Respawn) {
	entityID := p.EntityRuntimeID
	if entityID == 0 {
		b.mu.Lock()
		entityID = b.entityID
		b.mu.Unlock()
	}
	switch p.State {
	case packet.RespawnStateSearchingForSpawn:
		b.mu.Lock()
		b.pos = p.Position
		b.last = p.Position
		b.velocityY = 0
		b.stuckTicks = 0
		b.entityID = entityID
		b.mu.Unlock()
		if err := c.WritePacket(&packet.Respawn{
			Position: p.Position, State: packet.RespawnStateClientReadyToSpawn, EntityRuntimeID: entityID,
		}); err != nil {
			log.Println("bebot: respawn response failed:", ExplainError(err))
		}
	case packet.RespawnStateReadyToSpawn:
		if err := c.WritePacket(&packet.PlayerAction{
			EntityRuntimeID: entityID,
			ActionType:      protocol.PlayerActionRespawn,
			BlockFace:       -1,
		}); err != nil {
			log.Println("bebot: respawn action failed:", ExplainError(err))
		}
	}
}

func (b *Bot) scheduleReconnect() {
	go func() {
		time.Sleep(500 * time.Millisecond)
		b.mu.Lock()
		b.reconnectDelay = config.BaseReconnectDelay(b.cfg)
		c := b.conn
		b.mu.Unlock()
		if c != nil {
			_ = c.Close()
		}
	}()
}

func formatBedTranslation(msg string) string {
	switch {
	case strings.Contains(msg, "notSafe"):
		return "You may not rest now; there are monsters nearby."
	case strings.Contains(msg, "notValid"):
		return "Your home bed was missing or obstructed."
	case strings.Contains(msg, "occupied"):
		return "This bed is occupied."
	case strings.Contains(msg, "noSleep"):
		return "You can sleep only at night and during thunderstorms."
	case strings.Contains(msg, "tooFar"):
		return "The bed is too far away."
	case strings.Contains(msg, "respawnSet"):
		return "Respawn point set."
	case strings.Contains(msg, "obstructed"):
		return "This bed is obstructed."
	default:
		return ""
	}
}
