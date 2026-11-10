package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
)

const (
	HashAlgo = "hmac-sha256-v1"
	base62   = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// HashSecret = HMAC-SHA256(pepper, secret)。
func HashSecret(pepper, secret string) []byte {
	m := hmac.New(sha256.New, []byte(pepper))
	m.Write([]byte(secret))
	return m.Sum(nil)
}

func VerifySecretHash(pepper, secret string, stored []byte) bool {
	return hmac.Equal(HashSecret(pepper, secret), stored)
}

func SecretHint(secret string) string {
	if len(secret) < 4 {
		return ""
	}
	return secret[len(secret)-4:]
}

// NewClientSecret 生成约 256-bit 的 base62 secret。
func NewClientSecret() (string, error) {
	return randomString(base62, 43)
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

func deriveKey(pepper string) []byte {
	sum := sha256.Sum256([]byte("jc-client-secret:" + pepper))
	return sum[:]
}

// EncryptSecret AES-GCM(pepper_derived, secret)，返回 ciphertext（含 nonce 前缀）。
func EncryptSecret(pepper, secret string) ([]byte, error) {
	block, err := aes.NewCipher(deriveKey(pepper))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(secret), nil), nil
}

// DecryptSecret 还原明文 secret。
func DecryptSecret(pepper string, enc []byte) (string, error) {
	block, err := aes.NewCipher(deriveKey(pepper))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(enc) < ns {
		return "", errors.New("ciphertext too short")
	}
	plain, err := gcm.Open(nil, enc[:ns], enc[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return string(plain), nil
}

// EncodeB64 / DecodeB64 便于日志外传输（非 DB）。
func EncodeB64(b []byte) string { return base64.RawStdEncoding.EncodeToString(b) }
