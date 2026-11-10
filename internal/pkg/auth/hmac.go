package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// 管理 API 签名头（与 docs/architecture.md 一致）。
const (
	HeaderClientID     = "X-JC-Client-Id"
	HeaderTimestamp    = "X-JC-Timestamp"
	HeaderNonce        = "X-JC-Nonce"
	HeaderOperatorID   = "X-JC-Operator-Id"
	HeaderOperatorName = "X-JC-Operator-Name"
	HeaderSignature    = "X-JC-Signature"
)

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func CanonicalQuery(rawQuery string) (string, error) {
	vals, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", err
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), vals[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, rfc3986(k)+"="+rfc3986(v))
		}
	}
	return strings.Join(parts, "&"), nil
}

func rfc3986(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// Canonical:
//
//	METHOD\nPATH\nQUERY\nClient-Id\nTimestamp\nNonce\nOperator-Id\nOperator-Name\nhex(sha256(body))
func Canonical(method, path, rawQuery, clientID, timestamp, nonce, operatorID, operatorName string, body []byte) (string, error) {
	cq, err := CanonicalQuery(rawQuery)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		strings.ToUpper(method),
		path,
		cq,
		clientID,
		timestamp,
		nonce,
		operatorID,
		operatorName,
		sha256Hex(body),
	}, "\n"), nil
}

func Sign(secret, canonical string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(canonical))
	return hex.EncodeToString(m.Sum(nil))
}

func VerifySignature(secret, canonical, sigHex string) bool {
	want, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(canonical))
	return hmac.Equal(m.Sum(nil), want)
}

// NonceCache 进程内防重放。
type NonceCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]int64
}

func NewNonceCache(ttl time.Duration) *NonceCache {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &NonceCache{ttl: ttl, m: map[string]int64{}}
}

func (c *NonceCache) Use(clientID, nonce string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().UnixNano()
	key := clientID + "\x00" + nonce
	if exp, ok := c.m[key]; ok && exp > now {
		return false
	}
	c.m[key] = now + int64(c.ttl)
	if len(c.m) > 10000 {
		for k, exp := range c.m {
			if exp <= now {
				delete(c.m, k)
			}
		}
	}
	return true
}
