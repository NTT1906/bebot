package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/google/uuid"
	"github.com/sandertv/go-raknet"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/auth"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
	"golang.org/x/oauth2"
)

type config struct {
	Superuser string `json:"superuser"`
	Server    struct {
		Host    string `json:"host"`
		Port    int    `json:"port"`
		Version string `json:"version"`
	} `json:"server"`
	Target    struct{ X, Y, Z float32 } `json:"target"`
	Behaviour struct {
		ReconnectBaseDelayMs, AfkJumpIntervalMs int
		WalkSpeed                               float32
	} `json:"behaviour"`
	CommandsViaChat *bool `json:"commands_via_chat"`
}

var tracePackets bool
var tracedPackets atomic.Uint64

func main() {
	configPath := flag.String("config", "config.json", "bot configuration file")
	authCachePath := flag.String("auth-cache", "token_cache.json", "path used to persist the Microsoft session")
	debugFlag := flag.Bool("debug", false, "print packet-level handshake diagnostics")
	flag.Parse()
	tracePackets = *debugFlag
	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Println("bebot:", err)
		return
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 19132
	}
	if cfg.Behaviour.WalkSpeed <= 0 {
		cfg.Behaviour.WalkSpeed = .15
	}
	if cfg.Behaviour.ReconnectBaseDelayMs <= 0 {
		cfg.Behaviour.ReconnectBaseDelayMs = 5000
	}
	if cfg.CommandsViaChat == nil {
		t := false
		cfg.CommandsViaChat = &t
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	source, err := loadAuthSource(*authCachePath)
	if err != nil {
		fmt.Println("bebot: authentication failed:", err)
		return
	}
	bot := &Bot{cfg: cfg, source: source, players: make(map[string]player), mode: modeIdle, waypoints: loadWaypoints(waypointsFile)}
	defer func() {
		if recovered := recover(); recovered != nil {
			fmt.Printf("bebot: FATAL panic: %v\n%s\n", recovered, debug.Stack())
		}
	}()
	go bot.run(ctx)

	// Console commands are shared with the old Node.js bot. Lines starting
	// with "/" are forwarded to the server as Minecraft commands.
	go func() {
		s := bufio.NewScanner(os.Stdin)
		for s.Scan() {
			line := strings.TrimSpace(s.Text())
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "/") {
				bot.runCommand(line)
				continue
			}
			bot.consoleCommand(line)
		}
	}()
	<-ctx.Done()
	bot.close()
}

// loadAuthSource keeps the OAuth refresh token on disk. The access token is
// short-lived, but RefreshTokenSource renews it automatically using the saved
// refresh token, so device authentication is only needed once.
func loadAuthSource(path string) (oauth2.TokenSource, error) {
	path = filepath.Clean(path)
	if data, err := os.ReadFile(path); err == nil {
		var token oauth2.Token
		if json.Unmarshal(data, &token) == nil && token.RefreshToken != "" {
			source := &persistedTokenSource{source: auth.RefreshTokenSource(&token), path: path}
			if _, err := source.Token(); err == nil {
				fmt.Println("bebot: restored saved Microsoft session from", path)
				return source, nil
			}
			fmt.Println("bebot: saved Microsoft session expired; signing in again")
		}
	}
	fmt.Println("bebot: no usable saved Microsoft session; device authentication is required")
	token, err := auth.RequestLiveToken()
	if err != nil {
		return nil, err
	}
	source := &persistedTokenSource{source: auth.RefreshTokenSource(token), path: path}
	if err := source.save(token); err != nil {
		return nil, err
	}
	fmt.Println("bebot: Microsoft session saved to", path)
	return source, nil
}

type persistedTokenSource struct {
	source oauth2.TokenSource
	path   string
	mu     sync.Mutex
}

