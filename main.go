package main

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math"
	"math/rand/v2"
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

	"github.com/df-mc/go-nethernet"
	"github.com/df-mc/go-nethernet/discovery"
	"github.com/df-mc/go-xsapi/v2/xal/sisu"
	"github.com/df-mc/go-xsapi/v2/xal/xasd"
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

// skinConfig holds the bot's skin in a dedicated skin.json file.
type skinConfig struct {
	Type               string `json:"type"`               // "slim", "normal"/"wide", "pack"/"custom"
	Path               string `json:"path"`               // PNG file (slim/normal) or skin pack folder (pack/custom)
	Skin               string `json:"skin"`               // skin id/index in the pack (pack/custom)
	OverrideAppearance *bool  `json:"overrideAppearance"` // nil defaults to true
}

// defaultSkinConfig is what "skin default" restores: the standard slim skin.
var defaultSkinConfig = skinConfig{Type: "slim", Path: "skin/skin.png"}

// overrideAppearanceOn returns true unless sc.OverrideAppearance is explicitly set to false.
func overrideAppearanceOn(sc skinConfig) bool {
	if sc.OverrideAppearance == nil {
		return true
	}
	return *sc.OverrideAppearance
}

// baseReconnectDelay returns the configured base reconnect backoff.
func baseReconnectDelay(cfg config) time.Duration {
	d := time.Duration(cfg.Behaviour.ReconnectBaseDelayMs) * time.Millisecond
	if d <= 0 {
		d = 5 * time.Second
	}
	return d
}

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
		RenderDistance                          int32 `json:"renderDistance"`
	} `json:"behaviour"`
	CommandsViaChat *bool      `json:"commands_via_chat"`
	Skin            skinConfig `json:"skin"`
}

var tracePackets bool
var tracedPackets atomic.Uint64

type xstsDiskCache struct {
	Snapshot    *sisu.Snapshot `json:"snapshot"`
	DeviceToken *xasd.Token    `json:"device_token"`
	PrivateKey  []byte         `json:"private_key"`
}

func loadXBLCache(path string, source oauth2.TokenSource) *auth.XBLTokenCache {
	if data, err := os.ReadFile(path); err == nil {
		var cache xstsDiskCache
		if json.Unmarshal(data, &cache) == nil && cache.Snapshot != nil {
			if key, err := x509.ParseECPrivateKey(cache.PrivateKey); err == nil {
				device := xasd.ReuseTokenSource(auth.AndroidConfig.Config.Config, cache.DeviceToken, key)
				session := auth.AndroidConfig.New(source, &sisu.SessionConfig{
					Snapshot:          cache.Snapshot,
					DeviceTokenSource: device,
				})
				log.Println("bebot: restored Xbox Live session from", path)
				return auth.AndroidConfig.ReuseTokenCache(session)
			}
		}
	}
	return auth.AndroidConfig.NewTokenCache()
}

func saveXBLCache(path string, cache *auth.XBLTokenCache) {
	if cache == nil {
		return
	}
	session := cache.Session()
	if session == nil {
		return
	}
	deviceToken, _ := cache.Device().DeviceToken(context.Background())
	keyBytes, _ := x509.MarshalECPrivateKey(cache.Device().ProofKey())

	dc := xstsDiskCache{
		Snapshot:    session.Snapshot(),
		DeviceToken: deviceToken,
		PrivateKey:  keyBytes,
	}
	if data, err := json.Marshal(dc); err == nil {
		_ = os.WriteFile(path, data, 0644)
	}
}

const offlineUUIDFile = "offline_uuid.json"

func loadOrGenerateOfflineUUID(path string, botName string, renew bool) uuid.UUID {
	cache := make(map[string]string)
	if !renew {
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &cache)
			if idStr, ok := cache[botName]; ok {
				if parsed, err := uuid.Parse(idStr); err == nil {
					log.Printf("bebot: restored saved offline UUID %s for %q from %s\n", parsed.String(), botName, path)
					return parsed
				}
			}
		}
	}
	newUUID := uuid.New()
	cache[botName] = newUUID.String()
	if data, err := json.MarshalIndent(cache, "", "  "); err == nil {
		_ = os.WriteFile(path, data, 0644)
	}
	if renew {
		log.Printf("bebot: (-renew) generated fresh offline UUID %s for %q\n", newUUID.String(), botName)
	} else {
		log.Printf("bebot: generated and saved offline UUID %s for %q to %s\n", newUUID.String(), botName, path)
	}
	return newUUID
}

func isFlagPassed(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func main() {
	configPath := flag.String("config", "config.json", "bot configuration file")
	authCachePath := flag.String("auth-cache", "token_cache.json", "path used to persist the Microsoft session")
	debugFlag := flag.Bool("debug", false, "print packet-level handshake diagnostics")
	skinFile := flag.String("skin", "skin.json", "bot skin configuration file")
	localFlag := flag.Bool("local", false, "connect to a local/LAN singleplayer world (127.0.0.1)")
	hostFlag := flag.String("host", "", "override server host (e.g. 127.0.0.1)")
	portFlag := flag.Int("port", 0, "override server port (e.g. 19132)")
	offlineFlag := flag.Bool("offline", false, "connect without Microsoft authentication (LAN mode)")
	botNameFlag := flag.String("name", "Bebot", "bot display name (used in offline/LAN mode)")
	renewFlag := flag.Bool("renew", false, "generate a fresh identity/UUID for offline/LAN mode instead of reusing saved UUID")
	flag.Parse()

	// Initialize file logger
	logWriter := io.MultiWriter(os.Stderr, newDailyLogger("logs"))
	log.SetOutput(logWriter)

	tracePackets = *debugFlag
	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Println("bebot:", err)
		return
	}
	if *localFlag {
		if *hostFlag == "" {
			*hostFlag = "127.0.0.1"
		}
		if *portFlag == 0 && cfg.Server.Port == 0 {
			cfg.Server.Port = 19132
		}
		if !isFlagPassed("offline") {
			*offlineFlag = true
		}
	}
	if *hostFlag != "" {
		cfg.Server.Host = *hostFlag
	}
	if *portFlag != 0 {
		cfg.Server.Port = *portFlag
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
	skinCfg, err := loadSkin(*skinFile)
	if err != nil {
		log.Println("bebot: skin:", err)
		skinCfg = defaultSkinConfig
	}
	cfg.Skin = skinCfg
	log.Printf("bebot: skin config: type=%s path=%s skin=%s override=%v\n", cfg.Skin.Type, cfg.Skin.Path, cfg.Skin.Skin, overrideAppearanceOn(cfg.Skin))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var source oauth2.TokenSource
	var xblCache *auth.XBLTokenCache
	var offlineUUID uuid.UUID
	if !*offlineFlag {
		var err error
		source, err = loadAuthSource(*authCachePath)
		if err != nil {
			log.Println("bebot: authentication failed:", err)
			return
		}
		xblCache = loadXBLCache("xsts_cache.json", source)
	} else {
		offlineUUID = loadOrGenerateOfflineUUID(offlineUUIDFile, *botNameFlag, *renewFlag)
		log.Printf("bebot: running in offline/LAN mode as %q [UUID: %s] (no Microsoft authentication)\n", *botNameFlag, offlineUUID.String())
	}

	bot := &Bot{
		cfg:         cfg,
		source:      source,
		offline:     *offlineFlag,
		botName:     *botNameFlag,
		offlineUUID: offlineUUID,
		local:         *localFlag,
		players:       make(map[string]player),
		entities:      make(map[int64]string),
		mode:          modeIdle,
		waypoints:     loadWaypoints(waypointsFile),
		configPath:    *configPath,
		skinPath:      *skinFile,
		xblCache:      xblCache,
		itemNamesByID:     make(map[int32]string),
		mineAttempts:      make(map[protocol.BlockPos]int),
		mineRecentBroken:  make(map[protocol.BlockPos]uint64),
		mineAirRuntimeIDs: make(map[uint32]bool),
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("bebot: FATAL panic: %v\n%s\n", recovered, debug.Stack())
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
				log.Println("bebot: restored saved Microsoft session from", path)
				return source, nil
			}
			log.Println("bebot: saved Microsoft session expired; signing in again")
		}
	}
	log.Println("bebot: no usable saved Microsoft session; device authentication is required")
	token, err := auth.RequestLiveToken()
	if err != nil {
		return nil, err
	}
	source := &persistedTokenSource{source: auth.RefreshTokenSource(token), path: path}
	if err := source.save(token); err != nil {
		return nil, err
	}
	log.Println("bebot: Microsoft session saved to", path)
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
		log.Println("bebot: warning: could not save refreshed session:", err)
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

