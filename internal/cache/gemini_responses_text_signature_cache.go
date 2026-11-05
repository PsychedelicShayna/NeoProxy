package cache

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// GeminiResponsesTextSignatureCacheTTL limits how long late text thought
	// signatures stay available for the next OpenAI Responses turn.
	GeminiResponsesTextSignatureCacheTTL = 1 * time.Hour

	// GeminiResponsesTextSignatureCacheMaxEntries bounds process memory. The
	// oldest entries are evicted first.
	GeminiResponsesTextSignatureCacheMaxEntries = 10240

	geminiResponsesTextSignatureCacheEvictBatchSize = 128
	geminiResponsesTextSignatureMaxItemsPerEntry    = 4096
	geminiResponsesTextSignatureMaxBytesPerEntry    = 16 << 20
	geminiResponsesTextSignatureMinLen              = 16
	geminiResponsesTextSignatureMaxSerializedBytes  = 24 << 20
)

type geminiResponsesTextSignatureEntry struct {
	Items     [][]byte
	Timestamp time.Time
}

var (
	geminiResponsesTextSignatureMu      sync.Mutex
	geminiResponsesTextSignatureEntries = make(map[string]geminiResponsesTextSignatureEntry)
)

type geminiResponsesTextSignatureKVClient interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSet(ctx context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error)
	KVExpire(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

var currentGeminiResponsesTextSignatureKVClient = func() (geminiResponsesTextSignatureKVClient, bool, error) {
	return homekv.CurrentKVClient()
}

// CacheGeminiResponsesTextSignatureItems stores the thought-signature carriers
// that Gemini attached after a text part, keyed by model and Responses message
// ID, so the next stateless Responses turn can restore them. Items are
// normalized to the minimal thought_signature shape.
func CacheGeminiResponsesTextSignatureItems(modelName, sessionKey string, items [][]byte) bool {
	key := geminiResponsesTextSignatureCacheKey(modelName, sessionKey)
	if key == "" {
		return false
	}
	normalized, ok := normalizeGeminiResponsesTextSignatureItems(items)
	if !ok {
		return false
	}
	if client, homeMode, errClient := currentGeminiResponsesTextSignatureKVClient(); homeMode {
		if errClient != nil {
			log.Errorf("home kv best-effort gemini responses text signature set failed prefix=cpa:gemini:*: %v", errClient)
			return false
		}
		raw, errMarshal := json.Marshal(normalized)
		if errMarshal != nil {
			log.Errorf("home kv best-effort gemini responses text signature set failed prefix=cpa:gemini:*: %v", errMarshal)
			return false
		}
		written, errSet := client.KVSet(context.Background(), geminiResponsesTextSignatureKVKey(modelName, sessionKey), raw, homekv.KVSetOptions{EX: GeminiResponsesTextSignatureCacheTTL})
		if errSet != nil {
			log.Errorf("home kv best-effort gemini responses text signature set failed prefix=cpa:gemini:*: %v", errSet)
			return false
		}
		return written
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	geminiResponsesTextSignatureMu.Lock()
	defer geminiResponsesTextSignatureMu.Unlock()
	geminiResponsesTextSignatureEntries[key] = geminiResponsesTextSignatureEntry{Items: normalized, Timestamp: time.Now()}
	if len(geminiResponsesTextSignatureEntries) > GeminiResponsesTextSignatureCacheMaxEntries {
		evictOldestGeminiResponsesTextSignatureEntries(geminiResponsesTextSignatureCacheEvictBatchSize)
	}
	return true
}

// GetGeminiResponsesTextSignatureItems returns the normalized carriers stored
// for a model and Responses message ID.
func GetGeminiResponsesTextSignatureItems(modelName, sessionKey string) ([][]byte, bool) {
	key := geminiResponsesTextSignatureCacheKey(modelName, sessionKey)
	if key == "" {
		return nil, false
	}
	if client, homeMode, errClient := currentGeminiResponsesTextSignatureKVClient(); homeMode {
		if errClient != nil {
			return nil, false
		}
		kvKey := geminiResponsesTextSignatureKVKey(modelName, sessionKey)
		raw, found, errGet := client.KVGet(context.Background(), kvKey)
		if errGet != nil || !found || len(raw) > geminiResponsesTextSignatureMaxSerializedBytes {
			return nil, false
		}
		var items [][]byte
		if errUnmarshal := json.Unmarshal(raw, &items); errUnmarshal != nil || len(items) == 0 {
			return nil, false
		}
		normalized, ok := normalizeGeminiResponsesTextSignatureItems(items)
		if !ok || len(normalized) != len(items) {
			return nil, false
		}
		if _, errExpire := client.KVExpire(context.Background(), kvKey, GeminiResponsesTextSignatureCacheTTL); errExpire != nil {
			return nil, false
		}
		return normalized, true
	}

	cacheCleanupOnce.Do(startCacheCleanup)
	now := time.Now()
	geminiResponsesTextSignatureMu.Lock()
	defer geminiResponsesTextSignatureMu.Unlock()
	entry, ok := geminiResponsesTextSignatureEntries[key]
	if !ok {
		return nil, false
	}
	if now.Sub(entry.Timestamp) > GeminiResponsesTextSignatureCacheTTL {
		delete(geminiResponsesTextSignatureEntries, key)
		return nil, false
	}
	entry.Timestamp = now
	geminiResponsesTextSignatureEntries[key] = entry
	return cloneGeminiResponsesTextSignatureItems(entry.Items), true
}

// ClearGeminiResponsesTextSignatureCache drops every process-local entry.
func ClearGeminiResponsesTextSignatureCache() {
	geminiResponsesTextSignatureMu.Lock()
	geminiResponsesTextSignatureEntries = make(map[string]geminiResponsesTextSignatureEntry)
	geminiResponsesTextSignatureMu.Unlock()
}

func geminiResponsesTextSignatureCacheKey(modelName, sessionKey string) string {
	modelName = strings.TrimSpace(modelName)
	sessionKey = strings.TrimSpace(sessionKey)
	if modelName == "" || sessionKey == "" {
		return ""
	}
	return strings.Join([]string{"gemini-responses-text-signature", modelName, sessionKey}, "\x00")
}

func geminiResponsesTextSignatureKVKey(modelName, sessionKey string) string {
	return "cpa:gemini:responses-text-signature:" + homekv.HashKeyPart(strings.TrimSpace(modelName)) + ":" + homekv.HashKeyPart(strings.TrimSpace(sessionKey))
}

func normalizeGeminiResponsesTextSignatureItems(items [][]byte) ([][]byte, bool) {
	if len(items) > geminiResponsesTextSignatureMaxItemsPerEntry {
		return nil, false
	}
	normalized := make([][]byte, 0, len(items))
	totalBytes := 0
	for _, item := range items {
		normalizedItem, ok := normalizeGeminiResponsesTextSignatureItem(gjson.ParseBytes(item))
		if !ok {
			continue
		}
		totalBytes += len(normalizedItem)
		if totalBytes > geminiResponsesTextSignatureMaxBytesPerEntry {
			return nil, false
		}
		normalized = append(normalized, normalizedItem)
	}
	return normalized, len(normalized) > 0
}

func normalizeGeminiResponsesTextSignatureItem(item gjson.Result) ([]byte, bool) {
	if strings.TrimSpace(item.Get("type").String()) != "thought_signature" {
		return nil, false
	}
	signature := strings.TrimSpace(item.Get("thoughtSignature").String())
	if signature == "" || signature == "skip_thought_signature_validator" || len(signature) < geminiResponsesTextSignatureMinLen {
		return nil, false
	}
	normalized := []byte(`{"type":"thought_signature"}`)
	normalized, _ = sjson.SetBytes(normalized, "thoughtSignature", signature)
	if targetKind := strings.TrimSpace(item.Get("targetKind").String()); targetKind == "text" || targetKind == "thought" {
		normalized, _ = sjson.SetBytes(normalized, "targetKind", targetKind)
	}
	if targetHash := strings.TrimSpace(item.Get("targetHash").String()); targetHash != "" {
		normalized, _ = sjson.SetBytes(normalized, "targetHash", targetHash)
	}
	return normalized, true
}

func cloneGeminiResponsesTextSignatureItems(items [][]byte) [][]byte {
	cloned := make([][]byte, 0, len(items))
	for _, item := range items {
		cloned = append(cloned, append([]byte(nil), item...))
	}
	return cloned
}

func evictOldestGeminiResponsesTextSignatureEntries(count int) {
	for ; count > 0 && len(geminiResponsesTextSignatureEntries) > 0; count-- {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range geminiResponsesTextSignatureEntries {
			if oldestKey == "" || entry.Timestamp.Before(oldest) {
				oldestKey, oldest = key, entry.Timestamp
			}
		}
		delete(geminiResponsesTextSignatureEntries, oldestKey)
	}
}

func purgeExpiredGeminiResponsesTextSignatureCache(now time.Time) {
	geminiResponsesTextSignatureMu.Lock()
	for key, entry := range geminiResponsesTextSignatureEntries {
		if now.Sub(entry.Timestamp) > GeminiResponsesTextSignatureCacheTTL {
			delete(geminiResponsesTextSignatureEntries, key)
		}
	}
	geminiResponsesTextSignatureMu.Unlock()
}