func (s *persistedTokenSource) Token() (*oauth2.Token, error) {
	token, err := s.source.Token()
	if err != nil {
		return nil, err
	}
	if err := s.save(token); err != nil {
		fmt.Println("bebot: warning: could not save refreshed session:", err)
	}
	return token, nil
}
func (s *persistedTokenSource) save(token *oauth2.Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

func loadConfig(path string) (config, error) {
	var cfg config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Server.Host == "" {
		return cfg, fmt.Errorf("server.host is required")
	}
	return cfg, nil
}

func tracePacket(header packet.Header, payload []byte, src, dst net.Addr) {
	if !tracePackets {
		return
	}
	n := tracedPackets.Add(1)
	if n > 200 && header.PacketID != packet.IDDisconnect && header.PacketID != packet.IDStartGame && header.PacketID != packet.IDPlayStatus && header.PacketID != packet.IDResourcePacksInfo && header.PacketID != packet.IDResourcePackStack && header.PacketID != packet.IDResourcePackClientResponse && header.PacketID != packet.IDSetLocalPlayerAsInitialised && header.PacketID != packet.IDRequestChunkRadius && header.PacketID != packet.IDChunkRadiusUpdated {
		return
	}
	from, to := "?", "?"
	if src != nil {
		from = src.String()
	}
	if dst != nil {
		to = dst.String()
	}
	fmt.Printf("bebot: PACKET #%d id=%d bytes=%d %s -> %s\n", n, header.PacketID, len(payload), from, to)
}

func explainError(err error) string {
	if err == nil {
		return "<nil>"
	}
	var disconnect minecraft.DisconnectError
	if errors.As(err, &disconnect) {
		return fmt.Sprintf("server disconnect: %s", disconnect.Error())
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return fmt.Sprintf("%s (%s): %v", op.Op, op.Net, op.Err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout: " + err.Error()
	}
	return err.Error()
}

type player struct {
	name                                   string
	accountUUID                            uuid.UUID
	id                                     uint64
	uniqueID                               int64
	online                                 bool
	position                               mgl32.Vec3
	yaw, pitch                             float32
	sprinting, sneaking, swimming, jumping bool
	swinging, usingItem                    bool
}

type waypoint struct {
	X, Y, Z float32
	Note    string
}

type commandResult struct {
	key    string
	params []string
	text   string
}

const waypointsFile = "waypoints.json"

func loadWaypoints(path string) map[string]waypoint {
	data, err := os.ReadFile(path)
	if err != nil {
		return make(map[string]waypoint)
	}
	var wps map[string]waypoint
	if err := json.Unmarshal(data, &wps); err != nil {
		fmt.Println("bebot: ignoring invalid waypoints file:", err)
		return make(map[string]waypoint)
	}
	if wps == nil {
		wps = make(map[string]waypoint)
	}
	return wps
}

func (b *Bot) saveWaypoints() {
	b.mu.Lock()
	data, err := json.MarshalIndent(b.waypoints, "", "  ")
	b.mu.Unlock()
	if err != nil {
		fmt.Println("bebot: failed to encode waypoints:", err)
		return
	}
	if err := os.WriteFile(waypointsFile, data, 0600); err != nil {
		fmt.Println("bebot: failed to save waypoints:", err)
	}
}

func entityFlagSet(metadata protocol.EntityMetadata, key uint32, flag int) bool {
	raw, ok := metadata[key]
	if !ok {
		return false
	}
	switch value := raw.(type) {
	case int64:
		return value&(int64(1)<<flag) != 0
	case int32:
		return value&(int32(1)<<flag) != 0
	case byte:
		return value&(byte(1)<<flag) != 0
	}
	return false
}

func playerMetadataState(metadata protocol.EntityMetadata) (sneaking, sprinting, swimming, using bool) {
	// Player movement state is normally carried in EntityDataKeyFlags. Some
	// servers send the same metadata as a byte, so decode both wire forms.
	sneaking = entityFlagSet(metadata, protocol.EntityDataKeyFlags, protocol.EntityDataFlagSneaking)
	sprinting = entityFlagSet(metadata, protocol.EntityDataKeyFlags, protocol.EntityDataFlagSprinting)
	swimming = entityFlagSet(metadata, protocol.EntityDataKeyFlags, protocol.EntityDataFlagSwimming)
	using = entityFlagSet(metadata, protocol.EntityDataKeyFlags, protocol.EntityDataFlagUsingItem)
	return
}

type botMode uint8

const (
	modeIdle botMode = iota
	modeMoving
	modeFollowing
	modeMimic
	modeCamic
	modeLookAt
)

// stackRequest tracks an in-flight ItemStackRequest so the ItemStackResponse
// handler can deliver the matching status back to the sender.
type stackRequest struct {
	id   int32
	resp chan uint8
}

type Bot struct {
	cfg                          config
	source                       oauth2.TokenSource
	mu                           sync.Mutex
	conn                         *minecraft.Conn
	pos                          mgl32.Vec3
	last                         mgl32.Vec3
	tick                         uint64
	moving                       bool
	quitting                     bool
	players                      map[string]player
	playersByID                  map[uint64]string
	playerListReady              bool
	playerEventsAfter            time.Time
	waypoints                    map[string]waypoint
	fromConsole                  bool
	whisperTarget                string
	worldTime                    int64
	startGameTime                int64
	capture                      chan commandResult
	mode                         botMode
	followTarget, mimicTarget    string
	lookAtTarget                 string
	lookAtPos                    *mgl32.Vec3
	spawnPos                     protocol.BlockPos
	entityID                     uint64
	gameMode                     int32
	serverAuthoritativeInventory bool
	itemRequestID                int32
	stackPending                 *stackRequest
	containerOpen                chan struct{}
	facingYaw                    float32
	velocityY                    float32
	stuckTicks                   int
	lastJump, lastSwing, lastUse bool
	breakTarget                  *protocol.BlockPos
	breakTicks                   int
	breakAnimation               bool
	inventory                    []protocol.ItemInstance
	hotbarSlot                   byte
	heldItem                     protocol.ItemInstance
	mimicLastPos                 mgl32.Vec3
	mimicPosSet                  bool
	mimicSneaking, mimicSwimming bool
	sneaking, sentSneaking       bool
}

func (b *Bot) run(ctx context.Context) {
	defer func() {
		if recovered := recover(); recovered != nil {
			fmt.Printf("bebot: FATAL bot panic: %v\n%s\n", recovered, debug.Stack())
			b.close()
		}
	}()
	delay := time.Duration(b.cfg.Behaviour.ReconnectBaseDelayMs) * time.Millisecond
	if delay <= 0 {
		delay = 5 * time.Second
	}
	for !b.quitting {
		if err := b.connect(ctx); err != nil && !b.quitting {
			fmt.Println("bebot: disconnected:", err)
		}
		if b.quitting || ctx.Err() != nil {
			return
		}
		fmt.Printf("bebot: reconnecting in %s\n", delay)
		time.Sleep(delay)
		if delay < time.Minute {
			delay += 5 * time.Second
			if delay > time.Minute {
				delay = time.Minute
			}
		}
	}
}

func (b *Bot) connect(ctx context.Context) error {
	address := fmt.Sprintf("%s:%d", b.cfg.Server.Host, b.cfg.Server.Port)
	fmt.Printf("bebot: pinging %s...\n", address)
	pongBytes, err := raknet.PingContext(ctx, address)
	if err != nil {
		return fmt.Errorf("ping %s: %w", address, err)
	}
	pong, err := parseServerPong(pongBytes)
	if err != nil {
		return err
	}
	version := pong.Version
	if b.cfg.Server.Version != "" && b.cfg.Server.Version != pong.Version {
		fmt.Printf("bebot: warning: config version %s differs from server %s; using server version\n", b.cfg.Server.Version, pong.Version)
	}
	fmt.Printf("bebot: server=%s motd=%q protocol=%d version=%s players=%d/%d\n", pong.Edition, pong.MOTD, pong.ProtocolID, pong.Version, pong.Players, pong.MaxPlayers)
	fmt.Printf("bebot: connecting to %s using Minecraft %s...\n", address, version)
	clientData := login.ClientData{
		DeviceOS: protocol.DeviceOrbis, DeviceModel: "playstation_5_emu", DeviceID: login.DeviceID(uuid.NewString()),
		LanguageCode: "en_US", GameVersion: version, CurrentInputMode: packet.InputModeTouch,
		DefaultInputMode: packet.InputModeTouch, UIProfile: 0, MaxViewDistance: 16, MemoryTier: 3,
		PlatformType: 2, GraphicsMode: 1, TrustedSkin: true, CompatibleWithClientSideChunkGen: true,
		SelfSignedID: uuid.NewString(), ArmSize: "slim", SkinID: "Standard_Alex",
	}
	if err := applySkin(&clientData, "skin.png"); err != nil {
		fmt.Println("bebot: skin disabled:", err)
	} else {
		fmt.Printf("bebot: loaded skin.png (%dx%d)\n", clientData.SkinImageWidth, clientData.SkinImageHeight)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	d := minecraft.Dialer{ErrorLog: logger, TokenSource: b.source, ClientData: clientData, Protocol: serverProtocol{id: pong.ProtocolID, version: pong.Version}, DisconnectOnUnknownPackets: false, DisconnectOnInvalidPackets: false, EnableClientCache: false, PacketFunc: tracePacket}
	dialCtx, cancelDial := context.WithTimeout(ctx, time.Minute)
	defer cancelDial()
	c, err := d.DialContext(dialCtx, "raknet", address)
	if err != nil {
		return fmt.Errorf("dial %s: %s", address, explainError(err))
	}
	b.mu.Lock()
	b.conn = c
	b.mu.Unlock()
	defer func() { c.Close(); b.mu.Lock(); b.conn = nil; b.mu.Unlock() }()
	// Bedrock clients close the world-loading phase after StartGame. The
	// gophertunnel dialer handles login/resource packs and sends
	// SetLocalPlayerAsInitialised itself, but it does not emit this transition.
	// BDS can leave movement and damage state incomplete without the end marker.
	if err := c.WritePacket(&packet.ServerBoundLoadingScreen{Type: packet.LoadingScreenTypeEnd}); err != nil {
		return fmt.Errorf("finish loading screen: %s", explainError(err))
	}
	if err := c.Flush(); err != nil {
		return fmt.Errorf("flush loading screen: %s", explainError(err))
	}
	fmt.Println("bebot: connected; waiting for world spawn...")
	spawnCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := c.DoSpawnContext(spawnCtx); err != nil {
		return fmt.Errorf("spawn: %s", explainError(err))
	}
	b.pos = c.GameData().PlayerPosition
	b.last = b.pos
	b.entityID = c.GameData().EntityRuntimeID
	b.gameMode = c.GameData().PlayerGameMode
	// PlayerAuthInput tick is the client prediction tick, not world time.
	b.tick = 0
	b.players = make(map[string]player)
	b.playersByID = make(map[uint64]string)
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
	b.stackPending = nil
	b.containerOpen = make(chan struct{}, 1)
	game := c.GameData()
	b.serverAuthoritativeInventory = game.ServerAuthoritativeInventory
	b.worldTime = game.Time
	b.startGameTime = game.Time
	var daylightCycle bool
	for _, gr := range game.GameRules {
		if strings.EqualFold(gr.Name, "dodaylightcycle") {
			if v, ok := gr.Value.(bool); ok {
				daylightCycle = v
			}
		}
	}
	fmt.Printf("bebot: world time: startGameTime=%d dayCycleLockTime=%d doDaylightCycle=%v\n", game.Time, game.DayCycleLockTime, daylightCycle)
	fmt.Printf("bebot: joined %s at %.1f %.1f %.1f entity=%d gamemode=%d dimension=%d seed=%d\n", address, b.pos.X(), b.pos.Y(), b.pos.Z(), b.entityID, game.PlayerGameMode, game.Dimension, game.WorldSeed)
	fmt.Printf("bebot: movement settings rewind=%d server-authoritative-block-breaking=%v server-authoritative-inventory=%v interactions-disabled=%v chunk-radius=%d\n", game.PlayerMovementSettings.RewindHistorySize, game.PlayerMovementSettings.ServerAuthoritativeBlockBreaking, game.ServerAuthoritativeInventory, game.DisablePlayerInteractions, game.ChunkRadius)
	done := make(chan error, 1)
	go func() { done <- b.readPackets(c) }()
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	var afk *time.Ticker
	var afkChannel <-chan time.Time
	if b.cfg.Behaviour.AfkJumpIntervalMs > 0 {
		afk = time.NewTicker(time.Duration(b.cfg.Behaviour.AfkJumpIntervalMs) * time.Millisecond)
		defer afk.Stop()
		afkChannel = afk.C
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			return fmt.Errorf("connection reader: %s", explainError(err))
		case <-t.C:
			b.moveTick(c, false)
		case <-afkChannel:
			b.moveTick(c, true)
		}
	}
}

func (b *Bot) readPackets(c *minecraft.Conn) error {
	for {
		pk, err := c.ReadPacket()
		if err != nil {
			return err
		}
		switch p := pk.(type) {
		case *packet.Text:
			switch p.TextType {
			case packet.TextTypeChat:
				// Bedrock servers render private whispers as chat text in the
				// form "<name> whispers to you: <message>".
				if from, msg, ok := parseWhisper(p.Message); ok {
					b.handleWhisper(from, msg)
					break
				}
				fmt.Printf("[chat - %s] <%s> %s\n", time.Now().Format("2006-01-02 15:04:05"), cleanChatName(p.SourceName), p.Message)
				b.commandFromChat(p.SourceName, p.Message)
			case packet.TextTypeWhisper:
				b.handleWhisper(p.SourceName, p.Message)
			}
		case *packet.Transfer:
			return fmt.Errorf("server requested transfer to %s:%d; reconnecting to configured target", p.Address, p.Port)
		case *packet.Disconnect:
			fmt.Printf("bebot: server disconnected: %q\n", p.Message)
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
			fmt.Printf("bebot: command output (success=%d): %s\n", p.SuccessCount, strings.Join(msgs, " | "))
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
			}
			if name, ok := b.playersByID[p.EntityRuntimeID]; ok {
				v := b.players[name]
				oldY := v.position.Y()
				v.position, v.yaw, v.pitch = p.Position, p.Yaw, p.Pitch
				v.jumping = p.Position.Y()-oldY > .25
				b.players[name] = v
			}
			b.mu.Unlock()
		case *packet.CorrectPlayerMovePrediction:
			if p.PredictionType == packet.PredictionTypePlayer {
				b.mu.Lock()
				b.pos = p.Position
				b.last = p.Position
				b.mu.Unlock()
				//fmt.Printf("bebot: server movement correction at tick %d -> (%.2f, %.2f, %.2f)\n", p.Tick, p.Position.X(), p.Position.Y(), p.Position.Z())
			}
		case *packet.SetHealth:
			fmt.Printf("bebot: health update=%d\n", p.Health)
		case *packet.HurtArmour:
			fmt.Printf("bebot: armour damage cause=%d damage=%d slots=0x%x\n", p.Cause, p.Damage, p.ArmourSlots)
		case *packet.Respawn:
			if p.State == packet.RespawnStateReadyToSpawn {
				fmt.Printf("bebot: respawn state=%d position=(%.2f, %.2f, %.2f)\n", p.State, p.Position.X(), p.Position.Y(), p.Position.Z())
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
					// Ignore the initial snapshot, but report a name that appears
					// for the first time after the snapshot. Replayed list entries
					// remain online and therefore do not produce noise.
					if b.playerListReady && !wasOnline && time.Now().After(b.playerEventsAfter) {
						b.logPlayerEvent("joined", entry.Username)
					}
				} else if entry.ActionType == protocol.PlayerListActionRemove {
					for key, v := range b.players {
						if v.accountUUID == entry.UUID {
							if v.id != 0 {
								delete(b.playersByID, v.id)
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
		case *packet.RemoveActor:
			// RemoveActor is an entity despawn, not a disconnect: it fires when a
			// player leaves render distance or the server culls the entity, so it
			// must not be reported as a leave. Join/leave events are driven by
			// PlayerList add/remove only. Drop the runtime-ID mapping here; the
			// player entry stays so the bot knows they are still online.
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
			b.mu.Unlock()
		case *packet.Animate:
			if name, ok := b.playersByID[p.EntityRuntimeID]; ok && p.ActionType == packet.AnimateActionSwingArm {
				b.mu.Lock()
				v := b.players[name]
				v.jumping = false
				v.swinging = true
				v.usingItem = p.SwingSource == packet.AnimateSwingSourceUseItem || p.SwingSource == packet.AnimateSwingSourceInteract
				b.players[name] = v
				b.mu.Unlock()
			}
		case *packet.SetActorData:
			if name, ok := b.playersByID[p.EntityRuntimeID]; ok {
				sneaking, sprinting, swimming, using := playerMetadataState(p.EntityMetadata)
				b.mu.Lock()
				v := b.players[name]
				v.sneaking, v.sprinting, v.swimming = sneaking, sprinting, swimming
				v.usingItem = using
				b.players[name] = v
				b.mu.Unlock()
			}
		case *packet.MobEquipment:
			if p.EntityRuntimeID == b.entityID {
				b.hotbarSlot = p.HotBarSlot
				b.heldItem = p.NewItem
			}
		case *packet.ContainerOpen:
			select {
			case b.containerOpen <- struct{}{}:
			default:
			}
		case *packet.ItemStackResponse:
			var resp chan uint8
			var status uint8
			b.mu.Lock()
			pending := b.stackPending
			b.mu.Unlock()
			if pending != nil {
				for _, response := range p.Responses {
					if response.RequestID != pending.id {
						continue
					}
					status = response.Status
					resp = pending.resp
					b.mu.Lock()
					if b.stackPending == pending {
						b.stackPending = nil
					}
					b.mu.Unlock()
					if status != protocol.ItemStackResponseStatusOK {
						fmt.Printf("bebot: inventory request=%d rejected status=%d\n", response.RequestID, status)
					}
					break
				}
				if resp != nil {
					select {
					case resp <- status:
					default:
					}
				}
			} else {
				for _, response := range p.Responses {
					if response.Status != protocol.ItemStackResponseStatusOK {
						fmt.Printf("bebot: inventory request=%d rejected status=%d\n", response.RequestID, response.Status)
					}
				}
			}
		case *packet.InventoryContent:
			if p.WindowID == 0 {
				b.inventory = append([]protocol.ItemInstance(nil), p.Content...)
				if int(b.hotbarSlot) < len(b.inventory) {
					b.heldItem = b.inventory[b.hotbarSlot]
				}
				b.sendHeldItem(c)
			}
		case *packet.InventorySlot:
			if p.WindowID == 0 {
				if int(p.Slot) >= len(b.inventory) {
					grown := make([]protocol.ItemInstance, int(p.Slot)+1)
					copy(grown, b.inventory)
					b.inventory = grown
				}
				b.inventory[p.Slot] = p.NewItem
				if byte(p.Slot) == b.hotbarSlot {
					b.heldItem = p.NewItem
					b.sendHeldItem(c)
				}
			}
		case *packet.PlayerHotBar:
			if p.SelectHotBarSlot && p.SelectedHotBarSlot < 10 {
				b.hotbarSlot = byte(p.SelectedHotBarSlot)
				if int(b.hotbarSlot) < len(b.inventory) {
					b.heldItem = b.inventory[b.hotbarSlot]
					b.sendHeldItem(c)
				}
			}
		}
	}
}

// handleRespawn completes Bedrock's two-step client respawn exchange. A bot
// that only logs the Respawn packet remains dead on the server and commonly
// gets disconnected or stuck at its old position.
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
			fmt.Println("bebot: respawn response failed:", explainError(err))
		}
	case packet.RespawnStateReadyToSpawn:
		if err := c.WritePacket(&packet.PlayerAction{
			EntityRuntimeID: entityID,
			ActionType:      protocol.PlayerActionRespawn,
			BlockFace:       -1,
		}); err != nil {
			fmt.Println("bebot: respawn action failed:", explainError(err))
		}
	}
}

func (b *Bot) moveTick(c *minecraft.Conn, jump bool) {
	target := mgl32.Vec3{b.cfg.Target.X, b.cfg.Target.Y, b.cfg.Target.Z}
	var targetPlayer player
	targetMoved := true
	if b.mode == modeFollowing || b.mode == modeMimic || b.mode == modeCamic || b.mode == modeLookAt {
		b.mu.Lock()
		lookup := b.followTarget
		if b.mode == modeMimic {
			lookup = b.mimicTarget
		} else if b.mode == modeLookAt {
			lookup = b.lookAtTarget
		}
		if b.mode == modeLookAt && lookup == "" {
			nearestDistance := float32(math.MaxFloat32)
			for _, candidate := range b.players {
				if !candidate.online || candidate.id == 0 {
					continue
				}
				d := candidate.position.Sub(b.pos)
				distance := d.Dot(d)
				if distance < nearestDistance {
					nearestDistance = distance
					targetPlayer = candidate
				}
			}
		} else {
			targetPlayer = b.players[strings.ToLower(lookup)]
		}
		if b.mode == modeLookAt && b.lookAtPos != nil {
			targetPlayer = player{position: *b.lookAtPos, id: 1}
		}
		if b.mode == modeMimic {
			if b.mimicPosSet {
				d := targetPlayer.position.Sub(b.mimicLastPos)
				targetMoved = d.Dot(d) > .0004
			}
			b.mimicLastPos = targetPlayer.position
			b.mimicPosSet = targetPlayer.id != 0
			v := b.players[strings.ToLower(b.mimicTarget)]
			v.swinging = false
			v.usingItem = false
			b.players[strings.ToLower(b.mimicTarget)] = v
		}
		b.mu.Unlock()
		if targetPlayer.id != 0 && b.mode != modeLookAt {
			target = targetPlayer.position
		}
	}
	if (b.mode == modeMimic && !targetMoved) || b.mode == modeCamic || b.mode == modeLookAt {
		// Mimic reproduces the target's movement, not follow mode's behaviour of
		// closing the distance to a stationary target.
		target = b.pos
	}
	dx, dy, dz := target.X()-b.pos.X(), target.Y()-b.pos.Y(), target.Z()-b.pos.Z()
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
	b.moving = dist > .25
	if b.mode == modeIdle {
		b.moving = false
		jump = false
		b.velocityY = 0
	}
	if b.moving {
		step := b.cfg.Behaviour.WalkSpeed
		if step > dist {
			step = dist
		}
		// Horizontal movement follows the target; vertical motion is kept as
		// client-side physics so jumps and falling produce valid auth-input data.
		b.pos = b.pos.Add(mgl32.Vec3{dx / dist * step, 0, dz / dist * step})
		if dy > .4 && b.velocityY == 0 {
			jump = true
		}
	}
	// Predict the initial fall and jumps. The old implementation left Y at
	// the StartGame spawn height forever, which made BDS treat the actor as an
	// ungrounded/frozen entity. Horizontal movement remains step-limited.
	if jump && b.velocityY == 0 {
		b.velocityY = .42
	}
	if b.velocityY != 0 || b.pos.Y() > target.Y()+.6 {
		b.velocityY -= .08
		b.pos[1] += b.velocityY
		if b.pos.Y() <= target.Y() {
			b.pos[1] = target.Y()
			b.velocityY = 0
		}
	}
	flags := protocol.NewInputFlags(packet.InputFlagCount)
	flags.Set(packet.InputFlagBlockBreakingDelayEnabled)
	flags.Set(packet.InputFlagClientAckServerData)
	if b.moving {
		flags.Set(packet.InputFlagUp)
		flags.Set(packet.InputFlagSprinting)
		flags.Set(packet.InputFlagStartSprinting)
	}
	progress := b.pos.Sub(b.last)
	progressLen := float32(math.Sqrt(float64(progress.Dot(progress))))
	if b.moving && progressLen < .02 {
		b.stuckTicks++
	} else {
		b.stuckTicks = 0
	}
	if b.stuckTicks > 2 {
		jump = true
	}
	if b.mode == modeMimic && targetPlayer.sprinting {
		flags.Set(packet.InputFlagSprinting)
		flags.Set(packet.InputFlagStartSprinting)
	}
	desiredSneaking := b.sneaking
	if b.mode == modeMimic {
		desiredSneaking = targetPlayer.sneaking
	}
	if desiredSneaking {
		flags.Set(packet.InputFlagSneaking)
		flags.Set(packet.InputFlagSneakDown)
		flags.Set(packet.InputFlagPersistSneak)
		if !b.sentSneaking {
			flags.Set(packet.InputFlagStartSneaking)
		}
	} else if b.sentSneaking {
		flags.Set(packet.InputFlagStopSneaking)
	}
	if b.mode == modeMimic && targetPlayer.swimming {
		if !b.mimicSwimming {
			flags.Set(packet.InputFlagStartSwimming)
		}
	} else if b.mode == modeMimic && b.mimicSwimming {
		flags.Set(packet.InputFlagStopSwimming)
	}
	mirrorSwing := b.mode == modeMimic && targetPlayer.swinging
	mirrorUse := b.mode == modeMimic && targetPlayer.usingItem
	if mirrorSwing {
		flags.Set(packet.InputFlagMissedSwing)
	}
	if mirrorUse {
		flags.Set(packet.InputFlagPerformItemInteraction)
		flags.Set(packet.InputFlagStartUsingItem)
	}
	if jump {
		flags.Set(packet.InputFlagJumping)
		flags.Set(packet.InputFlagAscend)
		flags.Set(packet.InputFlagWantUp)
		flags.Set(packet.InputFlagStartJumping)
	}
	b.tick++
	moveZ := float32(0)
	if b.moving {
		moveZ = 1
	}
	yaw := float32(0)
	if b.moving {
		yaw = float32(math.Atan2(float64(-dx), float64(dz)) * 180 / math.Pi)
		if yaw < 0 {
			yaw += 360
		}
	}
	if b.mode == modeMimic || b.mode == modeCamic {
		yaw = targetPlayer.yaw
	}
	lookPitch := float32(0)
	if b.mode == modeLookAt && targetPlayer.id != 0 {
		lookDX := targetPlayer.position.X() - b.pos.X()
		lookDY := targetPlayer.position.Y() + .9 - b.pos.Y()
		lookDZ := targetPlayer.position.Z() - b.pos.Z()
		horizontal := math.Sqrt(float64(lookDX*lookDX + lookDZ*lookDZ))
		if horizontal > .001 {
			yaw = float32(math.Atan2(float64(-lookDX), float64(lookDZ)) * 180 / math.Pi)
			if yaw < 0 {
				yaw += 360
			}
			lookPitch = float32(-math.Atan2(float64(lookDY), horizontal) * 180 / math.Pi)
		}
	}
	b.facingYaw = yaw
	sneakStart := desiredSneaking && !b.sentSneaking
	sneakStop := !desiredSneaking && b.sentSneaking
	swimStart := b.mode == modeMimic && targetPlayer.swimming && !b.mimicSwimming
	swimStop := b.mode == modeMimic && !targetPlayer.swimming && b.mimicSwimming
	pitch := float32(0)
	if b.mode == modeMimic || b.mode == modeCamic {
		pitch = targetPlayer.pitch
	} else if b.mode == modeLookAt {
		pitch = lookPitch
	}
	auth := packet.PlayerAuthInput{Position: b.pos, Delta: b.pos.Sub(b.last), Yaw: yaw, HeadYaw: yaw, Pitch: pitch, MoveVector: mgl32.Vec2{0, moveZ}, AnalogueMoveVector: mgl32.Vec2{0, moveZ}, RawMoveVector: mgl32.Vec2{0, moveZ}, InputData: flags, InputMode: packet.InputModeTouch, PlayMode: packet.PlayModeScreen, InteractionModel: packet.InteractionModelTouch, Tick: b.tick}
	mineSwing := false
	if b.breakTarget != nil {
		action := protocol.PlayerActionContinueDestroyBlock
		actions := make([]protocol.PlayerBlockAction, 0, 2)
		if b.breakTicks == 0 {
			action = protocol.PlayerActionStartBreak
			mineSwing = b.breakAnimation
			actions = append(actions,
				protocol.PlayerBlockAction{Action: int32(protocol.PlayerActionStartBreak), BlockPos: *b.breakTarget, Face: -1},
				protocol.PlayerBlockAction{Action: int32(protocol.PlayerActionPredictDestroyBlock), BlockPos: *b.breakTarget, Face: -1},
			)
		} else if b.breakTicks == 1 {
			// The prediction was sent together with start_break above.
		}
		if len(actions) == 0 {
			actions = append(actions, protocol.PlayerBlockAction{Action: int32(action), BlockPos: *b.breakTarget, Face: -1})
		}
		auth.InputData.Set(packet.InputFlagPerformBlockActions)
		auth.BlockActions = protocol.Option(actions)
		b.breakTicks++
		// Server-authoritative block actions use continue_break after the
		// prediction; stop_break is a legacy PlayerAction and is invalid here.
		if b.breakTicks >= 40 {
			b.breakTarget = nil
			b.breakTicks = 0
		}
	}
	_ = c.WritePacket(&auth)
	if mineSwing {
		_ = c.WritePacket(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: b.entityID, SwingSource: packet.AnimateSwingSourceMine})
		fmt.Printf("bebot: break input action=%d target=%d %d %d\n", protocol.PlayerActionStartBreak, b.breakTarget.X(), b.breakTarget.Y(), b.breakTarget.Z())
	}
	if sneakStart {
		_ = c.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStartSneak, BlockFace: -1})
	}
	if sneakStop {
		_ = c.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStopSneak, BlockFace: -1})
	}
	b.sentSneaking = desiredSneaking
	if swimStart {
		_ = c.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStartSwimming, BlockFace: -1})
	}
	if swimStop {
		_ = c.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStopSwimming, BlockFace: -1})
	}
	if b.mode == modeMimic {
		if targetPlayer.sneaking != b.mimicSneaking {
			fmt.Printf("bebot: mimic target=%q runtime=%d sneaking=%v\n", targetPlayer.name, targetPlayer.id, targetPlayer.sneaking)
		}
		b.mimicSneaking = targetPlayer.sneaking
		b.mimicSwimming = targetPlayer.swimming
	}
	if b.mode == modeMimic && mirrorSwing {
		_ = c.WritePacket(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: b.entityID, SwingSource: packet.AnimateSwingSourceAttack})
	}
	if b.mode == modeMimic && mirrorUse {
		_ = c.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStartUsingItem, BlockFace: -1})
	}
	b.last = b.pos
}