// loadSkin reads the bot's skin config from its dedicated skin.json file.
func loadSkin(path string) (skinConfig, error) {
	var sc skinConfig
	b, err := os.ReadFile(path)
	if err != nil {
		return sc, err
	}
	if err := json.Unmarshal(b, &sc); err != nil {
		return sc, err
	}
	if sc.Type == "" {
		sc.Type = defaultSkinConfig.Type
	}
	if sc.Path == "" {
		sc.Path = defaultSkinConfig.Path
	}
	return sc, nil
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
	log.Printf("bebot: PACKET #%d id=%d bytes=%d %s -> %s\n", n, header.PacketID, len(payload), from, to)
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
	lastSwing                              time.Time
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
		log.Println("bebot: ignoring invalid waypoints file:", err)
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
		log.Println("bebot: failed to encode waypoints:", err)
		return
	}
	if err := os.WriteFile(waypointsFile, data, 0600); err != nil {
		log.Println("bebot: failed to save waypoints:", err)
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

func entityPose(metadata protocol.EntityMetadata) (byte, bool) {
	raw, ok := metadata[protocol.EntityDataKeyPoseIndex]
	if !ok {
		return 0, false
	}
	switch value := raw.(type) {
	case int64:
		return byte(value), true
	case int32:
		return byte(value), true
	case byte:
		return value, true
	}
	return 0, false
}

func playerMetadataState(metadata protocol.EntityMetadata) (sneaking, sprinting, swimming, using bool) {
	// Player movement state is normally carried in EntityDataKeyFlags. Some
	// servers send the same metadata as a byte, so decode both wire forms.
	sneaking = entityFlagSet(metadata, protocol.EntityDataKeyFlags, protocol.EntityDataFlagSneaking)
	sprinting = entityFlagSet(metadata, protocol.EntityDataKeyFlags, protocol.EntityDataFlagSprinting)
	swimming = entityFlagSet(metadata, protocol.EntityDataKeyFlags, protocol.EntityDataFlagSwimming)
	using = entityFlagSet(metadata, protocol.EntityDataKeyFlags, protocol.EntityDataFlagUsingItem)
	// A stationary sneak/swim toggle is announced through the actor pose
	// (EntityDataKeyPoseIndex) rather than the movement flags, so honour the
	// Bedrock ActorPose values whenever the pose changes. Pose 0 (standing)
	// clears the state unless the flags in this same packet still report
	// sneaking/swimming.
	if pose, ok := entityPose(metadata); ok {
		switch pose {
		case 4: // ActorPose.Sneaking
			sneaking = true
			swimming = false
		case 2: // ActorPose.Swimming
			swimming = true
			sneaking = false
		}
	}
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

// headshakeMode drives the bot's scripted head motion: "yes" bobs the head
// up/down (pitch), "no" turns it left/right (yaw).
type headshakeMode uint8

const (
	headshakeNone headshakeMode = iota
	headshakeYes
	headshakeNo
)

// headshake*Ticks tune the animation cadence (1 tick = 50ms).
const (
	headshakeSwingTicks = 8  // ticks for a single quarter-swing
	headshakePeriod     = 32 // ticks for a full left-to-left / up-to-up cycle
	headshakeOnceTicks  = 96 // ticks (~4.8s, 3 cycles) before a non-repeat shake stops
)

// defaultEmoteLength is the default number of ticks (50ms) the bot reports an
// emote lasts and the interval at which a repeating emote is resent. The emote
// metadata list has no per-emote duration, so this is a guess (Bedrock emotes
// are typically ~3s = 60 ticks); it can be overridden per emote with the
// length argument of the emote command.
const defaultEmoteLength = 60

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
	entities                     map[int64]string
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
	invMu                        sync.Mutex
	facingYaw, facingPitch       float32
	idleYaw, idlePitch           float32
	velocityY                    float32
	stuckTicks                   int
	lastJump, lastSwing, lastUse bool
	breakTarget                  *protocol.BlockPos
	breakTicks                   int
	breakAnimation               bool
	breakRepeat                  bool
	abortBreakTarget             *protocol.BlockPos
	inventory                    []protocol.ItemInstance
	hotbarSlot                   byte
	heldItem                     protocol.ItemInstance
	mimicLastPos                 mgl32.Vec3
	mimicPosSet                  bool
	mimicSneaking, mimicSwimming bool
	sneaking, sentSneaking       bool
	headshake                    headshakeMode
	headshakeRepeat              bool
	headshakeTicks               int
	emoteID                      string
	emoteRepeat                  bool
	payback                      bool
	killTarget                   string
	lastHealth                   int32
	autoFish                     bool
	fishingHookID                uint64
	emoteLength                  uint32
	emoteNextTick                uint64
	selfUUID                     uuid.UUID
	configPath                   string
	skinPath                     string
	reconnectDelay               time.Duration
	xblCache                     *auth.XBLTokenCache
	mineQueue                    []protocol.BlockPos
	mineTotal                    int
	mineCount                    int
	mineActive                   bool
	mineCurrent                  *protocol.BlockPos
	mineStuckTicks               int
	mineTicksReq                 int
	mineBoxMin                   protocol.BlockPos
	mineBoxMax                   protocol.BlockPos
	mineContinuous               bool
	mineFaceAccurate             bool
	offline                      bool
	botName                      string
	offlineUUID                  uuid.UUID
	local                        bool
	itemNamesByID                map[int32]string
	mineAttempts                 map[protocol.BlockPos]int
	mineRecentBroken             map[protocol.BlockPos]uint64
	mineAirRuntimeIDs            map[uint32]bool
	mineLastFinishedPos          protocol.BlockPos
	mineLastFinishedTick         uint64
}

func (b *Bot) run(ctx context.Context) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("bebot: FATAL bot panic: %v\n%s\n", recovered, debug.Stack())
			b.close()
		}
	}()
	b.mu.Lock()
	b.reconnectDelay = baseReconnectDelay(b.cfg)
	b.mu.Unlock()
	for !b.quitting {
		if err := b.connect(ctx); err != nil && !b.quitting {
			log.Println("bebot: disconnected:", err)
		}
		if b.quitting || ctx.Err() != nil {
			return
		}
		b.mu.Lock()
		delay := b.reconnectDelay
		b.mu.Unlock()
		log.Printf("bebot: reconnecting in %s\n", delay)
		time.Sleep(delay)
		b.mu.Lock()
		if b.reconnectDelay < time.Minute {
			b.reconnectDelay += 5 * time.Second
			if b.reconnectDelay > time.Minute {
				b.reconnectDelay = time.Minute
			}
		}
		b.mu.Unlock()
	}
}

// pingServer pings the given Bedrock address with a short per-attempt deadline
// and parses the RakNet or NetherNet pong.
func pingServer(ctx context.Context, address string) (serverPong, string, string, error) {
	// 1. Try RakNet first (with a short 3s timeout)
	rakCtx, rakCancel := context.WithTimeout(ctx, 3*time.Second)
	defer rakCancel()
	pongBytes, rakErr := raknet.PingContext(rakCtx, address)
	if rakErr == nil {
		if p, err := parseServerPong(pongBytes); err == nil {
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
			if p, err := parseServerPong(pongBytes); err == nil {
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
				if p, err := parseServerPong(pBytes); err == nil {
					log.Printf("bebot: discovered local LAN world on port %d\n", testPort)
					return p, "raknet", testAddr, nil
				}
			}
		}
	}

	return serverPong{}, "", "", fmt.Errorf("ping %s: raknet (%v) and nethernet failed", address, rakErr)
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
		botUUID = loadOrGenerateOfflineUUID(offlineUUIDFile, b.botName, false)
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
	if skin, err := loadSkinFromConfig(b.cfg.Skin); err != nil {
		log.Println("bebot: skin disabled:", err)
	} else {
		fillClientDataSkin(&clientData, skin)
	}

	logger := slog.New(slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{Level: slog.LevelDebug}))
	dialer := minecraft.Dialer{
		ErrorLog: logger,
		ClientData: clientData,
		IdentityData: login.IdentityData{
			DisplayName: name,
			Identity:    botUUID.String(),
		},
		Protocol:                   serverProtocol{id: serverData.Protocol, version: serverData.Version},
		DisconnectOnUnknownPackets: false,
		DisconnectOnInvalidPackets: false,
		EnableClientCache:          false,
		PacketFunc:                 tracePacket,
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
		return fmt.Errorf("dial local: %s", explainError(err))
	}

	log.Println("bebot: stage: login complete; finishing loading screen...")
	b.mu.Lock()
	b.conn = c
	b.mu.Unlock()
	defer func() { c.Close(); b.mu.Lock(); b.conn = nil; b.mu.Unlock() }()

	if err := c.WritePacket(&packet.ServerBoundLoadingScreen{Type: packet.LoadingScreenTypeEnd}); err != nil {
		return fmt.Errorf("finish loading screen: %s", explainError(err))
	}
	if err := c.Flush(); err != nil {
		return fmt.Errorf("flush loading screen: %s", explainError(err))
	}

	log.Println("bebot: stage: waiting for world spawn...")
	spawnCtx, cancel := context.WithTimeout(ctx, 1*time.Minute)
	defer cancel()
	if err := c.DoSpawnContext(spawnCtx); err != nil {
		return fmt.Errorf("spawn: %s", explainError(err))
	}
	log.Println("bebot: stage: world spawn ok")

	renderDist := b.cfg.Behaviour.RenderDistance
	if renderDist == 0 {
		renderDist = 16
	}
	_ = c.WritePacket(&packet.RequestChunkRadius{ChunkRadius: int32(renderDist), MaxChunkRadius: uint8(renderDist)})

	b.pos = c.GameData().PlayerPosition
	b.last = b.pos
	b.entityID = c.GameData().EntityRuntimeID
	b.gameMode = c.GameData().PlayerGameMode
	b.tick = uint64(c.GameData().Time)
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
	b.mineActive = false
	b.mineFaceAccurate = false
	b.mineQueue = nil
	b.mineCurrent = nil
	b.mineStuckTicks = 0
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

