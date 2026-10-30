// Package id generates identifiers used by persisted entities.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

var state struct {
	sync.Mutex
	lastMillis int64
	sequence   uint16
}

// NewUUIDv7 returns a UUIDv7 string. The timestamp is sortable to millisecond
// precision and the remaining bits are cryptographically random, with a
// process-local sequence preventing collisions within the same millisecond.
func NewUUIDv7() (string, error) {
	now := time.Now().UnixMilli()
	state.Lock()
	if now == state.lastMillis {
		state.sequence++
	} else {
		state.lastMillis = now
		state.sequence = 0
	}
	sequence := state.sequence
	state.Unlock()

	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate UUIDv7 randomness: %w", err)
	}
	value[0] = byte(now >> 40)
	value[1] = byte(now >> 32)
	value[2] = byte(now >> 24)
	value[3] = byte(now >> 16)
	value[4] = byte(now >> 8)
	value[5] = byte(now)
	value[6] = 0x70 | byte(sequence>>8)&0x0f
	value[7] = byte(sequence)
	value[8] = 0x80 | value[8]&0x3f
	return format(value), nil
}

func format(value [16]byte) string {
	var encoded [36]byte
	hex.Encode(encoded[0:8], value[0:4])
	hex.Encode(encoded[9:13], value[4:6])
	hex.Encode(encoded[14:18], value[6:8])
	hex.Encode(encoded[19:23], value[8:10])
	hex.Encode(encoded[24:36], value[10:16])
	encoded[8] = '-'
	encoded[13] = '-'
	encoded[18] = '-'
	encoded[23] = '-'
	return string(encoded[:])
}