func (b *Bot) say(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil {
		_ = b.conn.WritePacket(&packet.Text{TextType: packet.TextTypeChat, Message: s, NeedsTranslation: false})
	}
}
func (b *Bot) commandFromChat(sender, text string) {
	text = strings.TrimSpace(strings.Trim(text, "\ufeff\u200b\u200c\u200d"))
	if !strings.HasPrefix(strings.ToLower(text), "bebot") {
		return
	}
	if !b.isSuperuser(sender) && !isPublicCommand(text) {
		return
	}
	b.command(text)
}

func isPublicCommand(line string) bool {
	p := strings.Fields(line)
	if len(p) == 0 {
		return false
	}
	if strings.EqualFold(p[0], "bebot") || strings.EqualFold(p[0], "bebop") {
		p = p[1:]
	}
	if len(p) == 0 {
		return false
	}
	switch strings.ToLower(p[0]) {
	case "nether", "day":
		return true
	}
	return false
}

func (b *Bot) isSuperuser(name string) bool {
	return b.cfg.Superuser != "" && strings.EqualFold(cleanChatName(name), cleanChatName(b.cfg.Superuser))
}

func (b *Bot) consoleCommand(line string) {
	b.mu.Lock()
	b.fromConsole = true
	b.whisperTarget = ""
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.fromConsole = false
		b.mu.Unlock()
	}()
	b.command(line)
}