func (b *Bot) connect(ctx context.Context) error {
	if b.local {
		return b.connectLocal(ctx)
	}
	address := fmt.Sprintf("%s:%d", b.cfg.Server.Host, b.cfg.Server.Port)
	// Stage 1: ping to learn the server's identity and whether it is actually up.
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
	// Stage 2: detect an offline lobby. Aternos (and similar hosts) expose a
	// lightweight proxy with MOTD "Offline" while the real server is stopped.
	// Connecting to that lobby accepts the connection but never produces a
	// world spawn, so the bot would hang forever. Instead, poll until the real
	// server reports itself online, then dial.
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
	if skin, err := loadSkinFromConfig(b.cfg.Skin); err != nil {
		log.Println("bebot: skin disabled:", err)
	} else {
		fillClientDataSkin(&clientData, skin)
		log.Printf("bebot: loaded skin type=%s path=%s (%dx%d)\n", b.cfg.Skin.Type, b.cfg.Skin.Path, clientData.SkinImageWidth, clientData.SkinImageHeight)
	}

	var identityData login.IdentityData
	if b.offline || b.source == nil {
		botUUID := b.offlineUUID
		if botUUID == uuid.Nil {
			botUUID = loadOrGenerateOfflineUUID(offlineUUIDFile, b.botName, false)
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
		Protocol:                   serverProtocol{id: pong.ProtocolID, version: pong.Version},
		DisconnectOnUnknownPackets: false,
		DisconnectOnInvalidPackets: false,
		EnableClientCache:          false,
		PacketFunc:                 tracePacket,
		DownloadResourcePack: func(id uuid.UUID, version string, current, total int) bool {
			log.Printf("bebot: skipping resource pack %s version %s (%d/%d)\n", id, version, current, total)
			return false
		},
	}
	// Stage 3: RakNet/NetherNet handshake + login handshake + resource packs. This is the
	// phase most likely to stall on a freshly started host, so surface it.
	log.Printf("bebot: stage: %s handshake + login...\n", transport)
	dialCtx, cancelDial := context.WithTimeout(ctx, 5*time.Minute)
	if b.source != nil && b.xblCache != nil {
		dialCtx = auth.WithXBLTokenCache(dialCtx, b.xblCache)
	}
	defer cancelDial()
	c, err := d.DialContext(dialCtx, transport, dialAddress)
	if err != nil {
		return fmt.Errorf("dial: %s", explainError(err))
	}
	if b.source != nil && b.xblCache != nil {
		saveXBLCache("xsts_cache.json", b.xblCache)
	}
	log.Println("bebot: stage: login complete; finishing loading screen...")
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
	// Stage 4: wait for the server to acknowledge spawn (StartGame world data).
	log.Println("bebot: stage: waiting for world spawn...")
	spawnCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := c.DoSpawnContext(spawnCtx); err != nil {
		return fmt.Errorf("spawn: %s", explainError(err))
	}
	log.Println("bebot: stage: world spawn ok")

	// The gophertunnel dialer is patched to use a 4-chunk radius to speed up the
	// Geyser login handshake. Now that we're spawned, we request the real chunk
	// radius so that the server sends chunks/entities up to the configured limit.
	renderDist := b.cfg.Behaviour.RenderDistance
	if renderDist == 0 {
		renderDist = 16 // fallback default
	}
	_ = c.WritePacket(&packet.RequestChunkRadius{ChunkRadius: int32(renderDist), MaxChunkRadius: uint8(renderDist)})
	log.Printf("bebot: requested updated chunk radius of %d chunks\n", renderDist)

	b.pos = c.GameData().PlayerPosition
	b.last = b.pos
	b.entityID = c.GameData().EntityRuntimeID
	if id, err := uuid.Parse(c.IdentityData().Identity); err == nil {
		b.selfUUID = id
	}
	b.gameMode = c.GameData().PlayerGameMode
	// PlayerAuthInput.Tick is the server tick the client believes it is at
	// (used to pair server corrections with the inputs they refer to). Start
	// from the world tick reported in StartGame and advance one per tick, like
	// a real client (and the old JS bot, which used StartGame.current_tick).
	b.tick = uint64(c.GameData().Time)
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
	b.mineActive = false
	b.mineFaceAccurate = false
	b.mineQueue = nil
	b.mineCurrent = nil
	b.mineStuckTicks = 0
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
	log.Printf("bebot: movement settings rewind=%d server-authoritative-block-breaking=%v server-authoritative-inventory=%v interactions-disabled=%v chunk-radius=%d\n", game.PlayerMovementSettings.RewindHistorySize, game.PlayerMovementSettings.ServerAuthoritativeBlockBreaking, game.ServerAuthoritativeInventory, game.DisablePlayerInteractions, game.ChunkRadius)
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
			log.Printf("bebot: server disconnected: %q\n", p.Message)
		case *packet.UpdateBlock:
			b.mu.Lock()
			if b.mineActive {
				pos := p.Position
				isCurrentBreak := b.breakTarget != nil && pos == *b.breakTarget
				_, isRecentBreak := b.mineRecentBroken[pos]
				isRecentFinish := pos == b.mineLastFinishedPos && b.tick-b.mineLastFinishedTick < 200

				if isCurrentBreak || isRecentBreak || isRecentFinish {
					// This block was broken into AIR by the bot. Register runtime ID as air.
					if b.mineAirRuntimeIDs == nil {
						b.mineAirRuntimeIDs = make(map[uint32]bool)
					}
					b.mineAirRuntimeIDs[p.NewBlockRuntimeID] = true

					if isCurrentBreak {
						if b.mineRecentBroken != nil {
							b.mineRecentBroken[*b.breakTarget] = b.tick
						}
						b.mineLastFinishedPos = *b.breakTarget
						b.mineLastFinishedTick = b.tick
						b.breakTarget = nil
						b.breakTicks = 0
						b.mineCurrent = nil
						b.mineCount++
						if b.mineCount%25 == 0 || len(b.mineQueue) == 0 {
							log.Printf("bebot: block broken (%d mined)\n", b.mineCount)
						}
					}
					if b.mineAttempts != nil {
						delete(b.mineAttempts, pos)
					}
					filtered := make([]protocol.BlockPos, 0, len(b.mineQueue))
					for _, q := range b.mineQueue {
						if q != pos {
							filtered = append(filtered, q)
						}
					}
					b.mineQueue = filtered
				} else if b.mineAirRuntimeIDs != nil && b.mineAirRuntimeIDs[p.NewBlockRuntimeID] {
					// Update is for an AIR block (broken by player, explosion, or previously broken).
					// Air must NEVER be added to the mine queue!
					if b.mineAttempts != nil {
						delete(b.mineAttempts, pos)
					}
					filtered := make([]protocol.BlockPos, 0, len(b.mineQueue))
					for _, q := range b.mineQueue {
						if q != pos {
							filtered = append(filtered, q)
						}
					}
					b.mineQueue = filtered

					// If the bot is currently targeting this position, it's already air! Move on immediately.
					if b.breakTarget != nil && *b.breakTarget == pos {
						b.breakTarget = nil
						b.breakTicks = 0
						b.mineCurrent = nil
					} else if b.mineCurrent != nil && *b.mineCurrent == pos {
						b.mineCurrent = nil
					}
				} else if b.isInMineBox(pos) {
					// Genuine solid block placed or generated in the mine area
					if b.mineAttempts != nil && b.mineAttempts[pos] >= 2 {
						// Block failed to break, do not re-queue
					} else {
						alreadyQueued := false
						if b.mineCurrent != nil && *b.mineCurrent == pos {
							alreadyQueued = true
						}
						if b.breakTarget != nil && *b.breakTarget == pos {
							alreadyQueued = true
						}
						if !alreadyQueued {
							for _, q := range b.mineQueue {
								if q == pos {
									alreadyQueued = true
									break
								}
							}
						}
						if !alreadyQueued {
							b.mineQueue = append(b.mineQueue, pos)
							b.mineTotal++
							log.Printf("bebot: new block detected at (%d, %d, %d), added to mine queue\n", pos.X(), pos.Y(), pos.Z())
						}
					}
				}
			}
			b.mu.Unlock()
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
				b.players[name] = v
			}
			b.mu.Unlock()
		case *packet.CorrectPlayerMovePrediction:
			if p.PredictionType == packet.PredictionTypePlayer {
				b.mu.Lock()
				b.pos = p.Position
				b.last = p.Position
				// sneaking := b.sentSneaking
				b.mu.Unlock()
				// if sneaking {
				// 	log.Printf("bebot: correction(tick=%d clientTick=%d) while sneaking -> (%.2f, %.2f, %.2f)\n", p.Tick, b.tick, p.Position.X(), p.Position.Y(), p.Position.Z())
				// }
			}
		case *packet.SetHealth:
			log.Printf("bebot: health update=%d\n", p.Health)
			b.mu.Lock()
			b.lastHealth = p.Health
			b.mu.Unlock()
		case *packet.HurtArmour:
			log.Printf("bebot: armour damage cause=%d damage=%d slots=0x%x\n", p.Cause, p.Damage, p.ArmourSlots)
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
					b.useItemInHand() // reel in
					
					// cast again in 1 second
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
			if p.EntityRuntimeID == b.entityID {
				// BDS normally does not echo a player's own metadata, but if it
				// does, it tells us whether the server-side sneak was applied.
				// b.mu.Lock()
				// sneaking := b.sentSneaking
				// b.mu.Unlock()
				// if sneaking {
				// 	log.Printf("bebot: own SetActorData tick=%d flags=%v\n", p.Tick, p.EntityMetadata[protocol.EntityDataKeyFlags])
				// }
			}
			if name, ok := b.playersByID[p.EntityRuntimeID]; ok {
				sneaking, sprinting, swimming, using := playerMetadataState(p.EntityMetadata)
				_, hasFlags := p.EntityMetadata[protocol.EntityDataKeyFlags]
				pose, hasPose := entityPose(p.EntityMetadata)
				if hasFlags || hasPose {
					b.mu.Lock()
					isMimicTarget := strings.EqualFold(name, b.mimicTarget)
					b.mu.Unlock()
					if isMimicTarget {
						// Debug: log.Printf("bebot: mimic metadata target=%q pose=%d flags=%v sneak=%v swim=%v\n", name, pose, p.EntityMetadata[protocol.EntityDataKeyFlags], sneaking, swimming)
					}
				}
				// Only overwrite the player's movement state when this packet
				// explicitly carries it (a flags key, or a definitive
				// sneaking/swimming pose). An unrelated metadata refresh (held
				// item change, pose refresh, ...) that omits the flags key must
				// not cancel a sneak that a previous packet set. This keeps the
				// detected state persistent, mirroring how the sneak command's
				// b.sneaking stays true until explicitly toggled off.
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
			// Some servers announce a player's sneak/swim toggles as
			// edge-triggered PlayerAction packets. Apply them immediately so
			// a stationary toggle is not missed when no metadata update
			// follows it.
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
						log.Printf("bebot: inventory request=%d rejected status=%d\n", response.RequestID, status)
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
						log.Printf("bebot: inventory request=%d rejected status=%d\n", response.RequestID, response.Status)
					}
				}
			}
		case *packet.InventoryContent:
			log.Printf("bebot: trace: InventoryContent WindowID=%d", p.WindowID)
			if p.WindowID == 0 {
				b.inventory = append([]protocol.ItemInstance(nil), p.Content...)
				if int(b.hotbarSlot) < len(b.inventory) {
					b.heldItem = b.inventory[b.hotbarSlot]
				}
				b.sendHeldItem(c)
			}
		case *packet.InventorySlot:
			log.Printf("bebot: trace: InventorySlot WindowID=%d Slot=%d Item=%v", p.WindowID, p.Slot, p.NewItem.Stack.ItemType.NetworkID)
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
		case *packet.InventoryTransaction:
			if _, ok := p.TransactionData.(*protocol.NormalTransactionData); ok {
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
								b.sendHeldItem(c)
							}
						}
					}
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
			log.Println("bebot: respawn response failed:", explainError(err))
		}
	case packet.RespawnStateReadyToSpawn:
		if err := c.WritePacket(&packet.PlayerAction{
			EntityRuntimeID: entityID,
			ActionType:      protocol.PlayerActionRespawn,
			BlockFace:       -1,
		}); err != nil {
			log.Println("bebot: respawn action failed:", explainError(err))
		}
	}
}

