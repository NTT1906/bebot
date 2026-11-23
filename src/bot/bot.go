package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"bebot/src/config"
	"bebot/src/waypoint"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft"
	mauth "github.com/sandertv/gophertunnel/minecraft/auth"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
	"golang.org/x/oauth2"
)

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
// emote lasts and the interval at which a repeating emote is resent.
const defaultEmoteLength = 60

// stackRequest tracks an in-flight ItemStackRequest so the ItemStackResponse
// handler can deliver the matching status back to the sender.
type stackRequest struct {
	id   int32
	resp chan uint8
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

type commandResult struct {
	key    string
	params []string
	text   string
}

// Bot is the core player bot instance.
type Bot struct {
	cfg                          config.Config
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
	waypoints                    map[string]waypoint.Waypoint
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
	xblCache                     *mauth.XBLTokenCache
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
	mineRepeat                   bool
	mineFaceAccurate             bool
	offline                      bool
	botName                      string
	offlineUUID                  uuid.UUID
	local                        bool
	itemNamesByID                map[int32]string
	mineAttempts                 map[protocol.BlockPos]int
	mineAirRuntimeIDs            map[uint32]bool
	knownAirBlocks               map[protocol.BlockPos]bool
	mineFastBreak                bool
	hasteLevel                   int32
	mineTimeoutSec               float64
	mineBlockStart               time.Time
	mineInitialDone              bool
	mineLastFinishedPos          protocol.BlockPos
	mineLastFinishedTick         uint64
	mineLastWasBroken            bool
	mineWaitingForBlocks         bool
	mineLearnActive              bool
	mineLearnPos                 protocol.BlockPos
	lastPacketTime               atomic.Int64
	stallTicks                   int
}

// ServerStallThreshold returns the configured duration without server packets before considering the server stalled.
func (b *Bot) ServerStallThreshold() time.Duration {
	return b.cfg.ServerStallThreshold()
}

// StallThrottleTicks returns the number of 50ms client ticks between throttled packets during stalls.
func (b *Bot) StallThrottleTicks() int {
	ticks := int(b.ServerStallThreshold() / (50 * time.Millisecond))
	if ticks < 2 {
		return 2
	}
	return ticks
}

// isServerStalled returns true if no packets have been received from the server within the configured stall threshold.
func (b *Bot) isServerStalled() bool {
	last := b.lastPacketTime.Load()
	if last == 0 {
		return false
	}
	return time.Since(time.Unix(0, last)) > b.ServerStallThreshold()
}

// markPacketReceived records the current timestamp as the latest server packet arrival.
func (b *Bot) markPacketReceived() {
	b.lastPacketTime.Store(time.Now().UnixNano())
}

// Options contains the configuration and initial state needed to create a Bot.
type Options struct {
	Config      config.Config
	ConfigPath  string
	SkinPath    string
	Source      oauth2.TokenSource
	XBLCache    *mauth.XBLTokenCache
	Offline     bool
	BotName     string
	OfflineUUID uuid.UUID
	Local       bool
	Debug       bool
}

var tracePackets bool
var tracedPackets atomic.Uint64

// SetTracePackets enables or disables low-level packet tracing.
func SetTracePackets(enabled bool) {
	tracePackets = enabled
}

// New creates and initializes a Bot instance.
func New(opts Options) *Bot {
	tracePackets = opts.Debug
	mineTimeout := opts.Config.Mine.TimeoutSec
	if mineTimeout <= 0 {
		mineTimeout = 3.0
	}

	return &Bot{
		cfg:               opts.Config,
		source:            opts.Source,
		offline:           opts.Offline,
		botName:           opts.BotName,
		offlineUUID:       opts.OfflineUUID,
		local:             opts.Local,
		players:           make(map[string]player),
		entities:          make(map[int64]string),
		mode:              modeIdle,
		waypoints:         waypoint.LoadWaypoints(waypoint.DefaultWaypointsFile),
		configPath:        opts.ConfigPath,
		skinPath:          opts.SkinPath,
		xblCache:          opts.XBLCache,
		itemNamesByID:     make(map[int32]string),
		mineAttempts:      make(map[protocol.BlockPos]int),
		mineAirRuntimeIDs: map[uint32]bool{0: true},
		knownAirBlocks:    make(map[protocol.BlockPos]bool),
		mineTimeoutSec:    mineTimeout,
	}
}

// Run starts the main bot connect and reconnect loop.
func (b *Bot) Run(ctx context.Context) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("bebot: FATAL bot panic: %v\n%s\n", recovered, debug.Stack())
			b.Close()
		}
	}()
	b.mu.Lock()
	b.reconnectDelay = config.BaseReconnectDelay(b.cfg)
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

// Close disconnects and stops the bot connection cleanly.
func (b *Bot) Close() {
	b.mu.Lock()
	c := b.conn
	b.conn = nil
	b.quitting = true
	b.mu.Unlock()

	if c != nil {
		_ = c.WritePacket(&packet.Disconnect{HideDisconnectionScreen: true, Message: "Client shutting down"})
		_ = c.Flush()
		_ = c.Close()
		runtime.GC()
	}
}

// TracePacket logs packet metadata when debug tracing is enabled.
func TracePacket(header packet.Header, payload []byte, src, dst net.Addr) {
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

// ExplainError formats network and Minecraft protocol errors into human-readable strings.
func ExplainError(err error) string {
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