func parseWhisper(msg string) (sender, text string, ok bool) {
	low := strings.ToLower(msg)
	marker := "whispers to you"
	pos := strings.Index(low, marker)
	if pos < 0 {
		return "", "", false
	}
	sender = cleanChatName(msg[:pos])
	rest := strings.TrimPrefix(strings.TrimSpace(msg[pos+len(marker):]), ":")
	return sender, strings.TrimSpace(rest), true
}

func (b *Bot) handleWhisper(sender, message string) {
	sender = cleanChatName(sender)
	message = strings.TrimSpace(strings.Trim(message, "\ufeff\u200b\u200c\u200d"))
	if message == "" {
		return
	}
	fmt.Printf("[whisper - %s] <%s> %s\n", time.Now().Format("2006-01-02 15:04:05"), sender, message)
	if !b.isSuperuser(sender) || !strings.HasPrefix(strings.ToLower(message), "bebot") {
		return
	}
	b.mu.Lock()
	b.whisperTarget = sender
	b.fromConsole = false
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.whisperTarget = ""
		b.mu.Unlock()
	}()
	b.command(message)
}

func (b *Bot) runCommand(cmd string) {
	b.mu.Lock()
	c := b.conn
	viaChat := b.cfg.CommandsViaChat != nil && *b.cfg.CommandsViaChat
	b.mu.Unlock()
	if c == nil {
		fmt.Println("bebot: not connected; cannot run command")
		return
	}
	if !strings.HasPrefix(cmd, "/") {
		cmd = "/" + cmd
	}
	var err error
	if viaChat {
		err = c.WritePacket(&packet.Text{TextType: packet.TextTypeChat, Message: cmd, NeedsTranslation: false})
	} else {
		err = c.WritePacket(&packet.CommandRequest{
			CommandLine: cmd,
			CommandOrigin: protocol.CommandOrigin{
				Origin:         protocol.CommandOriginPlayer,
				UUID:           uuid.New(),
				PlayerUniqueID: 0,
			},
			Internal: false,
			Version:  "latest",
		})
	}
	if err != nil {
		fmt.Println("bebot: command failed:", explainError(err))
		return
	}
	if err := c.Flush(); err != nil {
		fmt.Println("bebot: command flush failed:", explainError(err))
		return
	}
	fmt.Println("bebot: executed command:", cmd)
}