func (b *Bot) moveTick(c *minecraft.Conn, jump bool) {
	b.mu.Lock()
	if b.mineActive {
		if b.mineCurrent == nil {
			if len(b.mineQueue) == 0 {
				if !b.mineContinuous {
					b.mineActive = false
					b.mode = modeIdle
					count := b.mineCount
					b.mu.Unlock()
					b.reply(fmt.Sprintf("Area mining complete! Mined %d blocks.", count))
					b.mu.Lock()
				} else {
					if b.mode == modeMoving {
						b.mode = modeIdle
					}
				}
			} else {
				b.mineCurrent = b.pickNextMineBlock()
				b.mineStuckTicks = 0
			}
		}

		if b.mineCurrent != nil {
			cur := *b.mineCurrent
			eyePos := mgl32.Vec3{b.pos.X(), b.pos.Y() + 1.62, b.pos.Z()}

			// Calculate distance to closest point on target block bounding box
			closestX := float32(math.Max(float64(cur.X()), math.Min(float64(eyePos.X()), float64(cur.X()+1))))
			closestY := float32(math.Max(float64(cur.Y()), math.Min(float64(eyePos.Y()), float64(cur.Y()+1))))
			closestZ := float32(math.Max(float64(cur.Z()), math.Min(float64(eyePos.Z()), float64(cur.Z()+1))))
			diff := mgl32.Vec3{closestX - eyePos.X(), closestY - eyePos.Y(), closestZ - eyePos.Z()}
			dist := float32(math.Sqrt(float64(diff.Dot(diff))))

			reachLimit := float32(5.0)
			if b.gameMode == 1 || b.gameMode == 4 {
				reachLimit = 6.5
			}

			// If the block is above eye level and slightly out of reach, jump to reach it
			if dist > reachLimit && closestY > eyePos.Y() && dist <= reachLimit+1.25 {
				jump = true
			}

			if dist > reachLimit {
				b.cfg.Target.X = float32(cur.X()) + 0.5
				b.cfg.Target.Y = float32(cur.Y())
				b.cfg.Target.Z = float32(cur.Z()) + 0.5
				b.mode = modeMoving
				b.mineStuckTicks++
				if b.mineStuckTicks > 100 {
					log.Printf("bebot: skipping unreachable block at %d %d %d\n", cur.X(), cur.Y(), cur.Z())
					b.mineCurrent = nil
					b.mineStuckTicks = 0
				}
			} else {
				if b.mode == modeMoving {
					b.mode = modeIdle
				}
				b.mineStuckTicks = 0

				centerDiff := mgl32.Vec3{float32(cur.X()) + 0.5 - eyePos.X(), float32(cur.Y()) + 0.5 - eyePos.Y(), float32(cur.Z()) + 0.5 - eyePos.Z()}
				yaw := float32(math.Atan2(float64(-centerDiff.X()), float64(centerDiff.Z())) * 180 / math.Pi)
				if yaw < 0 {
					yaw += 360
				}
				hLen := math.Sqrt(float64(centerDiff.X()*centerDiff.X() + centerDiff.Z()*centerDiff.Z()))
				pitch := float32(-math.Atan2(float64(centerDiff.Y()), hLen) * 180 / math.Pi)
				b.idleYaw = yaw
				b.idlePitch = pitch
				b.facingYaw = yaw
				b.facingPitch = pitch

				if b.breakTarget == nil {
					b.breakTarget = b.mineCurrent
					b.breakTicks = 0
					b.breakAnimation = true
					b.breakRepeat = false
				}
			}
		}
	}
	b.mu.Unlock()

	target := mgl32.Vec3{b.cfg.Target.X, b.cfg.Target.Y, b.cfg.Target.Z}
	var targetPlayer player
	targetMoved := true
	if b.mode == modeFollowing || b.mode == modeMimic || b.mode == modeCamic || b.mode == modeLookAt {
		b.mu.Lock()
		lookup := b.followTarget
		switch b.mode {
		case modeMimic:
			lookup = b.mimicTarget
		case modeLookAt:
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
				// Only ground movement counts as movement: a jump in place
				// changes Y without moving horizontally and must not make the
				// bot walk toward the target.
				d[1] = 0
				targetMoved = d.Dot(d) > .0004
			}
			b.mimicLastPos = targetPlayer.position
			b.mimicPosSet = targetPlayer.id != 0
			v := b.players[strings.ToLower(b.mimicTarget)]
			v.swinging = false
			v.usingItem = false
			v.jumping = false
			b.players[strings.ToLower(b.mimicTarget)] = v
		} else if b.mode == modeFollowing && targetPlayer.id != 0 {
			v := b.players[strings.ToLower(b.followTarget)]
			v.jumping = false
			b.players[strings.ToLower(b.followTarget)] = v
		}
		b.mu.Unlock()
		if targetPlayer.id != 0 && b.mode != modeLookAt {
			target = targetPlayer.position
		}
	}
	if (b.mode == modeMimic && !targetMoved) || b.mode == modeCamic || b.mode == modeLookAt || b.mode == modeIdle {
		// Mimic reproduces the target's movement, not follow mode's behaviour of
		// closing the distance to a stationary target.
		target = b.pos
	}
	dx, dy, dz := target.X()-b.pos.X(), target.Y()-b.pos.Y(), target.Z()-b.pos.Z()
	// Only the horizontal distance decides whether to walk: a target jumping
	// in place has a large dy but no ground movement, so it must not cause
	// forward input.
	hDist := float32(math.Sqrt(float64(dx*dx + dz*dz)))
	b.moving = hDist > .25
	if b.mode == modeIdle {
		b.moving = false
	}
	if b.moving {
		step := b.cfg.Behaviour.WalkSpeed
		if step > hDist {
			step = hDist
		}
		// Horizontal movement follows the target; vertical motion is kept as
		// client-side physics so jumps and falling produce valid auth-input data.
		b.pos = b.pos.Add(mgl32.Vec3{dx / hDist * step, 0, dz / hDist * step})
	}
	// A target jumping in place must still be jumped after, without moving.
	if dy > .4 && b.velocityY == 0 {
		jump = true
	}
	// The packet handler latches targetPlayer.jumping from a MovePlayer Y
	// jump edge, so the mimic reacts on the very next tick regardless of
	// whether the target moved horizontally.
	if (b.mode == modeMimic || b.mode == modeFollowing) && targetPlayer.id != 0 && targetPlayer.jumping {
		jump = true
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
	sneakStart := desiredSneaking && !b.sentSneaking
	sneakStop := !desiredSneaking && b.sentSneaking
	if desiredSneaking {
		flags.Set(packet.InputFlagSneaking)
		flags.Set(packet.InputFlagSneakDown)
		flags.Set(packet.InputFlagPersistSneak)
		flags.Set(packet.InputFlagSneakCurrentRaw)
		if sneakStart {
			flags.Set(packet.InputFlagStartSneaking)
			flags.Set(packet.InputFlagSneakPressedRaw)
		}
	} else if sneakStop {
		flags.Set(packet.InputFlagStopSneaking)
		flags.Set(packet.InputFlagSneakReleasedRaw)
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
		// Aim the camera at the target's eye line. The pitch is measured from
		// the bot's camera (pos.Y + eye height), so the vertical component must
		// be the difference between the two eye lines: (target.Y + eye) -
		// (bot.Y + eye) = target.Y - bot.Y. Measuring from the feet and adding a
		// head offset here made the bot look up above the target's head.
		lookDY := targetPlayer.position.Y() - b.pos.Y()
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
	if b.mode == modeIdle {
		yaw = b.idleYaw
	}
	b.facingYaw = yaw
	swimStart := b.mode == modeMimic && targetPlayer.swimming && !b.mimicSwimming
	swimStop := b.mode == modeMimic && !targetPlayer.swimming && b.mimicSwimming
	pitch := float32(0)
	switch b.mode {
	case modeMimic, modeCamic:
		pitch = targetPlayer.pitch
	case modeLookAt:
		pitch = lookPitch
	}
	if b.mode == modeIdle {
		pitch = b.idlePitch
	}
	if b.headshake != headshakeNone {
		// Scripted head shake: "no" swings the head side to side (yaw), "yes"
		// bobs it up and down (pitch). A sine phase over the period gives a
		// smooth back-and-forth motion added on top of the current facing.
		phase := float32(math.Sin(float64(b.headshakeTicks) * 2 * math.Pi / headshakePeriod))
		switch b.headshake {
		case headshakeNo:
			yaw += phase * 18
		case headshakeYes:
			pitch += phase * 22
		}
		b.headshakeTicks++
		if !b.headshakeRepeat && b.headshakeTicks >= headshakeOnceTicks {
			b.headshake = headshakeNone
			b.headshakeTicks = 0
		}
	}
	b.facingPitch = pitch

	b.mu.Lock()
	bt := b.breakTarget
	var abortBt *protocol.BlockPos
	if b.abortBreakTarget != nil {
		abortBt = b.abortBreakTarget
		b.abortBreakTarget = nil
	}
	b.mu.Unlock()

	// If actively breaking a block, ensure crosshair (Yaw & Pitch) aims directly at the block when face-accurate
	if bt != nil && (!b.mineActive || b.mineFaceAccurate) {
		eyePos := b.pos.Add(mgl32.Vec3{0, 1.62, 0})
		tX := float32(bt.X()) + 0.5
		tY := float32(bt.Y()) + 0.5
		tZ := float32(bt.Z()) + 0.5
		lookDX := tX - eyePos.X()
		lookDY := tY - eyePos.Y()
		lookDZ := tZ - eyePos.Z()
		hLen := math.Sqrt(float64(lookDX*lookDX + lookDZ*lookDZ))
		if hLen > .001 {
			yaw = float32(math.Atan2(float64(-lookDX), float64(lookDZ)) * 180 / math.Pi)
			if yaw < 0 {
				yaw += 360
			}
		}
		pitch = float32(-math.Atan2(float64(lookDY), hLen) * 180 / math.Pi)
		b.facingYaw = yaw
		b.facingPitch = pitch
	}

	auth := packet.PlayerAuthInput{Position: b.pos, Delta: b.pos.Sub(b.last), Yaw: yaw, HeadYaw: yaw, Pitch: pitch, MoveVector: mgl32.Vec2{0, moveZ}, AnalogueMoveVector: mgl32.Vec2{0, moveZ}, RawMoveVector: mgl32.Vec2{0, moveZ}, InputData: flags, InputMode: packet.InputModeGamePad, PlayMode: packet.PlayModeScreen, InteractionModel: packet.InteractionModelTouch, Tick: b.tick}
	mineSwing := false

	var actions []protocol.PlayerBlockAction
	eyePos := b.pos.Add(mgl32.Vec3{0, 1.62, 0})
	if abortBt != nil {
		abortFace := int32(1)
		if !b.mineActive || b.mineFaceAccurate {
			abortFace = determineBlockFace(eyePos, *abortBt)
		}
		actions = append(actions, protocol.PlayerBlockAction{Action: int32(protocol.PlayerActionAbortBreak), BlockPos: *abortBt, Face: abortFace})
	}

	if bt != nil {
		btFace := int32(1)
		if !b.mineActive || b.mineFaceAccurate {
			btFace = determineBlockFace(eyePos, *bt)
		}
		if actions == nil {
			actions = make([]protocol.PlayerBlockAction, 0, 2)
		}

		b.mu.Lock()
		ticks := b.breakTicks
		b.mu.Unlock()

		if ticks == 0 {
			b.mu.Lock()
			mineSwing = b.breakAnimation
			b.mu.Unlock()
			actions = append(actions,
				protocol.PlayerBlockAction{Action: int32(protocol.PlayerActionStartBreak), BlockPos: *bt, Face: btFace},
			)
		} else {
			actions = append(actions,
				protocol.PlayerBlockAction{Action: int32(protocol.PlayerActionContinueDestroyBlock), BlockPos: *bt, Face: btFace},
			)
		}

		maxTicks := b.getBreakDuration()

		if ticks >= maxTicks {
			actions = append(actions,
				protocol.PlayerBlockAction{Action: int32(protocol.PlayerActionPredictDestroyBlock), BlockPos: *bt, Face: btFace},
			)
			_ = c.WritePacket(&packet.InventoryTransaction{
				TransactionData: &protocol.UseItemTransactionData{
					ActionType:    protocol.UseItemActionBreakBlock,
					BlockPosition: *bt,
					BlockFace:     btFace,
					HotBarSlot:    int32(b.hotbarSlot),
					HeldItem:      b.heldItem,
				},
			})
			if b.gameMode == 1 || b.gameMode == 4 {
				_ = c.WritePacket(&packet.PlayerAction{
					EntityRuntimeID: b.entityID,
					ActionType:      protocol.PlayerActionCreativePlayerDestroyBlock,
					BlockPosition:   *bt,
					ResultPosition:  *bt,
					BlockFace:       btFace,
				})
			}
			b.mu.Lock()
			if b.breakRepeat {
				b.breakTicks = 0
			} else {
				if b.mineActive {
					if b.mineAttempts != nil {
						b.mineAttempts[*bt]++
					}
					if b.mineRecentBroken != nil {
						b.mineRecentBroken[*bt] = b.tick
					}
					b.mineLastFinishedPos = *bt
					b.mineLastFinishedTick = b.tick
					b.mineCurrent = nil
					b.mineCount++
					if b.mineCount%25 == 0 || len(b.mineQueue) == 0 {
						log.Printf("bebot: mined %d/%d blocks\n", b.mineCount, b.mineTotal)
					}
				}
				b.breakTarget = nil
				b.breakTicks = 0
			}
			b.mu.Unlock()
		} else {
			b.mu.Lock()
			b.breakTicks++
			b.mu.Unlock()
		}
	}
	if len(actions) > 0 {
		auth.InputData.Set(packet.InputFlagPerformBlockActions)
		auth.BlockActions = protocol.Option(actions)
	}
	_ = c.WritePacket(&auth)
	if mineSwing || (bt != nil && b.mineActive && b.breakTicks%4 == 0) {
		_ = c.WritePacket(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: b.entityID, SwingSource: packet.AnimateSwingSourceMine})
		if mineSwing {
			log.Printf("bebot: break input action=%d target=%d %d %d\n", protocol.PlayerActionStartBreak, bt.X(), bt.Y(), bt.Z())
		}
	}
	if sneakStart || sneakStop {
		// Debug: log.Printf("bebot: sent sneak %s tick=%d desired=%v target=%v up=%v moveZ=%v delta=%v flags=%s\n",
		// 	map[bool]string{true: "start", false: "stop"}[sneakStart],
		// 	b.tick, desiredSneaking, targetPlayer.sneaking, flags.Load(packet.InputFlagUp), moveZ, auth.Delta, inputFlagsValue(flags))
	}
	b.sentSneaking = desiredSneaking
	if sneakStart {
		// Bedrock tracks the sneak state both from PlayerAuthInput flags and
		// from PlayerAction start_sneaking/stop_sneaking; a real client emits
		// the PlayerAction edge too. It is the only path BDS applies without
		// requiring the player to move.
		_ = c.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStartSneak, BlockFace: -1})
	}
	if sneakStop {
		_ = c.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStopSneak, BlockFace: -1})
	}
	if swimStart {
		_ = c.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStartSwimming, BlockFace: -1})
	}
	if swimStop {
		_ = c.WritePacket(&packet.PlayerAction{EntityRuntimeID: b.entityID, ActionType: protocol.PlayerActionStopSwimming, BlockFace: -1})
	}
	if b.mode == modeMimic {
		if targetPlayer.sneaking != b.mimicSneaking {
			// Debug: log.Printf("bebot: mimic target=%q runtime=%d sneaking=%v\n", targetPlayer.name, targetPlayer.id, targetPlayer.sneaking)
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
	if b.emoteRepeat && b.emoteID != "" && b.tick >= b.emoteNextTick {
		_ = c.WritePacket(&packet.Emote{EntityRuntimeID: b.entityID, EmoteID: b.emoteID, EmoteLength: b.emoteLength, Flags: 0})
		b.emoteNextTick = b.tick + uint64(b.emoteLength)
	}
	b.last = b.pos
}

func inputFlagsValue(f protocol.InputFlags) string {
	ids := make([]int, 0, 8)
	for i := 0; i < f.Len(); i++ {
		if f.Load(i) {
			ids = append(ids, i)
		}
	}
	return fmt.Sprintf("%v", ids)
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
	// Commands run inside the packet loop; run them on their own goroutine so
	// commands that wait for a server response (transfer, drop) do not block
	// packet processing.
	go b.command(text)
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
	go func() {
		defer func() {
			b.mu.Lock()
			b.whisperTarget = ""
			b.mu.Unlock()
		}()
		b.command(message)
	}()
}

func (b *Bot) runCommand(cmd string) {
	b.mu.Lock()
	c := b.conn
	viaChat := b.cfg.CommandsViaChat != nil && *b.cfg.CommandsViaChat
	b.mu.Unlock()
	if c == nil {
		log.Println("bebot: not connected; cannot run command")
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
		log.Println("bebot: command failed:", explainError(err))
		return
	}
	if err := c.Flush(); err != nil {
		log.Println("bebot: command flush failed:", explainError(err))
		return
	}
	log.Println("bebot: executed command:", cmd)
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
		log.Println("bebot: whisper failed:", explainError(err))
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
	case "stopkill":
		b.mu.Lock()
		b.killTarget = ""
		b.mu.Unlock()
		b.reply("Stopped killing.")
	case "kill":
		if len(p) < 3 {
			b.reply("Usage: kill <target> <cps>")
			return
		}
		targetName := strings.ToLower(p[1])
		cps, err := strconv.Atoi(p[2])
		if err != nil || cps <= 0 || cps > 100 {
			b.reply("Invalid CPS (must be 1-100).")
			return
		}
		b.mu.Lock()
		pEntry, ok := b.players[targetName]
		if !ok || !pEntry.online || pEntry.id == 0 {
			b.mu.Unlock()
			b.reply("Cannot find target nearby.")
			return
		}
		b.killTarget = targetName
		b.mu.Unlock()
		b.reply(fmt.Sprintf("Killing %s at %d CPS...", pEntry.name, cps))
		go b.killLoop(targetName, cps)
	case "payback":
		b.mu.Lock()
		b.payback = !b.payback
		if len(p) > 1 {
			b.payback = strings.EqualFold(p[1], "on") || strings.EqualFold(p[1], "true")
		}
		state := "off"
		if b.payback {
			state = "on"
		}
		b.mu.Unlock()
		b.reply("Payback mode is now " + state + ".")
	case "autofish":
		b.mu.Lock()
		b.autoFish = !b.autoFish
		if len(p) > 1 {
			b.autoFish = strings.EqualFold(p[1], "on") || strings.EqualFold(p[1], "true")
		}
		state := "off"
		if b.autoFish {
			state = "on"
		}
		b.mu.Unlock()
		b.reply("Auto-fish is now " + state + ".")
		if state == "on" {
			// Auto cast immediately
			b.useItemInHand()
		}
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
	case "headshake", "shake":
		if len(p) >= 2 && strings.EqualFold(p[1], "clear") {
			b.headshake = headshakeNone
			b.headshakeTicks = 0
			b.reply("Headshake cleared.")
			return
		}
		if len(p) < 2 || len(p) > 3 {
			b.reply("Usage: headshake yes|no [repeat:true|false] | headshake clear")
			return
		}
		var mode headshakeMode
		switch strings.ToLower(p[1]) {
		case "yes", "y", "nod":
			mode = headshakeYes
		case "no", "n":
			mode = headshakeNo
		default:
			b.reply("Usage: headshake yes|no [repeat:true|false] | headshake clear")
			return
		}
		repeat := false
		if len(p) == 3 {
			repeat = strings.EqualFold(p[2], "true") || strings.EqualFold(p[2], "yes") || p[2] == "1"
		}
		b.headshake = mode
		b.headshakeRepeat = repeat
		b.headshakeTicks = 0
		b.reply(fmt.Sprintf("Headshaking %s (repeat=%v).", p[1], repeat))
	case "emote":
		if len(p) >= 2 && strings.EqualFold(p[1], "clear") {
			b.emoteID = ""
			b.emoteRepeat = false
			b.reply("Emote stopped.")
			return
		}
		if len(p) < 2 || len(p) > 4 {
			b.reply("Usage: emote <uuid> [repeat:true|false] [length] | emote clear")
			return
		}
		emoteUUID := strings.ToLower(p[1])
		if _, err := uuid.Parse(emoteUUID); err != nil {
			b.reply(fmt.Sprintf("Invalid emote uuid: %s (use a UUID from the Bedrock-Emotes list)", p[1]))
			return
		}
		repeat := false
		var length uint32 = defaultEmoteLength
		for _, arg := range p[2:] {
			switch {
			case strings.EqualFold(arg, "true") || strings.EqualFold(arg, "yes") || arg == "1":
				repeat = true
			case strings.EqualFold(arg, "false") || strings.EqualFold(arg, "no") || arg == "0":
				repeat = false
			default:
				n, err := strconv.Atoi(arg)
				if err != nil || n < 1 || n > 7200 {
					b.reply(fmt.Sprintf("Invalid length: %s (expected ticks 1-7200)", arg))
					return
				}
				length = uint32(n)
			}
		}
		if b.conn != nil {
			_ = b.conn.WritePacket(&packet.Emote{EntityRuntimeID: b.entityID, EmoteID: emoteUUID, EmoteLength: length, Flags: 0})
		}
		b.emoteRepeat = repeat
		if repeat {
			b.emoteID = emoteUUID
			b.emoteLength = length
			b.emoteNextTick = b.tick + uint64(length)
			b.reply(fmt.Sprintf("Emote %s repeating (length=%d).", emoteUUID, length))
		} else {
			b.emoteID = ""
			b.reply(fmt.Sprintf("Emote %s sent (length=%d).", emoteUUID, length))
		}
	case "skin":
		b.skinCmd(p[1:])
	case "nether":
		b.netherCmd(p[1:])
	case "renderdistance":
		if len(p) < 2 {
			b.reply("Usage: renderdistance <chunks>")
			return
		}
		dist, err := strconv.Atoi(p[1])
		if err != nil || dist < 1 {
			b.reply("Invalid render distance.")
			return
		}
		b.mu.Lock()
		c := b.conn
		b.mu.Unlock()
		if c != nil {
			_ = c.WritePacket(&packet.RequestChunkRadius{ChunkRadius: int32(dist), MaxChunkRadius: uint8(dist)})
			b.reply(fmt.Sprintf("Requested chunk radius update to %d", dist))
		}
	case "sleep":
		var x, y, z int32
		if len(p) >= 4 {
			fmt.Sscanf(strings.Join(p[1:4], " "), "%d %d %d", &x, &y, &z)
		} else {
			x, y, z = int32(math.Floor(float64(b.pos.X()))), int32(math.Floor(float64(b.pos.Y()))), int32(math.Floor(float64(b.pos.Z())))
		}
		b.spawnPos = protocol.BlockPos{x, y, z}
		b.interactBlock(b.spawnPos)
		log.Printf("bebot: sleep/spawn checkpoint requested at %d %d %d\n", x, y, z)
	case "break", "breakat", "breakcoord", "mine", "mineat", "minearea", "minespace", "breakarea", "breakspace", "mineloop", "minefarm":
		continuous := strings.EqualFold(p[0], "mineloop") || strings.EqualFold(p[0], "minefarm")
		if len(p) >= 7 && len(p) <= 10 {
			var ax, ay, az, bx, by, bz int32
			if _, err := fmt.Sscanf(strings.Join(p[1:7], " "), "%d %d %d %d %d %d", &ax, &ay, &az, &bx, &by, &bz); err != nil {
				b.reply("Usage: mine <ax> <ay> <az> <bx> <by> <bz> [ticksPerBlock] [continuous: true|false] [faceAccurate: true|false]")
				return
			}
			ticks := 0
			faceAccurate := false
			boolIdx := 0
			for _, arg := range p[7:] {
				if t, err := strconv.Atoi(arg); err == nil && t >= 0 {
					ticks = t
				} else if strings.EqualFold(arg, "face") || strings.EqualFold(arg, "faceaccurate") || strings.EqualFold(arg, "accurate") {
					faceAccurate = true
				} else if bVal, err := strconv.ParseBool(arg); err == nil {
					if boolIdx == 0 && !strings.EqualFold(p[0], "mineloop") && !strings.EqualFold(p[0], "minefarm") {
						continuous = bVal
						boolIdx++
					} else {
						faceAccurate = bVal
						boolIdx++
					}
				}
			}
			b.startMiningArea(ax, ay, az, bx, by, bz, ticks, continuous, faceAccurate)
			return
		}
		if len(p) < 5 || len(p) > 6 {
			b.reply("Usage: break <x> <y> <z> <animation: true|false> [repeat: true|false] OR mine <ax> <ay> <az> <bx> <by> <bz> [ticks]")
			return
		}
		var x, y, z int32
		if _, err := fmt.Sscanf(strings.Join(p[1:4], " "), "%d %d %d", &x, &y, &z); err != nil {
			b.reply("Usage: break <x> <y> <z> <animation: true|false> [repeat: true|false]")
			return
		}
		animate, err := strconv.ParseBool(p[4])
		if err != nil {
			b.reply("Usage: break <x> <y> <z> <animation: true|false> [repeat: true|false]")
			return
		}
		repeat := false
		if len(p) == 6 {
			repeat, err = strconv.ParseBool(p[5])
			if err != nil {
				b.reply("Usage: break <x> <y> <z> <animation: true|false> [repeat: true|false]")
				return
			}
		}
		b.startBreaking(protocol.BlockPos{x, y, z}, animate, repeat)
	case "stopbreak", "cancelbreak", "abortbreak", "stopmine", "cancelmine":
		b.stopMiningArea()
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
		if len(p) < 2 {
			b.reply("Usage: count <beds|villagers|golems>")
			return
		}
		arg := strings.ToLower(p[1])
		switch arg {
		case "beds":
			log.Println("bebot: count beds is limited to decoded chunks; gophertunnel is receiving chunk packets")
		case "villagers", "villager":
			b.mu.Lock()
			count := 0
			for _, entityType := range b.entities {
				if entityType == "minecraft:villager" || entityType == "minecraft:villager_v2" {
					count++
				}
			}
			b.mu.Unlock()
			b.reply(fmt.Sprintf("There are %d villagers nearby.", count))
		case "golems", "golem", "irongolem", "irongolems":
			b.mu.Lock()
			count := 0
			for _, entityType := range b.entities {
				if entityType == "minecraft:iron_golem" {
					count++
				}
			}
			b.mu.Unlock()
			b.reply(fmt.Sprintf("There are %d iron golems nearby.", count))
		default:
			b.reply("Usage: count <beds|villagers|golems>")
		}
	case "online", "players", "list":
		b.online()
	case "nearby", "near":
		b.nearby()
	case "coords", "coord", "coordinates", "playercoords", "playerpos", "pos", "positions", "where":
		b.coords()
	case "idle", "stop":
		b.stopMiningArea()
		b.stopBreaking()
		b.mode = modeIdle
		b.followTarget = ""
		b.mimicTarget = ""
		b.lookAtTarget = ""
		b.lookAtPos = nil
		b.velocityY = 0
		if len(p) > 1 && strings.EqualFold(p[1], "reset") {
			b.idleYaw = 0
			b.idlePitch = 0
			b.reply("bebot: idle (reset).")
		} else {
			b.idleYaw = b.facingYaw
			b.idlePitch = b.facingPitch
			b.reply("bebot: idle.")
		}
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
	case "rejoin", "reconnect":
		b.reply("Reconnecting...")
		b.close()
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

func (b *Bot) skinCmd(args []string) {
	if len(args) == 0 {
		b.reply("Usage: skin <slim|normal|wide> <path> [reload] | skin pack <folder> <skin_id> [reload] | skin pack <folder> list | skin default [reload] | skin toggle override")
		return
	}
	var err error
	sc := b.cfg.Skin
	switch strings.ToLower(args[0]) {
	case "toggle":
		if len(args) < 2 || !strings.EqualFold(args[1], "override") {
			b.reply("Usage: skin toggle override")
			return
		}
		on := !overrideAppearanceOn(sc)
		sc.OverrideAppearance = &on
		b.cfg.Skin = sc
		if err := b.saveSkin(); err != nil {
			b.reply("skin toggle failed: " + err.Error())
			return
		}
		if on {
			b.reply("skin override appearance: ON (other players will see skin changes)")
		} else {
			b.reply("skin override appearance: OFF (other players will keep their cached skin)")
		}
		return
	case "default":
		sc = defaultSkinConfig
		_, err = loadSkinFromConfig(sc)
		if err != nil {
			b.reply("skin default failed: " + err.Error())
			return
		}
	case "slim", "normal", "wide":
		if len(args) < 2 {
			b.reply("Usage: skin <slim|normal|wide> <path> [reload]")
			return
		}
		armSize := uint8(protocol.ArmSizeSlim)
		if strings.EqualFold(args[0], "normal") || strings.EqualFold(args[0], "wide") {
			armSize = protocol.ArmSizeWide
		}
		_, err = buildClassicSkin(args[1], armSize)
		if err != nil {
			b.reply("skin: " + err.Error())
			return
		}
		sc.Type = strings.ToLower(args[0])
		sc.Path = args[1]
		sc.Skin = ""
	case "pack", "custom":
		if len(args) < 3 {
			b.reply("Usage: skin pack <folder> <skin_id> [reload] | skin pack <folder> list")
			return
		}
		if strings.EqualFold(args[2], "list") {
			skins, err := parseSkinPack(args[1])
			if err != nil {
				b.reply("skin: " + err.Error())
				return
			}
			if len(skins) == 0 {
				b.reply(fmt.Sprintf("No skins in %s.", args[1]))
				return
			}
			b.reply(fmt.Sprintf("Skins in %s (%d):", args[1], len(skins)))
			for i, s := range skins {
				b.reply(fmt.Sprintf("  [%d] %s  (%s)", i, s.LocalizationName, s.Geometry))
			}
			return
		}
		_, err = buildPackSkin(args[1], args[2])
		if err != nil {
			b.reply("skin: " + err.Error())
			return
		}
		sc.Type = "pack"
		sc.Path = args[1]
		sc.Skin = args[2]
	default:
		b.reply("Usage: skin <slim|normal|wide> <path> [reload] | skin pack <folder> <skin_id> [reload] | skin pack <folder> list | skin default [reload] | skin toggle override")
		return
	}
	b.cfg.Skin = sc
	if err := b.saveSkin(); err != nil {
		b.reply("skin changed but skin.json write failed: " + err.Error())
		return
	}
	reload := isReloadArg(args)
	if reload {
		b.reply(fmt.Sprintf("skin changed to type=%s path=%s skin=%s (reloading)", sc.Type, sc.Path, sc.Skin))
		b.scheduleReconnect()
	} else {
		if err := b.sendSkin(sc); err != nil {
			b.reply(fmt.Sprintf("skin saved: type=%s path=%s skin=%s (apply failed: %v)", sc.Type, sc.Path, sc.Skin, err))
			return
		}
		b.reply(fmt.Sprintf("skin changed: type=%s path=%s skin=%s", sc.Type, sc.Path, sc.Skin))
	}
}

// sendSkin builds the skin described by sc and sends a PlayerSkin packet to the
// server, updating the bot's appearance in-game without reconnecting.
func (b *Bot) sendSkin(sc skinConfig) error {
	b.mu.Lock()
	c := b.conn
	b.mu.Unlock()
	if c == nil {
		return fmt.Errorf("not connected")
	}
	skin, err := loadSkinFromConfig(sc)
	if err != nil {
		return err
	}
	skin.OverrideAppearance = overrideAppearanceOn(sc)
	return c.WritePacket(&packet.PlayerSkin{
		UUID: b.selfUUID,
		Skin: skin,
	})
}

// isReloadArg checks whether the trailing argument in the skin command is a
// truthy reload flag ("true", "yes", or "reload"). The reload argument is
// optional and must be the very last argument.
func isReloadArg(args []string) bool {
	if len(args) < 2 {
		return false
	}
	last := strings.ToLower(args[len(args)-1])
	return last == "true" || last == "yes" || last == "reload"
}

// scheduleReconnect forces a fresh connection so the newly saved skin is loaded
// via the login ClientData. BDS only applies/relays the first in-session
// PlayerSkin self-change (and rejects empty-geometry classic skins), so runtime
// PlayerSkin changes are unreliable here; a quick reconnect is the reliable way
// to show the skin to other players. The reconnect delay is reset so this
// reconnect happens immediately instead of waiting on the failure backoff.
func (b *Bot) scheduleReconnect() {
	go func() {
		time.Sleep(500 * time.Millisecond)
		b.mu.Lock()
		b.reconnectDelay = baseReconnectDelay(b.cfg)
		c := b.conn
		b.mu.Unlock()
		if c != nil {
			_ = c.Close()
		}
	}()
}

// saveSkin writes the bot's skin config to its dedicated skin.json file. The
// file holds only skin data, so nothing else is touched.
func (b *Bot) saveSkin() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, err := json.MarshalIndent(b.cfg.Skin, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(b.skinPath, data, 0644)
}

func (b *Bot) reply(message string) {
	log.Println("bebot:", message)
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
	log.Printf("bebot: online players (%d): %s\n", len(names), strings.Join(names, ", "))
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
		log.Printf("bebot: %s @ (%.1f, %.1f, %.1f), %.1fm away\n", name, p.position.X(), p.position.Y(), p.position.Z(), float32(math.Sqrt(float64(d.Dot(d)))))
	}
}

func (b *Bot) nearestPlayer(maxDist float32) (uint64, mgl32.Vec3, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var closestID uint64
	var closestPos mgl32.Vec3
	minD2 := maxDist * maxDist
	found := false
	for _, p := range b.players {
		if !p.online || p.id == 0 || p.id == b.entityID {
			continue
		}
		d := p.position.Sub(b.pos)
		d2 := d.Dot(d)
		if d2 <= minD2 {
			minD2 = d2
			closestID = p.id
			closestPos = p.position
			found = true
		}
	}
	return closestID, closestPos, found
}

func (b *Bot) nearestAttacker(maxDist float32) (uint64, mgl32.Vec3, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var closestID uint64
	var closestPos mgl32.Vec3
	minD2 := maxDist * maxDist
	found := false
	now := time.Now()
	for _, p := range b.players {
		if !p.online || p.id == 0 || p.id == b.entityID {
			continue
		}
		// Consider attackers who swung their arm within the last 1 second
		if now.Sub(p.lastSwing) > time.Second {
			continue
		}
		d := p.position.Sub(b.pos)
		d2 := d.Dot(d)
		if d2 <= minD2 {
			minD2 = d2
			closestID = p.id
			closestPos = p.position
			found = true
		}
	}
	return closestID, closestPos, found
}

func (b *Bot) attack(targetID uint64, targetPos mgl32.Vec3) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return
	}
	
	// Turn to face them visually
	lookDX := targetPos.X() - b.pos.X()
	lookDZ := targetPos.Z() - b.pos.Z()
	horizontal := math.Sqrt(float64(lookDX*lookDX + lookDZ*lookDZ))
	if horizontal > 0.001 {
		yaw := float32(math.Atan2(float64(-lookDX), float64(lookDZ)) * 180 / math.Pi)
		if yaw < 0 {
			yaw += 360
		}
		lookDY := (targetPos.Y() + 1.62) - (b.pos.Y() + 1.62) // approximate eye height
		pitch := float32(-math.Atan2(float64(lookDY), horizontal) * 180 / math.Pi)
		b.idleYaw = yaw
		b.idlePitch = pitch
	}
	
	_ = b.conn.WritePacket(&packet.InventoryTransaction{
		TransactionData: &protocol.UseItemOnEntityTransactionData{
			TargetEntityRuntimeID: targetID,
			ActionType:            protocol.UseItemOnEntityActionAttack,
			HotBarSlot:            int32(b.hotbarSlot),
			HeldItem:              b.heldItem,
			Position:              b.pos,
			ClickedPosition:       targetPos,
		},
	})
	_ = b.conn.Flush()
	
	// Send arm swing animation so everyone can see the bot hitting back
	_ = b.conn.WritePacket(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: b.entityID})
	_ = b.conn.Flush()
}

