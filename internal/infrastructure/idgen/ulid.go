// Package idgen 生成 ULID（26 位 Crockford Base32，时间有序）。
package idgen

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	mu       sync.Mutex
	lastMs   uint64
	lastRand [10]byte
)

// NewULID 返回单调递增的 ULID（同一毫秒内随机部分 +1）。
func NewULID() string {
	mu.Lock()
	ms := uint64(time.Now().UnixMilli())
	if ms == lastMs {
		for i := 9; i >= 0; i-- {
			lastRand[i]++
			if lastRand[i] != 0 {
				break
			}
		}
	} else {
		lastMs = ms
		if _, err := rand.Read(lastRand[:]); err != nil {
			panic(err)
		}
	}
	var b [16]byte
	var t [8]byte
	binary.BigEndian.PutUint64(t[:], ms)
	copy(b[0:6], t[2:8])
	copy(b[6:], lastRand[:])
	mu.Unlock()
	return encode(b)
}

func encode(b [16]byte) string {
	// 128 位 → 26 个 5 位字符（首字符只用 3 位）
	var out [26]byte
	var hi, lo uint64 = binary.BigEndian.Uint64(b[0:8]), binary.BigEndian.Uint64(b[8:16])
	for i := 25; i >= 0; i-- {
		out[i] = alphabet[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}