func (b *Bot) whisper(target, message string) {
	b.mu.Lock()
	c := b.conn
	viaChat := b.cfg.CommandsViaChat != nil && *b.cfg.CommandsViaChat
	b.mu.Unlock()
	if c == nil {
		return
	}
	cmd := "/w " + target + " " + message
	var err error
	if viaChat {
		err = c.WritePacket(&packet.Text{TextType: packet.TextTypeChat, Message: cmd, NeedsTranslation: false})
	} else {
		err = c.WritePacket(&packet.CommandRequest{
			CommandLine: cmd,
			CommandOrigin: protocol.CommandOrigin{
				Origin:         protocol.CommandOriginPlayer,
				UUID:           uuid.New(),
				PlayerUniqueID: 0,
			},
			Internal: false,
			Version:  "latest",
		})
	}
	if err != nil {
		fmt.Println("bebot: whisper failed:", explainError(err))
		return
	}
	_ = c.Flush()
}

func cleanChatName(name string) string {
	runes := []rune(name)
	out := make([]rune, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\u00a7' && i+1 < len(runes) {
			i++
			continue
		}
		out = append(out, runes[i])
	}
	return strings.TrimSpace(string(out))
}
func (b *Bot) command(line string) {
	line = strings.Trim(line, "\ufeff\u200b\u200c\u200d \t\r\n")
	p := strings.Fields(line)
	if len(p) == 0 {
		return
	}
	p[0] = strings.TrimSuffix(strings.TrimSuffix(p[0], ":"), ",")
	if strings.EqualFold(p[0], "bebot") || strings.EqualFold(p[0], "bebop") {
		p = p[1:]
	}
	if len(p) == 0 {
		return
	}
	switch strings.ToLower(p[0]) {
	case "say":
		if len(p) < 2 {
			b.reply("Usage: say <message>")
			return
		}
		b.say(strings.Join(p[1:], " "))
	case "follow":
		if len(p) < 2 {
			b.reply("Usage: follow <playername>")
			return
		}
		b.followTarget = p[1]
		b.mimicTarget = p[1]
		b.mode = modeFollowing
		b.reply("Following " + p[1] + "…")
	case "mimic":
		if len(p) < 2 || strings.EqualFold(p[1], "clear") || strings.EqualFold(p[1], "stop") {
			b.mode = modeIdle
			b.followTarget = ""
			b.mimicTarget = ""
			b.reply("Cleared mimic mode.")
			return
		}
		b.mimicTarget = p[1]
		b.followTarget = p[1]
		b.mode = modeMimic
		b.reply("Mimicking " + p[1] + "!")
	case "camic":
		if len(p) < 2 || strings.EqualFold(p[1], "clear") || strings.EqualFold(p[1], "stop") {
			b.mode = modeIdle
			b.followTarget = ""
			b.mimicTarget = ""
			b.reply("Cleared camera mimic mode.")
			return
		}
		b.mimicTarget = p[1]
		b.followTarget = p[1]
		b.mode = modeCamic
		b.reply("Following camera rotation of " + p[1] + "!")
	case "lookat":
		if len(p) != 1 && len(p) != 2 && len(p) != 4 {
			b.reply("Usage: lookat [player] or lookat <x> <y> <z>")
			return
		}
		b.followTarget = ""
		b.mimicTarget = ""
		b.lookAtTarget = ""
		b.lookAtPos = nil
		if len(p) == 2 {
			b.lookAtTarget = p[1]
		} else if len(p) == 4 {
			var x, y, z float32
			if _, err := fmt.Sscanf(strings.Join(p[1:4], " "), "%f %f %f", &x, &y, &z); err != nil {
				b.reply("Usage: lookat [player] or lookat <x> <y> <z>")
				return
			}
			position := mgl32.Vec3{x, y, z}
			b.lookAtPos = &position
		}
		b.mode = modeLookAt
		if b.lookAtPos != nil {
			b.reply(fmt.Sprintf("Looking at (%.1f, %.1f, %.1f).", b.lookAtPos.X(), b.lookAtPos.Y(), b.lookAtPos.Z()))
		} else if b.lookAtTarget == "" {
			b.reply("Looking at the nearest player.")
		} else {
			b.reply("Looking at " + b.lookAtTarget + ".")
		}
	case "stoplookat":
		b.mode = modeIdle
		b.lookAtTarget = ""
		b.lookAtPos = nil
		b.reply("Stopped looking.")
	case "sneak":
		if len(p) > 2 {
			b.reply("Usage: sneak [on|off|toggle]")
			return
		}
		switch {
		case len(p) == 1, strings.EqualFold(p[1], "toggle"):
			b.sneaking = !b.sneaking
		case strings.EqualFold(p[1], "on"), strings.EqualFold(p[1], "true"):
			b.sneaking = true
		case strings.EqualFold(p[1], "off"), strings.EqualFold(p[1], "false"):
			b.sneaking = false
		default:
			b.reply("Usage: sneak [on|off|toggle]")
			return
		}
		b.reply(fmt.Sprintf("Sneaking %v.", b.sneaking))
	case "unsneak":
		b.sneaking = false
		b.reply("Sneaking disabled.")
	case "nether":
		b.netherCmd(p[1:])
	case "day":
		b.dayCmd()
	case "sleep":
		var x, y, z int32
		if len(p) >= 4 {
			fmt.Sscanf(strings.Join(p[1:4], " "), "%d %d %d", &x, &y, &z)
		} else {
			x, y, z = int32(math.Floor(float64(b.pos.X()))), int32(math.Floor(float64(b.pos.Y()))), int32(math.Floor(float64(b.pos.Z())))
		}
		b.spawnPos = protocol.BlockPos{x, y, z}
		b.interactBlock(b.spawnPos)
		fmt.Printf("bebot: sleep/spawn checkpoint requested at %d %d %d\n", x, y, z)
	case "break", "breakat", "breakcoord", "mine", "mineat":
		if len(p) != 5 {
			b.reply("Usage: break <x> <y> <z> <animation: true|false>")
			return
		}
		var x, y, z int32
		if _, err := fmt.Sscanf(strings.Join(p[1:4], " "), "%d %d %d", &x, &y, &z); err != nil {
			b.reply("Usage: break <x> <y> <z> <animation: true|false>")
			return
		}
		animate, err := strconv.ParseBool(p[4])
		if err != nil {
			b.reply("Usage: break <x> <y> <z> <animation: true|false>")
			return
		}
		b.startBreaking(protocol.BlockPos{x, y, z}, animate)
	case "stopbreak", "cancelbreak", "abortbreak":
		b.stopBreaking()
	case "dropall":
		b.dropSlots(0, len(b.inventory), 0)
	case "transfer":
		b.transferCmd(p)
	case "drop":
		if len(p) < 2 || len(p) > 3 {
			b.reply("Usage: drop <slot> [amount]")
			return
		}
		slot, err := strconv.Atoi(p[1])
		if err != nil || slot < 0 || slot >= len(b.inventory) {
			b.reply("Usage: drop <slot> [amount]")
			return
		}
		amount := 0
		if len(p) == 3 {
			amount, err = strconv.Atoi(p[2])
			if err != nil || amount <= 0 {
				b.reply("Usage: drop <slot> [amount]")
				return
			}
		}
		b.dropSlots(slot, 1, amount)
	case "help", "commands", "commandlist":
		b.help()
	case "hotbar", "slot":
		if len(p) != 2 {
			b.reply("Usage: hotbar <0-9>")
			return
		}
		slot, err := strconv.Atoi(p[1])
		if err != nil || slot < 0 || slot > 9 {
			b.reply("Usage: hotbar <0-9>")
			return
		}
		b.selectHotbar(byte(slot))
	case "count":
		if len(p) < 2 || strings.ToLower(p[1]) != "beds" {
			b.reply("Usage: count beds")
			return
		}
		fmt.Println("bebot: count beds is limited to decoded chunks; gophertunnel is receiving chunk packets")
	case "online", "players", "list":
		b.online()
	case "nearby", "near":
		b.nearby()
	case "coords", "coord", "coordinates", "playercoords", "playerpos", "pos", "positions", "where":
		b.coords()
	case "idle", "stop":
		b.mode = modeIdle
		b.followTarget = ""
		b.mimicTarget = ""
		b.lookAtTarget = ""
		b.lookAtPos = nil
		b.velocityY = 0
		b.reply("Completely idle & static.")
	case "waypoint", "waypoints", "wp":
		b.waypointCmd(p[1:])
	case "move":
		if len(p) < 4 {
			b.reply("Usage: move <x> <y> <z>")
			return
		}
		if _, err := fmt.Sscanf(strings.Join(p[1:4], " "), "%f %f %f", &b.cfg.Target.X, &b.cfg.Target.Y, &b.cfg.Target.Z); err != nil {
			b.reply("Usage: move <x> <y> <z>")
			return
		}
		b.mode = modeMoving
		b.followTarget = ""
		b.mimicTarget = ""
		b.lookAtTarget = ""
		b.lookAtPos = nil
		b.reply(fmt.Sprintf("Moving to (%.1f, %.1f, %.1f)…", b.cfg.Target.X, b.cfg.Target.Y, b.cfg.Target.Z))
	case "quit", "exit":
		b.reply("Disconnecting. Bye!")
		b.quitting = true
		b.close()
	default:
		b.reply("Unknown command: " + p[0])
	}
}