func (b *Bot) killLoop(targetName string, cps int) {
	ticker := time.NewTicker(time.Second / time.Duration(cps))
	defer ticker.Stop()
	for {
		<-ticker.C
		b.mu.Lock()
		if b.killTarget != targetName {
			b.mu.Unlock()
			return
		}
		pEntry, ok := b.players[targetName]
		if !ok || !pEntry.online || pEntry.id == 0 {
			b.killTarget = ""
			b.mu.Unlock()
			b.reply("Target lost or died.")
			return
		}
		d := pEntry.position.Sub(b.pos)
		if d.Dot(d) > 36 { // > 6 blocks away
			b.killTarget = ""
			b.mu.Unlock()
			b.reply("Target escaped (too far).")
			return
		}
		targetID := pEntry.id
		targetPos := pEntry.position
		b.mu.Unlock()
		
		b.attack(targetID, targetPos)
	}
}

func (b *Bot) useItemInHand() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return
	}
	
	_ = b.conn.WritePacket(&packet.InventoryTransaction{
		TransactionData: &protocol.UseItemTransactionData{
			ActionType:    protocol.UseItemActionClickAir,
			BlockPosition: protocol.BlockPos{},
			BlockFace:     255,
			HotBarSlot:    int32(b.hotbarSlot),
			HeldItem:      b.heldItem,
			Position:      b.pos,
			ClickedPosition: b.pos.Add(mgl32.Vec3{0, 1, 0}),
			BlockRuntimeID: 0,
		},
	})
	_ = b.conn.Flush()
	
	_ = b.conn.WritePacket(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: b.entityID})
	_ = b.conn.Flush()
}

