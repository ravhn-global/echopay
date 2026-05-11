package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Compact JWT-ish implementation (HS256). Avoids pulling a JWT library for
// what is essentially: header.payload.signature.

type Claims struct {
	UserID    uuid.UUID `json:"sub"`
	Phone     string    `json:"phone"`
	IssuedAt  int64     `json:"iat"`
	ExpiresAt int64     `json:"exp"`
}

type Issuer struct {
	secret []byte
	ttl    time.Duration
}

func NewIssuer(secret string, ttl time.Duration) *Issuer {
	return &Issuer{secret: []byte(secret), ttl: ttl}
}

func (i *Issuer) Issue(userID uuid.UUID, phone string) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:    userID,
		Phone:     phone,
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(i.ttl).Unix(),
	}
	header := map[string]string{"alg": "HS256", "typ": "JWT"}

	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)

	headerSeg := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsSeg := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerSeg + "." + claimsSeg

	sig := i.sign(signingInput)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (i *Issuer) Parse(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	signingInput := parts[0] + "." + parts[1]
	expected := i.sign(signingInput)
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}
	if !hmac.Equal(expected, got) {
		return nil, errors.New("signature mismatch")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("unmarshal claims: %w", err)
	}
	if time.Now().Unix() >= claims.ExpiresAt {
		return nil, errors.New("token expired")
	}
	return &claims, nil
}

func (i *Issuer) sign(input string) []byte {
	mac := hmac.New(sha256.New, i.secret)
	mac.Write([]byte(input))
	return mac.Sum(nil)
}
