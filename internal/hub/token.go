package hub

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	AgentID   string
	SessionID string
	Write     bool
	ExpiresAt time.Time
}

type tokenClaims struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
	Write     bool   `json:"write"`
	jwt.RegisteredClaims
}

type TokenManager struct {
	secret []byte
}

func NewTokenManager(secret string) *TokenManager {
	return &TokenManager{secret: []byte(secret)}
}

func (m *TokenManager) Issue(claims Claims, ttl time.Duration) (string, error) {
	if len(m.secret) == 0 {
		return "", errors.New("secret required")
	}
	exp := time.Now().Add(ttl)
	tc := tokenClaims{
		AgentID:   claims.AgentID,
		SessionID: claims.SessionID,
		Write:     claims.Write,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, tc)
	return token.SignedString(m.secret)
}

func (m *TokenManager) Verify(token string) (Claims, error) {
	if len(m.secret) == 0 {
		return Claims{}, errors.New("secret required")
	}
	parsed, err := jwt.ParseWithClaims(token, &tokenClaims{}, func(token *jwt.Token) (any, error) {
		return m.secret, nil
	})
	if err != nil {
		return Claims{}, err
	}
	claims, ok := parsed.Claims.(*tokenClaims)
	if !ok || !parsed.Valid {
		return Claims{}, errors.New("invalid token")
	}
	out := Claims{
		AgentID:   claims.AgentID,
		SessionID: claims.SessionID,
		Write:     claims.Write,
	}
	if claims.ExpiresAt != nil {
		out.ExpiresAt = claims.ExpiresAt.Time
	}
	return out, nil
}