func (b *Bot) coords() {
	log.Printf("bebot: bot @ (%.1f, %.1f, %.1f)\n", b.pos.X(), b.pos.Y(), b.pos.Z())
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

func (b *Bot) startBreaking(pos protocol.BlockPos, animate bool, repeat bool) {
	b.breakAnimation = animate
	b.breakRepeat = repeat
	if b.gameMode == 1 || b.gameMode == 4 {
		b.mu.Lock()
		c := b.conn
		entityID := b.entityID
		b.mu.Unlock()
		if c == nil {
			log.Println("bebot: cannot break block: not connected")
			return
		}
		face := determineBlockFace(b.pos.Add(mgl32.Vec3{0, 1.62, 0}), pos)
		if err := c.WritePacket(&packet.PlayerAction{
			EntityRuntimeID: entityID,
			ActionType:      protocol.PlayerActionCreativePlayerDestroyBlock,
			BlockPosition:   pos,
			ResultPosition:  pos,
			BlockFace:       face,
		}); err != nil {
			log.Println("bebot: creative break failed:", explainError(err))
		}
		if animate {
			_ = c.WritePacket(&packet.Animate{ActionType: packet.AnimateActionSwingArm, EntityRuntimeID: entityID, SwingSource: packet.AnimateSwingSourceMine})
		}
		if err := c.Flush(); err != nil {
			log.Println("bebot: creative break flush failed:", explainError(err))
		}
		// Some 1.26 servers advertise server-authoritative block breaking even
		// for Creative players. Keep the authoritative transaction queued too;
		// the creative action alone only produces the swing animation.
		b.mu.Lock()
		b.breakTarget = &pos
		b.breakTicks = 0
		b.mu.Unlock()
		log.Printf("bebot: creative break requested action=%d at %d %d %d\n", protocol.PlayerActionCreativePlayerDestroyBlock, pos.X(), pos.Y(), pos.Z())
		return
	}
	b.mu.Lock()
	b.breakTarget = &pos
	b.breakTicks = 0
	b.mu.Unlock()
	log.Printf("bebot: breaking block at %d %d %d\n", pos.X(), pos.Y(), pos.Z())
}

func (b *Bot) stopBreaking() {
	b.mu.Lock()
	if b.breakTarget == nil {
		b.mu.Unlock()
		log.Println("bebot: no block breaking operation is active")
		return
	}
	pos := *b.breakTarget
	b.abortBreakTarget = &pos
	b.breakTarget = nil
	b.breakTicks = 0
	b.mu.Unlock()

	log.Printf("bebot: stopped breaking block at %d %d %d\n", pos.X(), pos.Y(), pos.Z())
}

func (b *Bot) getBreakDuration() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mineActive && b.mineTicksReq > 0 {
		return b.mineTicksReq
	}
	if b.gameMode == 1 || b.gameMode == 4 {
		return 2
	}
	netID := b.heldItem.Stack.NetworkID
	name := b.itemNamesByID[netID]

	switch {
	case strings.Contains(name, "netherite_pickaxe"):
		return 8
	case strings.Contains(name, "diamond_pickaxe"):
		return 10
	case strings.Contains(name, "golden_pickaxe"):
		return 10
	case strings.Contains(name, "iron_pickaxe"):
		return 14
	case strings.Contains(name, "stone_pickaxe"):
		return 20
	case strings.Contains(name, "wooden_pickaxe"):
		return 35
	case strings.Contains(name, "axe"):
		return 40
	case strings.Contains(name, "shovel"):
		return 15
	default:
		// Bare hands or unadapted tool: cobblestone/stone requires 150 ticks (7.5s).
		// Use 160 ticks (8.0s) so it comfortably completes on the server without being rejected.
		return 160
	}
}

