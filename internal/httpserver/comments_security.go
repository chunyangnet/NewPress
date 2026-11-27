package httpserver

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	commentFormTokenVersion = "v1"
	commentRateWindow       = 10 * time.Minute
	commentRateKeyLimit     = 4096
)

type commentGuard struct {
	key  [32]byte
	rate commentRateLimiter
}

type commentRateLimiter struct {
	mu      sync.Mutex
	entries map[string]commentRateEntry
}

type commentRateEntry struct {
	Started  time.Time
	LastSeen time.Time
	Count    int
}

func newCommentGuard() *commentGuard {
	guard := &commentGuard{}
	if _, err := rand.Read(guard.key[:]); err != nil {
		guard.key = sha256.Sum256([]byte(strconv.FormatInt(time.Now().UnixNano(), 36)))
	}
	guard.rate.entries = make(map[string]commentRateEntry)
	return guard
}

func (guard *commentGuard) formToken(postID int64, now time.Time) string {
	if guard == nil || postID <= 0 {
		return ""
	}
	// Tokens are deterministic within a UTC day, remain valid for at least one
	// week and therefore tolerate short public-page caches and browser history.
	expires := now.UTC().Truncate(24 * time.Hour).Add(8 * 24 * time.Hour).Unix()
	payload := commentTokenPayload(postID, expires)
	signature := guard.sign(payload)
	return strings.Join([]string{
		commentFormTokenVersion,
		strconv.FormatInt(postID, 10),
		strconv.FormatInt(expires, 10),
		base64.RawURLEncoding.EncodeToString(signature),
	}, ".")
}

func (guard *commentGuard) verifyFormToken(token string, postID int64, now time.Time) bool {
	if guard == nil || token == "" || postID <= 0 || len(token) > 256 {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != commentFormTokenVersion {
		return false
	}
	tokenPostID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || tokenPostID != postID {
		return false
	}
	expires, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || expires < now.UTC().Unix() {
		return false
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(provided) != sha256.Size {
		return false
	}
	expected := guard.sign(commentTokenPayload(postID, expires))
	return subtle.ConstantTimeCompare(provided, expected) == 1
}

func (guard *commentGuard) sourceDigest(source string) string {
	if guard == nil {
		return ""
	}
	if source == "" {
		source = "unknown"
	}
	mac := hmac.New(sha256.New, guard.key[:])
	_, _ = mac.Write([]byte("newpress-comment-source\x00"))
	_, _ = mac.Write([]byte(source))
	return hex.EncodeToString(mac.Sum(nil))
}

func (guard *commentGuard) allow(sourceDigest string, postID int64, limit int, now time.Time) (bool, time.Duration) {
	if guard == nil || sourceDigest == "" || postID <= 0 || limit <= 0 {
		return false, commentRateWindow
	}
	key := sourceDigest + ":" + strconv.FormatInt(postID, 10)
	return guard.rate.allow(key, limit, now.UTC())
}

func (guard *commentGuard) sign(payload string) []byte {
	mac := hmac.New(sha256.New, guard.key[:])
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func (limiter *commentRateLimiter) allow(key string, limit int, now time.Time) (bool, time.Duration) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.entries == nil {
		limiter.entries = make(map[string]commentRateEntry)
	}
	entry, exists := limiter.entries[key]
	if !exists || now.Sub(entry.Started) >= commentRateWindow || now.Before(entry.Started) {
		if !exists && len(limiter.entries) >= commentRateKeyLimit {
			limiter.evictOldestLocked()
		}
		entry = commentRateEntry{Started: now}
	}
	entry.LastSeen = now
	if entry.Count >= limit {
		limiter.entries[key] = entry
		retry := entry.Started.Add(commentRateWindow).Sub(now)
		if retry < time.Second {
			retry = time.Second
		}
		return false, retry
	}
	entry.Count++
	limiter.entries[key] = entry
	return true, 0
}

func (limiter *commentRateLimiter) evictOldestLocked() {
	oldestKey := ""
	var oldest time.Time
	for key, entry := range limiter.entries {
		if oldestKey == "" || entry.LastSeen.Before(oldest) {
			oldestKey = key
			oldest = entry.LastSeen
		}
	}
	if oldestKey != "" {
		delete(limiter.entries, oldestKey)
	}
}

func commentTokenPayload(postID, expires int64) string {
	return commentFormTokenVersion + "|" + strconv.FormatInt(postID, 10) + "|" + strconv.FormatInt(expires, 10)
}

func boundedCommentUserAgent(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, character := range value {
		if unicode.IsControl(character) {
			character = ' '
		}
		size := utf8.RuneLen(character)
		if size < 0 || builder.Len()+size > 512 {
			break
		}
		builder.WriteRune(character)
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

func (s *Server) sameOriginPublicWrite(request *http.Request) bool {
	if request == nil || !s.sameOriginRequest(request) {
		return false
	}
	return len(request.Header.Values("Origin")) == 1 || len(request.Header.Values("Referer")) == 1
}