func (b *Bot) netherCmd(args []string) {
	var x, y, z float32
	switch len(args) {
	case 2:
		if _, err := fmt.Sscanf(strings.Join(args[0:2], " "), "%f %f", &x, &z); err != nil {
			b.reply("Usage: nether <x z> or nether <x y z>")
			return
		}
		b.reply(fmt.Sprintf("Nether: (%.1f, %.1f)", x/8, z/8))
	case 3:
		if _, err := fmt.Sscanf(strings.Join(args[0:3], " "), "%f %f %f", &x, &y, &z); err != nil {
			b.reply("Usage: nether <x z> or nether <x y z>")
			return
		}
		b.reply(fmt.Sprintf("Nether: (%.1f, %.1f, %.1f)", x/8, y, z/8))
	default:
		b.reply("Usage: nether <x z> or nether <x y z>")
	}
}

func (b *Bot) dayCmd() {
	b.mu.Lock()
	t := b.startGameTime
	setTime := b.worldTime
	b.mu.Unlock()
	day := t/24000 + 1
	tod := t % 24000
	phase := "morning"
	switch {
	case tod >= 12000 && tod < 18000:
		phase = "afternoon"
	case tod >= 18000:
		phase = "night"
	}
	h := (6 + tod/1000) % 24
	m := (tod % 1000) * 60 / 1000
	fmt.Printf("bebot: day debug: startGameTime=%d setTime=%d\n", t, setTime)
	b.reply(fmt.Sprintf("Day %d (%s) - %02d:%02d", day, phase, h, m))
}

func (b *Bot) reply(message string) {
	fmt.Println("bebot:", message)
	b.mu.Lock()
	target := b.whisperTarget
	fromConsole := b.fromConsole
	b.mu.Unlock()
	if target != "" {
		b.whisper(target, message)
		return
	}
	if fromConsole {
		return
	}
	b.say(message)
}
func (b *Bot) logPlayerEvent(event, name string) {
	if name == "" {
		return
	}
	fmt.Printf("[player - %s] %s %s\n", time.Now().Format("2006-01-02 15:04:05"), event, cleanChatName(name))
}
func (b *Bot) online() {
	b.mu.Lock()
	defer b.mu.Unlock()
	names := make([]string, 0, len(b.players))
	for key, p := range b.players {
		if !p.online {
			continue
		}
		name := p.name
		if name == "" {
			name = key
		}
		names = append(names, name)
	}
	fmt.Printf("bebot: online players (%d): %s\n", len(names), strings.Join(names, ", "))
}
func (b *Bot) nearby() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for key, p := range b.players {
		if !p.online || p.id == 0 {
			continue
		}
		d := p.position.Sub(b.pos)
		name := p.name
		if name == "" {
			name = key
		}
		fmt.Printf("bebot: %s @ (%.1f, %.1f, %.1f), %.1fm away\n", name, p.position.X(), p.position.Y(), p.position.Z(), float32(math.Sqrt(float64(d.Dot(d)))))
	}
}
func (b *Bot) coords() {
	fmt.Printf("bebot: bot @ (%.1f, %.1f, %.1f)\n", b.pos.X(), b.pos.Y(), b.pos.Z())
	b.nearby()
}

func (b *Bot) waypointCmd(p []string) {
	if len(p) < 1 {
		b.waypointHelp()
		return
	}
	switch strings.ToLower(p[0]) {
	case "help", "?":
		b.waypointHelp()
	case "list", "all":
		b.waypointList()
	case "add", "new", "create":
		b.waypointAdd(p[1:])
	case "remove", "rm", "del", "delete":
		b.waypointRemove(p[1:])
	case "update", "set", "edit", "modify":
		b.waypointUpdate(p[1:])
	default:
		b.waypointShow(p[0])
	}
}

func (b *Bot) waypointLookup(name string) (string, waypoint, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if w, ok := b.waypoints[name]; ok {
		return name, w, true
	}
	for key, w := range b.waypoints {
		if strings.EqualFold(key, name) {
			return key, w, true
		}
	}
	return "", waypoint{}, false
}

func (b *Bot) waypointAdd(args []string) {
	if len(args) < 4 {
		b.reply("Usage: waypoint add <name> <x y z> [note]")
		return
	}
	name := strings.TrimSpace(args[0])
	if name == "" {
		b.reply("Usage: waypoint add <name> <x y z> [note]")
		return
	}
	var x, y, z float32
	if _, err := fmt.Sscanf(strings.Join(args[1:4], " "), "%f %f %f", &x, &y, &z); err != nil {
		b.reply("Usage: waypoint add <name> <x y z> [note]")
		return
	}
	note := ""
	if len(args) > 4 {
		note = strings.Join(args[4:], " ")
	}
	b.mu.Lock()
	b.waypoints[name] = waypoint{X: x, Y: y, Z: z, Note: note}
	b.mu.Unlock()
	b.saveWaypoints()
	b.reply(fmt.Sprintf("Added waypoint %q at (%.1f, %.1f, %.1f).", name, x, y, z))
}

func (b *Bot) waypointRemove(args []string) {
	if len(args) < 1 || strings.TrimSpace(args[0]) == "" {
		b.reply("Usage: waypoint remove <name>")
		return
	}
	key, _, ok := b.waypointLookup(args[0])
	if !ok {
		b.reply("Waypoint not found: " + args[0])
		return
	}
	b.mu.Lock()
	delete(b.waypoints, key)
	b.mu.Unlock()
	b.saveWaypoints()
	b.reply("Removed waypoint " + key + ".")
}

func (b *Bot) waypointUpdate(args []string) {
	if len(args) < 4 {
		b.reply("Usage: waypoint update <name> <x y z> [note]")
		return
	}
	name := strings.TrimSpace(args[0])
	var x, y, z float32
	if _, err := fmt.Sscanf(strings.Join(args[1:4], " "), "%f %f %f", &x, &y, &z); err != nil {
		b.reply("Usage: waypoint update <name> <x y z> [note]")
		return
	}
	key, _, ok := b.waypointLookup(name)
	if !ok {
		b.reply("Waypoint not found: " + name)
		return
	}
	b.mu.Lock()
	existing := b.waypoints[key]
	note := existing.Note
	if len(args) > 4 {
		note = strings.Join(args[4:], " ")
	}
	b.waypoints[key] = waypoint{X: x, Y: y, Z: z, Note: note}
	b.mu.Unlock()
	b.saveWaypoints()
	b.reply(fmt.Sprintf("Updated waypoint %q to (%.1f, %.1f, %.1f).", key, x, y, z))
}

func (b *Bot) waypointShow(name string) {
	key, w, ok := b.waypointLookup(name)
	if !ok {
		b.reply("Waypoint not found: " + name + " (use \"waypoint list\")")
		return
	}
	if w.Note != "" {
		b.reply(fmt.Sprintf("%s: (%.1f, %.1f, %.1f) - %s", key, w.X, w.Y, w.Z, w.Note))
	} else {
		b.reply(fmt.Sprintf("%s: (%.1f, %.1f, %.1f)", key, w.X, w.Y, w.Z))
	}
}