func (b *Bot) isInMineBox(pos protocol.BlockPos) bool {
	return pos.X() >= b.mineBoxMin.X() && pos.X() <= b.mineBoxMax.X() &&
		pos.Y() >= b.mineBoxMin.Y() && pos.Y() <= b.mineBoxMax.Y() &&
		pos.Z() >= b.mineBoxMin.Z() && pos.Z() <= b.mineBoxMax.Z()
}

func (b *Bot) startMiningArea(ax, ay, az, bx, by, bz int32, ticksReq int, continuous bool, faceAccurate bool) {
	minX, maxX := ax, bx
	if minX > maxX {
		minX, maxX = maxX, minX
	}
	minY, maxY := ay, by
	if minY > maxY {
		minY, maxY = maxY, minY
	}
	minZ, maxZ := az, bz
	if minZ > maxZ {
		minZ, maxZ = maxZ, minZ
	}

	total := int((maxX - minX + 1) * (maxY - minY + 1) * (maxZ - minZ + 1))
	if total <= 0 || total > 100000 {
		b.reply(fmt.Sprintf("Invalid area size: %d blocks (limit 1-100,000).", total))
		return
	}

	queue := make([]protocol.BlockPos, 0, total)
	for y := maxY; y >= minY; y-- {
		forward := true
		for x := minX; x <= maxX; x++ {
			if forward {
				for z := minZ; z <= maxZ; z++ {
					queue = append(queue, protocol.BlockPos{x, y, z})
				}
			} else {
				for z := maxZ; z >= minZ; z-- {
					queue = append(queue, protocol.BlockPos{x, y, z})
				}
			}
			forward = !forward
		}
	}

	b.mu.Lock()
	b.mineQueue = queue
	b.mineTotal = total
	b.mineCount = 0
	b.mineActive = true
	b.mineCurrent = nil
	b.mineStuckTicks = 0
	b.mineTicksReq = ticksReq
	b.mineBoxMin = protocol.BlockPos{minX, minY, minZ}
	b.mineBoxMax = protocol.BlockPos{maxX, maxY, maxZ}
	b.mineContinuous = continuous
	b.mineFaceAccurate = faceAccurate
	b.mineAttempts = make(map[protocol.BlockPos]int)
	b.mineRecentBroken = make(map[protocol.BlockPos]uint64)
	b.mu.Unlock()

	modeStr := "top-down"
	if continuous {
		modeStr = "continuous/generator mode"
	}
	faceStr := "standard face"
	if faceAccurate {
		faceStr = "face-accurate"
	}
	b.reply(fmt.Sprintf("Mining space (%d, %d, %d) to (%d, %d, %d): %d blocks queued (%s, %s).", minX, minY, minZ, maxX, maxY, maxZ, total, modeStr, faceStr))
}

