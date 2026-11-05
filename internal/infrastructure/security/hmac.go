package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// 管理 API 签名头（docs/protocol-http.md §2）。
const (
	HeaderClient    = "X-JC-Client"
	HeaderTimestamp = "X-JC-Timestamp"
	HeaderNonce     = "X-JC-Nonce"
	HeaderOperator  = "X-JC-Operator"
	HeaderSignature = "X-JC-Signature"
)

// Operator 由 GlassAdmin 后端签名转发的管理员身份；只信任签名头，不信任请求体。
type Operator struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Perms     []string `json:"perms"`
	ClientIP  string   `json:"client_ip"`
	RequestID string   `json:"request_id"`
}

func (o *Operator) Has(perm string) bool {
	for _, p := range o.Perms {
		if p == perm || p == "*" {
			return true
		}
	}
	return false
}

// EncodeOperator = base64url(JSON)，无填充。
func EncodeOperator(o Operator) string {
	b, _ := json.Marshal(o)
	return base64.RawURLEncoding.EncodeToString(b)
}

func DecodeOperator(h string) (*Operator, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(h, "="))
	if err != nil {
		return nil, fmt.Errorf("operator header: %w", err)
	}
	var o Operator
	if err := json.Unmarshal(b, &o); err != nil {
		return nil, fmt.Errorf("operator header: %w", err)
	}
	if o.ID == "" {
		return nil, errors.New("operator header: id required")
	}
	return &o, nil
}

// CanonicalQuery 按 key 排序（同 key 按 value 排序），RFC 3986 编码（空格为 %20）。
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

func rfc3986(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }

func sha256Hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// Canonical = METHOD\nPATH\nCANONICAL_QUERY\nhex(sha256(body))\nTIMESTAMP\nNONCE\nhex(sha256(operator))
func Canonical(method, path, rawQuery string, body []byte, timestamp, nonce, operator string) (string, error) {
	cq, err := CanonicalQuery(rawQuery)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{strings.ToUpper(method), path, cq, sha256Hex(body), timestamp, nonce, sha256Hex([]byte(operator))}, "\n"), nil
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

// NonceCache 进程内防重放（V1 单节点；多节点时改为落库/Redis）。
type NonceCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]time.Time
	now  func() time.Time
}

func NewNonceCache(ttl time.Duration) *NonceCache {
	return &NonceCache{ttl: ttl, seen: map[string]time.Time{}, now: time.Now}
}

// Use 记录 nonce；在 TTL 内重复返回 false。
func (c *NonceCache) Use(client, nonce string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.seen) > 10000 {
		for k, exp := range c.seen {
			if now.After(exp) {
				delete(c.seen, k)
			}
		}
	}
	k := client + "\x00" + nonce
	if exp, ok := c.seen[k]; ok && now.Before(exp) {
		return false
	}
	c.seen[k] = now.Add(c.ttl)
	return true
}
