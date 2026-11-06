package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// SSETicket = base64url(JSON) + "." + base64url(HMAC-SHA256)（protocol-http.md §5）。
type SSETicket struct {
	ExecutionID string   `json:"execution_id"`
	OperatorID  string   `json:"operator_id"`
	Perms       []string `json:"perms"`
	Exp         int64    `json:"exp"`
	JTI         string   `json:"jti"`
}

var ErrTicket = errors.New("invalid sse ticket")

type TicketIssuer struct {
	key  []byte
	ttl  time.Duration
	mu   sync.Mutex
	used map[string]int64 // jti -> exp
	now  func() time.Time
}

func NewTicketIssuer(key string, ttl time.Duration) *TicketIssuer {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &TicketIssuer{key: []byte("sse-ticket:" + key), ttl: ttl, used: map[string]int64{}, now: time.Now}
}

func (t *TicketIssuer) sign(p []byte) string {
	m := hmac.New(sha256.New, t.key)
	m.Write(p)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (t *TicketIssuer) Issue(executionID string, op Operator) (string, time.Time) {
	var b [16]byte
	_, _ = rand.Read(b[:])
	exp := t.now().Add(t.ttl)
	tk := SSETicket{ExecutionID: executionID, OperatorID: op.ID, Perms: op.Perms, Exp: exp.Unix(), JTI: hex.EncodeToString(b[:])}
	js, _ := json.Marshal(tk)
	p := base64.RawURLEncoding.EncodeToString(js)
	return p + "." + t.sign([]byte(p)), exp
}

// Redeem 校验签名/过期/执行 ID，并消耗 jti（单次使用）。
func (t *TicketIssuer) Redeem(raw, executionID string) (*SSETicket, error) {
	p, sig, ok := strings.Cut(raw, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(t.sign([]byte(p)))) {
		return nil, ErrTicket
	}
	js, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return nil, ErrTicket
	}
	var tk SSETicket
	if json.Unmarshal(js, &tk) != nil {
		return nil, ErrTicket
	}
	now := t.now().Unix()
	if tk.Exp < now || tk.ExecutionID != executionID || tk.JTI == "" {
		return nil, ErrTicket
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for j, e := range t.used {
		if e < now {
			delete(t.used, j)
		}
	}
	if _, dup := t.used[tk.JTI]; dup {
		return nil, ErrTicket
	}
	t.used[tk.JTI] = tk.Exp
	return &tk, nil
}