func (b *Bot) waypointList() {
	b.mu.Lock()
	names := make([]string, 0, len(b.waypoints))
	for name := range b.waypoints {
		names = append(names, name)
	}
	sort.Strings(names)
	b.mu.Unlock()
	if len(names) == 0 {
		b.reply("No waypoints defined. Use: waypoint add <name> <x y z> [note]")
		return
	}
	b.reply(fmt.Sprintf("Waypoints (%d):", len(names)))
	for _, name := range names {
		b.waypointShow(name)
	}
}

func (b *Bot) waypointHelp() {
	lines := []string{
		"Waypoint commands:",
		"waypoint add <name> <x y z> [note] - add a waypoint",
		"waypoint remove <name> - remove a waypoint by name",
		"waypoint update <name> <x y z> [note] - update coordinates and note",
		"waypoint list - list all waypoints",
		"waypoint <name> - show a waypoint's details",
		"waypoint help - show this help",
	}
	for _, line := range lines {
		b.reply(line)
	}
}

func (b *Bot) startBreaking(pos protocol.BlockPos, animate bool) {
	b.breakAnimation = animate
	if b.gameMode == 1 || b.gameMode == 4 {
		b.mu.Lock()
		c := b.conn
		entityID := b.entityID
		b.mu.Unlock()
		if c == nil {
			fmt.Println("bebot: cannot break block: not connected")
			return
		}
		if err := c.WritePacket(&packet.PlayerAction{
			EntityRuntimeID: entityID,
			ActionType:      protocol.PlayerActionCreativePlayerDestroyBlock,
			BlockPosition:   pos,
			ResultPosition:  pos,
			BlockFace:       -1,
		}); err != nil {
			fmt.Println("bebot: creative break failed:", explainError(err))
		}
		if animate {
			_ = c.WritePacket(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: entityID, SwingSource: packet.AnimateSwingSourceMine})
		}
		if err := c.Flush(); err != nil {
			fmt.Println("bebot: creative break flush failed:", explainError(err))
		}
		// Some 1.26 servers advertise server-authoritative block breaking even
		// for Creative players. Keep the authoritative transaction queued too;
		// the creative action alone only produces the swing animation.
		b.breakTarget = &pos
		b.breakTicks = 0
		fmt.Printf("bebot: creative break requested action=%d at %d %d %d\n", protocol.PlayerActionCreativePlayerDestroyBlock, pos.X(), pos.Y(), pos.Z())
		return
	}
	b.breakTarget = &pos
	b.breakTicks = 0
	fmt.Printf("bebot: breaking block at %d %d %d\n", pos.X(), pos.Y(), pos.Z())
}

func (b *Bot) stopBreaking() {
	if b.breakTarget == nil {
		fmt.Println("bebot: no block breaking operation is active")
		return
	}
	pos := *b.breakTarget
	b.breakTarget = nil
	b.breakTicks = 0
	b.mu.Lock()
	c := b.conn
	entityID := b.entityID
	b.mu.Unlock()
	if c != nil {
		if err := c.WritePacket(&packet.PlayerAction{EntityRuntimeID: entityID, ActionType: protocol.PlayerActionAbortBreak, BlockPosition: pos, ResultPosition: pos, BlockFace: -1}); err != nil {
			fmt.Println("bebot: stop break failed:", explainError(err))
		} else {
			_ = c.Flush()
		}
	}
	fmt.Printf("bebot: stopped breaking block at %d %d %d\n", pos.X(), pos.Y(), pos.Z())
}

func (b *Bot) startBreakingFront(animate bool) {
	// Bedrock yaw points forward as (-sin(yaw), cos(yaw)).
	yaw := b.facingYaw
	if b.mode == modeMimic {
		b.mu.Lock()
		if p := b.players[strings.ToLower(b.mimicTarget)]; p.id != 0 {
			yaw = p.yaw
		}
		b.mu.Unlock()
	}
	rad := float64(yaw) * math.Pi / 180
	x := int32(math.Floor(float64(b.pos.X() - float32(math.Sin(rad)))))
	y := int32(math.Floor(float64(b.pos.Y())))
	z := int32(math.Floor(float64(b.pos.Z() + float32(math.Cos(rad)))))
	b.startBreaking(protocol.BlockPos{x, y, z}, animate)
}

func (b *Bot) help() {
	lines := []string{
		"Commands:",
		"move <x y z> - move to coordinates",
		"follow <player> - follow a player",
		"mimic <player> - mimic movement and actions",
		"camic <player> - mimic camera rotation only",
		"lookat [player] or lookat <x y z> - look at a player/point",
		"stoplookat - stop looking at a player",
		"sneak [on|off|toggle] - toggle or set sneaking",
		"unsneak - stop sneaking",
		"idle / stop - stop movement",
		"break <x y z> <animation: true|false> - break a block",
		"stopbreak - stop breaking",
		"hotbar <0-9> - select a hotbar slot",
		"dropall - drop all inventory items",
		"drop <slot> [amount] - drop items from a slot",
		"transfer <slot a> <slot b> - move items between inventory slots",
		"sleep [x y z] - set a sleep/spawn checkpoint",
		"players / list - list online players",
		"nearby - list nearby players",
		"coords - show coordinates",
		"waypoint help - waypoint add/remove/update/list/show",
		"say <message> - send chat",
		"quit / exit - disconnect",
	}
	for _, line := range lines {
		b.reply(line)
	}
}

func (b *Bot) selectHotbar(slot byte) {
	b.mu.Lock()
	c := b.conn
	entityID := b.entityID
	var item protocol.ItemInstance
	if int(slot) < len(b.inventory) {
		item = b.inventory[slot]
	}
	b.hotbarSlot = slot
	b.heldItem = item
	b.mu.Unlock()
	if c == nil {
		b.reply("Cannot change hotbar: bot is not connected.")
		return
	}
	if err := c.WritePacket(&packet.MobEquipment{
		EntityRuntimeID: entityID,
		NewItem:         item,
		InventorySlot:   slot,
		HotBarSlot:      slot,
		WindowID:        0,
	}); err != nil {
		b.reply("Hotbar selection failed: " + explainError(err))
		return
	}
	_ = c.Flush()
	b.reply(fmt.Sprintf("Selected hotbar slot %d.", slot))
}

// playerSlotContainer maps classic player inventory slots (0-8 hotbar, 9-35
// main inventory) to the container ID and slot used in item stack requests.
func playerSlotContainer(slot int) (byte, byte, bool) {
	switch {
	case slot >= 0 && slot <= 8:
		return protocol.ContainerHotBar, byte(slot), true
	case slot >= 9 && slot <= 35:
		return protocol.ContainerInventory, byte(slot), true
	}
	return 0, 0, false
}

func stackReqSlotInfo(container, slot byte, stackID int32) protocol.StackRequestSlotInfo {
	return protocol.StackRequestSlotInfo{
		Container:      protocol.FullContainerName{ContainerID: container},
		Slot:           slot,
		StackNetworkID: stackID,
	}
}

// stackStatusName names the common ItemStackResponse status codes for easier
// diagnosis when a request is rejected.
func stackStatusName(status uint8) string {
	switch status {
	case protocol.ItemStackResponseStatusOK:
		return "OK"
	case protocol.ItemStackResponseStatusError:
		return "generic error"
	case protocol.ItemStackResponseStatusInvalidRequestActionType:
		return "invalid request action type"
	case protocol.ItemStackResponseStatusActionRequestNotAllowed:
		return "action request not allowed"
	case protocol.ItemStackResponseStatusInvalidItemNetId:
		return "invalid stack network ID"
	case protocol.ItemStackResponseStatusRequestAlreadyInProgress:
		return "request already in progress"
	case protocol.ItemStackResponseStatusResultTransferFailed:
		return "result transfer failed"
	case protocol.ItemStackResponseStatusFailedToValidateSrcSlot:
		return "failed to validate source slot"
	case protocol.ItemStackResponseStatusFailedToValidateDstSlot:
		return "failed to validate destination slot"
	case protocol.ItemStackResponseStatusInvalidTransferAmount:
		return "invalid transfer amount"
	case protocol.ItemStackResponseStatusCannotSwapItem:
		return "cannot swap item"
	case protocol.ItemStackResponseStatusCannotPlaceItem:
		return "cannot place item"
	case protocol.ItemStackResponseStatusInvalidRemovedAmount:
		return "invalid removed amount"
	case protocol.ItemStackResponseStatusCannotDropItem:
		return "cannot drop item"
	case protocol.ItemStackResponseStatusInvalidSourceContainer:
		return "invalid source container"
	case protocol.ItemStackResponseStatusScreenStackError:
		return "screen stack error"
	}
	return fmt.Sprintf("unknown status %d", status)
}

