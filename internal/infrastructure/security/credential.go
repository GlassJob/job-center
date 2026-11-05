// Package security 实现执行器凭据（jcs_<key_id>.<secret>）与管理 API HMAC 签名。
package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"math/big"
	"regexp"
	"strings"
)

const (
	CredentialPrefix = "jcs_"
	HashAlgo         = "hmac-sha256-v1"
	base62           = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

var (
	ErrMalformedCredential = errors.New("malformed credential")
	keyIDRe                = regexp.MustCompile(`^[a-z0-9]{8,32}$`)
	secretRe               = regexp.MustCompile(`^[0-9A-Za-z]{32,128}$`)
)

// NewCredential 生成 key_id（16 位小写 base36）与 secret（43 位 base62 ≈ 256 bit）。
func NewCredential() (token, keyID, secret string, err error) {
	if keyID, err = randomString("0123456789abcdefghijklmnopqrstuvwxyz", 16); err != nil {
		return
	}
	if secret, err = randomString(base62, 43); err != nil {
		return
	}
	return CredentialPrefix + keyID + "." + secret, keyID, secret, nil
}

// ParseCredential 拆分 jcs_<key_id>.<secret>。
func ParseCredential(token string) (keyID, secret string, err error) {
	rest, ok := strings.CutPrefix(token, CredentialPrefix)
	if !ok {
		return "", "", ErrMalformedCredential
	}
	keyID, secret, ok = strings.Cut(rest, ".")
	if !ok || !keyIDRe.MatchString(keyID) || !secretRe.MatchString(secret) {
		return "", "", ErrMalformedCredential
	}
	return keyID, secret, nil
}

// HashSecret = HMAC-SHA256(pepper, secret)。
func HashSecret(pepper, secret string) []byte {
	m := hmac.New(sha256.New, []byte(pepper))
	m.Write([]byte(secret))
	return m.Sum(nil)
}

// VerifySecret 常量时间比较。
func VerifySecret(pepper, secret string, storedHash []byte) bool {
	return hmac.Equal(HashSecret(pepper, secret), storedHash)
}

// SecretHint 返回 secret 末 4 位，仅用于展示。
func SecretHint(secret string) string {
	if len(secret) < 4 {
		return ""
	}
	return secret[len(secret)-4:]
}

func randomString(alpha string, n int) (string, error) {
	b := make([]byte, n)
	max := big.NewInt(int64(len(alpha)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = alpha[v.Int64()]
	}
	return string(b), nil
}

// RedactCredential 把文本中的 jcs_ 凭据替换为 jcs_<key_id>.***，用于日志。
func RedactCredential(s string) string {
	return credInText.ReplaceAllString(s, "${1}.***")
}

var credInText = regexp.MustCompile(`(jcs_[a-z0-9]{8,32})\.[0-9A-Za-z]+`)
