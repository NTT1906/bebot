package auth

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/df-mc/go-xsapi/v2/xal/sisu"
	"github.com/df-mc/go-xsapi/v2/xal/xasd"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/auth"
	"golang.org/x/oauth2"
)

// XSTSDiskCache holds cached Xbox Live device and session tokens.
type XSTSDiskCache struct {
	Snapshot    *sisu.Snapshot `json:"snapshot"`
	DeviceToken *xasd.Token    `json:"device_token"`
	PrivateKey  []byte         `json:"private_key"`
}

// LoadXBLCache loads cached Xbox Live session tokens from disk.
func LoadXBLCache(path string, source oauth2.TokenSource) *auth.XBLTokenCache {
	if data, err := os.ReadFile(path); err == nil {
		var cache XSTSDiskCache
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

// SaveXBLCache saves the Xbox Live token cache to disk.
func SaveXBLCache(path string, cache *auth.XBLTokenCache) {
	if cache == nil {
		return
	}
	session := cache.Session()
	if session == nil {
		return
	}
	deviceToken, _ := cache.Device().DeviceToken(context.Background())
	keyBytes, _ := x509.MarshalECPrivateKey(cache.Device().ProofKey())

	dc := XSTSDiskCache{
		Snapshot:    session.Snapshot(),
		DeviceToken: deviceToken,
		PrivateKey:  keyBytes,
	}
	if data, err := json.Marshal(dc); err == nil {
		_ = os.WriteFile(path, data, 0644)
	}
}

// OfflineUUIDFile is the default filename for persisting offline UUIDs.
const OfflineUUIDFile = "offline_uuid.json"

// LoadOrGenerateOfflineUUID loads an existing offline UUID for botName or generates and saves a new one.
func LoadOrGenerateOfflineUUID(path string, botName string, renew bool) uuid.UUID {
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

// PersistedTokenSource keeps an OAuth token on disk and updates it automatically on refresh.
type PersistedTokenSource struct {
	source oauth2.TokenSource
	path   string
	mu     sync.Mutex
}

// Token returns a valid OAuth token, renewing and saving it to disk if necessary.
func (s *PersistedTokenSource) Token() (*oauth2.Token, error) {
	token, err := s.source.Token()
	if err != nil {
		return nil, err
	}
	if err := s.save(token); err != nil {
		log.Println("bebot: warning: could not save refreshed session:", err)
	}
	return token, nil
}

func (s *PersistedTokenSource) save(token *oauth2.Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

// LoadAuthSource keeps the OAuth refresh token on disk. The access token is
// short-lived, but RefreshTokenSource renews it automatically using the saved
// refresh token, so device authentication is only needed once.
func LoadAuthSource(path string) (oauth2.TokenSource, error) {
	path = filepath.Clean(path)
	if data, err := os.ReadFile(path); err == nil {
		var token oauth2.Token
		if json.Unmarshal(data, &token) == nil && token.RefreshToken != "" {
			source := &PersistedTokenSource{source: auth.RefreshTokenSource(&token), path: path}
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
	source := &PersistedTokenSource{source: auth.RefreshTokenSource(token), path: path}
	if err := source.save(token); err != nil {
		return nil, err
	}
	log.Println("bebot: Microsoft session saved to", path)
	return source, nil
}