// openInventory tells the server that the bot's own inventory screen is open
// and waits for the ContainerOpen reply. Real clients only send item stack
// requests that move items while this screen is open, so mirror that.
func (b *Bot) openInventory(c *minecraft.Conn) {
	select {
	case <-b.containerOpen:
	default:
	}
	_ = c.WritePacket(&packet.Interact{ActionType: packet.InteractActionOpenInventory})
	_ = c.Flush()
	select {
	case <-b.containerOpen:
	case <-time.After(2 * time.Second):
	}
}

// closeInventory closes the bot's inventory screen, mirroring what a real
// client does after moving items around.
func (b *Bot) closeInventory(c *minecraft.Conn) {
	// WindowType 'none' (-9) is what real clients send when closing the player
	// inventory screen.
	_ = c.WritePacket(&packet.ContainerClose{WindowID: 0, ContainerType: 247, ServerSide: false})
	_ = c.Flush()
}

func (b *Bot) clearStackPending(id int32) {
	b.mu.Lock()
	if b.stackPending != nil && b.stackPending.id == id {
		b.stackPending = nil
	}
	b.mu.Unlock()
}

// sendItemStackRequest sends an ItemStackRequest and waits for the matching
// ItemStackResponse. It returns whether the request was accepted along with
// the raw status code.
func (b *Bot) sendItemStackRequest(c *minecraft.Conn, actions []protocol.StackRequestAction) (bool, uint8) {
	b.mu.Lock()
	b.itemRequestID -= 2
	id := b.itemRequestID
	resp := make(chan uint8, 1)
	b.stackPending = &stackRequest{id: id, resp: resp}
	b.mu.Unlock()
	if err := c.WritePacket(&packet.ItemStackRequest{Requests: []protocol.ItemStackRequest{{RequestID: id, Actions: actions, FilterCause: -1}}}); err != nil {
		b.clearStackPending(id)
		b.reply("Item request failed: " + explainError(err))
		return false, 0
	}
	if err := c.Flush(); err != nil {
		b.clearStackPending(id)
		b.reply("Item request flush failed: " + explainError(err))
		return false, 0
	}
	select {
	case status := <-resp:
		return status == protocol.ItemStackResponseStatusOK, status
	case <-time.After(3 * time.Second):
		b.clearStackPending(id)
		return false, 0
	}
}

// transferCmd implements `bebot transfer <slot a> <slot b>`. It moves the whole
// stack when the destination is empty, or swaps the two stacks when the
// destination is occupied.
func (b *Bot) transferCmd(p []string) {
	if len(p) != 3 {
		b.reply("Usage: transfer <slot a> <slot b>")
		return
	}
	slotA, errA := strconv.Atoi(p[1])
	slotB, errB := strconv.Atoi(p[2])
	b.mu.Lock()
	c := b.conn
	inventory := append([]protocol.ItemInstance(nil), b.inventory...)
	b.mu.Unlock()
	if errA != nil || errB != nil || slotA < 0 || slotB < 0 || slotA >= len(inventory) || slotB >= len(inventory) {
		b.reply("Usage: transfer <slot a> <slot b>")
		return
	}
	if c == nil {
		b.reply("Cannot transfer items: bot is not connected.")
		return
	}
	if slotA == slotB {
		b.reply("Cannot transfer an item to its own slot.")
		return
	}
	srcItem := inventory[slotA]
	dstItem := inventory[slotB]
	if srcItem.Stack.Count == 0 {
		b.reply(fmt.Sprintf("Slot %d is empty.", slotA))
		return
	}
	srcContainer, srcSlot, ok := playerSlotContainer(slotA)
	if !ok {
		b.reply(fmt.Sprintf("Slot %d is not a transferable inventory slot.", slotA))
		return
	}
	dstContainer, dstSlot, ok := playerSlotContainer(slotB)
	if !ok {
		b.reply(fmt.Sprintf("Slot %d is not a transferable inventory slot.", slotB))
		return
	}
	var actions []protocol.StackRequestAction
	verb := "Moved"
	if dstItem.Stack.Count == 0 {
		count := srcItem.Stack.Count
		if count > 255 {
			count = 255
		}
		take := &protocol.TakeStackRequestAction{}
		take.Count = byte(count)
		take.Source = stackReqSlotInfo(srcContainer, srcSlot, srcItem.StackNetworkID)
		take.Destination = stackReqSlotInfo(protocol.ContainerCursor, 0, 0)
		place := &protocol.PlaceStackRequestAction{}
		place.Count = byte(count)
		place.Source = stackReqSlotInfo(protocol.ContainerCursor, 0, srcItem.StackNetworkID)
		place.Destination = stackReqSlotInfo(dstContainer, dstSlot, 0)
		actions = []protocol.StackRequestAction{take, place}
	} else {
		swap := &protocol.SwapStackRequestAction{
			Source:      stackReqSlotInfo(srcContainer, srcSlot, srcItem.StackNetworkID),
			Destination: stackReqSlotInfo(dstContainer, dstSlot, dstItem.StackNetworkID),
		}
		actions = []protocol.StackRequestAction{swap}
		verb = "Swapped"
	}
	b.openInventory(c)
	defer b.closeInventory(c)
	ok, status := b.sendItemStackRequest(c, actions)
	if !ok {
		b.reply(fmt.Sprintf("Transfer failed (slot %d <-> %d): %s.", slotA, slotB, stackStatusName(status)))
		return
	}
	b.reply(fmt.Sprintf("%s %d item(s) from slot %d to slot %d.", verb, srcItem.Stack.Count, slotA, slotB))
}

func (b *Bot) dropSlots(start, count, requested int) {
	b.mu.Lock()
	c := b.conn
	type dropRequest struct {
		slot  int
		item  protocol.ItemInstance
		count uint16
	}
	drops := make([]dropRequest, 0, count)
	for slot := start; slot < start+count && slot < len(b.inventory); slot++ {
		item := b.inventory[slot]
		if item.Stack.Count == 0 {
			continue
		}
		dropCount := int(item.Stack.Count)
		if requested > 0 {
			if requested > dropCount {
				requested = dropCount
			}
			dropCount = requested
		}
		drops = append(drops, dropRequest{slot: slot, item: item, count: uint16(dropCount)})
	}
	b.mu.Unlock()
	if c == nil {
		b.reply("Cannot drop items: bot is not connected.")
		return
	}
	if len(drops) == 0 {
		b.reply("No items found to drop.")
		return
	}
	// Drops go through the modern item stack request system, like dragging a
	// stack out of the open inventory screen: take the stack to the cursor,
	// then drop it from there. Each slot is its own request so a rejection on
	// one slot does not fail the rest.
	b.openInventory(c)
	defer b.closeInventory(c)
	dropped := 0
	for _, drop := range drops {
		container, slotIn, ok := playerSlotContainer(drop.slot)
		if !ok {
			continue
		}
		count := drop.count
		if count > 255 {
			count = 255
		}
		fromSlot := stackReqSlotInfo(container, slotIn, drop.item.StackNetworkID)
		cursorEmpty := stackReqSlotInfo(protocol.ContainerCursor, 0, 0)
		cursorFull := stackReqSlotInfo(protocol.ContainerCursor, 0, drop.item.StackNetworkID)
		take := &protocol.TakeStackRequestAction{}
		take.Count = byte(count)
		take.Source = fromSlot
		take.Destination = cursorEmpty
		dropAction := &protocol.DropStackRequestAction{Count: byte(count), Source: cursorFull}
		ok, status := b.sendItemStackRequest(c, []protocol.StackRequestAction{take, dropAction})
		if !ok {
			fmt.Printf("bebot: drop slot %d rejected status %d (%s)\n", drop.slot, status, stackStatusName(status))
			continue
		}
		dropped++
	}
	b.reply(fmt.Sprintf("Dropped items from %d of %d slot(s).", dropped, len(drops)))
}

func (b *Bot) sendHeldItem(c *minecraft.Conn) {
	b.mu.Lock()
	entityID := b.entityID
	slot := b.hotbarSlot
	item := b.heldItem
	b.mu.Unlock()
	if c == nil {
		return
	}
	if err := c.WritePacket(&packet.MobEquipment{
		EntityRuntimeID: entityID,
		NewItem:         item,
		InventorySlot:   slot,
		HotBarSlot:      slot,
		WindowID:        0,
	}); err != nil {
		fmt.Println("bebot: held-item update failed:", explainError(err))
	}
}

func (b *Bot) interactBlock(pos protocol.BlockPos) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return
	}
	_ = b.conn.WritePacket(&packet.Interact{ActionType: packet.InteractActionMouseOverEntity, TargetEntityRuntimeID: 0})
	_ = b.conn.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStartSleeping, BlockPosition: pos, ResultPosition: pos, BlockFace: 1})
	_ = b.conn.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStartItemUseOn, BlockPosition: pos, ResultPosition: pos, BlockFace: 1})
}
func (b *Bot) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil {
		// Give the server a proper client disconnect before closing RakNet.
		_ = b.conn.WritePacket(&packet.Disconnect{HideDisconnectionScreen: true, Message: "Client shutting down"})
		time.Sleep(100 * time.Millisecond)
		_ = b.conn.Close()
		b.conn = nil
	}
}
