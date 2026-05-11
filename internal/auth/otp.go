package auth

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
)

// GenerateOTP returns a uniformly-distributed 6-digit code.
func GenerateOTP() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint32(b[:]) % 1_000_000
	return fmt.Sprintf("%06d", n), nil
}
