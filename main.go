package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"

	bauth "bebot/src/auth"
	"bebot/src/bot"
	"bebot/src/config"
	"bebot/src/logger"

	"github.com/google/uuid"
	mauth "github.com/sandertv/gophertunnel/minecraft/auth"
	"golang.org/x/oauth2"
)

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
	tokenCacheFlag := flag.String("token-cache", "data/token_cache.json", "path to Microsoft OAuth token cache")
	authCacheFlag := flag.String("auth-cache", "", "alias for -token-cache")
	xstsCacheFlag := flag.String("xsts-cache", "data/xsts_cache.json", "path to Xbox Live XSTS token cache")
	debugFlag := flag.Bool("debug", false, "print packet-level handshake diagnostics")
	skinFile := flag.String("skin", "", "bot skin configuration (.json) or direct skin image (.png)")
	localFlag := flag.Bool("local", false, "connect to a local/LAN singleplayer world (127.0.0.1)")
	hostFlag := flag.String("host", "", "override server host (e.g. 127.0.0.1)")
	portFlag := flag.Int("port", 0, "override server port (e.g. 19132)")
	offlineFlag := flag.Bool("offline", false, "connect without Microsoft authentication (LAN mode)")
	botNameFlag := flag.String("name", "Bebot", "bot display name (used in offline/LAN mode)")
	renewFlag := flag.Bool("renew", false, "generate a fresh identity/UUID for offline/LAN mode instead of reusing saved UUID")
	offlineUUIDFlag := flag.String("offline-uuid-file", bauth.OfflineUUIDFile, "path to offline UUID cache file")
	pprofFlag := flag.String("pprof", "", "bind address for net/http/pprof diagnostics (e.g. :6060 or localhost:6060)")
	blockPaletteFlag := flag.String("block-palette", "", "path to custom block palette file (.nbt or .json)")
	flag.Parse()

	if *pprofFlag != "" {
		go func() {
			log.Printf("bebot: starting pprof diagnostics server on %s\n", *pprofFlag)
			if err := http.ListenAndServe(*pprofFlag, nil); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("bebot: pprof server error: %v\n", err)
			}
		}()
	}

	// Initialize file logger
	fileLogger := logger.NewDailyLogger("logs")
	defer fileLogger.Close()
	logWriter := io.MultiWriter(os.Stderr, fileLogger)
	log.SetOutput(logWriter)

	resolvedConfig := *configPath
	if !isFlagPassed("config") {
		if _, err := os.Stat("config.toml"); err == nil {
			resolvedConfig = "config.toml"
		}
	}
	cfg, err := config.LoadConfig(resolvedConfig)
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
	resolvedTokenCache := *tokenCacheFlag
	if isFlagPassed("auth-cache") && *authCacheFlag != "" {
		resolvedTokenCache = *authCacheFlag
	} else if !isFlagPassed("token-cache") {
		if _, err := os.Stat("data/token_cache.json"); os.IsNotExist(err) {
			if _, err := os.Stat("token_cache.json"); err == nil {
				resolvedTokenCache = "token_cache.json"
			}
		}
	}

	resolvedXSTSCache := *xstsCacheFlag
	if !isFlagPassed("xsts-cache") {
		if _, err := os.Stat("data/xsts_cache.json"); os.IsNotExist(err) {
			if _, err := os.Stat("xsts_cache.json"); err == nil {
				resolvedXSTSCache = "xsts_cache.json"
			}
		}
	}

	rawSkinTarget := *skinFile
	if !isFlagPassed("skin") || rawSkinTarget == "" {
		if cfg.SkinFile != "" {
			rawSkinTarget = cfg.SkinFile
		} else {
			rawSkinTarget = "data/skin.json"
		}
	}

	skinCfg, resolvedSavePath, err := config.ResolveSkinConfig(rawSkinTarget, "data/skin.json", cfg.Skin)
	if err != nil {
		log.Printf("bebot: skin: %v (using fallback)\n", err)
	}
	cfg.Skin = skinCfg
	log.Printf("bebot: skin config: type=%s path=%s skin=%s override=%v (save file: %s)\n",
		cfg.Skin.Type, cfg.Skin.Path, cfg.Skin.Skin, config.OverrideAppearanceOn(cfg.Skin), resolvedSavePath)

	resolvedOfflineUUID := *offlineUUIDFlag
	if !isFlagPassed("offline-uuid-file") {
		if _, err := os.Stat(bauth.OfflineUUIDFile); os.IsNotExist(err) {
			if _, err := os.Stat("offline_uuid.json"); err == nil {
				resolvedOfflineUUID = "offline_uuid.json"
			}
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var source oauth2.TokenSource
	var xblCache *mauth.XBLTokenCache
	var offlineUUID uuid.UUID
	if !*offlineFlag {
		var err error
		source, err = bauth.LoadAuthSource(resolvedTokenCache)
		if err != nil {
			log.Println("bebot: authentication failed:", err)
			return
		}
		xblCache = bauth.LoadXBLCache(resolvedXSTSCache, source)
	} else {
		offlineUUID = bauth.LoadOrGenerateOfflineUUID(resolvedOfflineUUID, *botNameFlag, *renewFlag)
		log.Printf("bebot: running in offline/LAN mode as %q [UUID: %s] (no Microsoft authentication)\n", *botNameFlag, offlineUUID.String())
	}

	b := bot.New(bot.Options{
		Config:           cfg,
		ConfigPath:       *configPath,
		SkinPath:         resolvedSavePath,
		Source:           source,
		XBLCache:         xblCache,
		XSTSCachePath:    resolvedXSTSCache,
		Offline:          *offlineFlag,
		BotName:          *botNameFlag,
		OfflineUUID:      offlineUUID,
		OfflineUUIDPath:  resolvedOfflineUUID,
		Local:            *localFlag,
		Debug:            *debugFlag,
		BlockPalettePath: *blockPaletteFlag,
	})

	go b.Run(ctx)

	go func() {
		s := bufio.NewScanner(os.Stdin)
		for s.Scan() {
			line := strings.TrimSpace(s.Text())
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "/") {
				b.RunCommand(line)
				continue
			}
			b.ConsoleCommand(line)
		}
	}()

	<-ctx.Done()
	b.Close()
}