func (b *Bot) stopMiningArea() {
	b.mu.Lock()
	if !b.mineActive && len(b.mineQueue) == 0 {
		b.mu.Unlock()
		return
	}
	b.mineActive = false
	b.mineContinuous = false
	b.mineFaceAccurate = false
	b.mineQueue = nil
	b.mineCurrent = nil
	b.mineStuckTicks = 0
	b.mineAttempts = nil
	b.mineRecentBroken = nil
	b.mu.Unlock()
	b.reply("Stopped area mining.")
}

func determineBlockFace(eyePos mgl32.Vec3, blockPos protocol.BlockPos) int32 {
	dx := float32(blockPos.X()) + 0.5 - eyePos.X()
	dy := float32(blockPos.Y()) + 0.5 - eyePos.Y()
	dz := float32(blockPos.Z()) + 0.5 - eyePos.Z()

	absX := float32(math.Abs(float64(dx)))
	absY := float32(math.Abs(float64(dy)))
	absZ := float32(math.Abs(float64(dz)))

	if absY >= absX && absY >= absZ {
		if dy > 0 {
			return 0 // Bottom face (-Y): player is below the block looking up
		}
		return 1 // Top face (+Y): player is above the block looking down
	} else if absX >= absZ {
		if dx > 0 {
			return 4 // West face (-X)
		}
		return 5 // East face (+X)
	} else {
		if dz > 0 {
			return 2 // North face (-Z)
		}
		return 3 // South face (+Z)
	}
}

func (b *Bot) pickNextMineBlock() *protocol.BlockPos {
	// Skip blocks that have failed to break 2 times (likely unbreakable, air, or protected)
	for len(b.mineQueue) > 0 && b.mineAttempts != nil && b.mineAttempts[b.mineQueue[0]] >= 2 {
		skipped := b.mineQueue[0]
		b.mineQueue = b.mineQueue[1:]
		log.Printf("bebot: skipping block at (%d, %d, %d) (failed 2 times, likely air or unbreakable)\n", skipped.X(), skipped.Y(), skipped.Z())
	}

	if len(b.mineQueue) == 0 {
		return nil
	}

	// Follow natural serpentine queue order (top-down, sweeping 2 rows per time as it moves)
	target := b.mineQueue[0]
	b.mineQueue = b.mineQueue[1:]
	return &target
}

func (b *Bot) startBreakingFront(animate bool, repeat bool) {
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
	b.startBreaking(protocol.BlockPos{x, y, z}, animate, repeat)
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
		"headshake yes|no [repeat:true|false] - shake the head (yes=up/down, no=left/right)",
		"headshake clear - stop headshaking",
		"emote <uuid> [repeat:true|false] [length] - send an emote (UUID from Bedrock-Emotes list); length = duration in ticks, default 60",
		"emote clear - stop repeating emote",
		"skin <slim|normal|wide> <path> [reload] - change to a classic skin PNG",
		"skin pack <folder> <skin_id> [reload] - change to a skin from a skin pack (skins.json)",
		"skin pack <folder> list - list skins in a skin pack",
		"skin default [reload] - revert to default slim skin",
		"skin toggle override - toggle whether skin changes override other players' view (default: on)",
		"idle / stop - stop movement (keeps current facing)",
		"idle reset - idle and reset facing to default",
		"break <x y z> <animation: true|false> - break a block",
		"mine <ax ay az> <bx by bz> [ticks] - mine all blocks in a space (top-down)",
		"stopmine / stopbreak - stop mining",
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
		"payback [on|off] - toggle payback mode (attacks back if hurt)",
		"autofish [on|off] - toggle auto-fishing",
		"kill <target> <cps> - rapidly attack a player until they die",
		"stopkill - stop the current kill command",
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
	b.mu.Lock()
	entityID := b.entityID
	b.mu.Unlock()
	_ = c.WritePacket(&packet.Interact{ActionType: packet.InteractActionOpenInventory, TargetEntityRuntimeID: entityID})
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
	b.invMu.Lock()
	defer b.invMu.Unlock()
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
	b.invMu.Lock()
	defer b.invMu.Unlock()
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
	// one slot does not fail the rest.
	b.openInventory(c)
	defer b.closeInventory(c)
	var actions []protocol.StackRequestAction
	var validDrops []dropRequest
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
		actions = append(actions, take, dropAction)
		validDrops = append(validDrops, drop)
	}

	if len(actions) == 0 {
		b.reply("No valid items to drop.")
		return
	}

	ok, status := b.sendItemStackRequest(c, actions)
	if !ok {
		b.reply(fmt.Sprintf("Drop request failed: %s", stackStatusName(status)))
		return
	}

	// Update local inventory state
	b.mu.Lock()
	for _, drop := range validDrops {
		count := drop.count
		if count > 255 {
			count = 255
		}
		if b.inventory[drop.slot].Stack.Count > count {
			b.inventory[drop.slot].Stack.Count -= count
		} else {
			b.inventory[drop.slot] = protocol.ItemInstance{}
		}
		if byte(drop.slot) == b.hotbarSlot {
			b.heldItem = b.inventory[drop.slot]
		}
	}
	b.mu.Unlock()
	b.sendHeldItem(c)

	b.reply(fmt.Sprintf("Dropped items from %d slot(s).", len(validDrops)))
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
		log.Println("bebot: held-item update failed:", explainError(err))
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
	_ = b.conn.Flush()
}
func (b *Bot) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil {
		// Give the server a proper client disconnect before closing RakNet.
		_ = b.conn.WritePacket(&packet.Disconnect{HideDisconnectionScreen: true, Message: "Client shutting down"})
		_ = b.conn.Flush()
		time.Sleep(100 * time.Millisecond)
		_ = b.conn.Close()
		b.conn = nil
	}
}
