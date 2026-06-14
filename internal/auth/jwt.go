package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

var (
	jwtSecret   []byte
	tokenTTL    = 24 * time.Hour
	errBadToken = errors.New("invalid token")
	errExpired  = errors.New("token expired")
)

// initSecret loads the JWT secret from secretPath if it exists,
// or generates a new one and saves it. This means tokens survive server restarts.
func initSecret(secretPath string) error {
	if data, err := os.ReadFile(secretPath); err == nil && len(data) >= 32 {
		jwtSecret = data[:32]
		jwtSecretRef = jwtSecret
		return nil
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	// Save for future restarts (non-fatal if write fails — e.g. read-only FS)
	_ = os.WriteFile(secretPath, secret, 0600)
	jwtSecret = secret
	jwtSecretRef = secret
	return nil
}

func b64(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

func b64dec(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// GenerateToken creates a signed JWT for the given user.
func GenerateToken(u *User) (string, error) {
	header := b64([]byte(`{"alg":"HS256","typ":"JWT"}`))

	payload, err := json.Marshal(Claims{
		Username: u.Username,
		Role:     u.Role,
		Exp:      time.Now().Add(tokenTTL).Unix(),
	})
	if err != nil {
		return "", err
	}
	payloadB64 := b64(payload)

	sigInput := header + "." + payloadB64
	mac := hmac.New(sha256.New, jwtSecret)
	mac.Write([]byte(sigInput))
	sig := b64(mac.Sum(nil))

	return sigInput + "." + sig, nil
}

// ParseToken validates a JWT string and returns its Claims.
func ParseToken(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errBadToken
	}

	sigInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, jwtSecret)
	mac.Write([]byte(sigInput))
	expected := b64(mac.Sum(nil))

	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return nil, errBadToken
	}

	payloadBytes, err := b64dec(parts[1])
	if err != nil {
		return nil, errBadToken
	}

	var c Claims
	if err = json.Unmarshal(payloadBytes, &c); err != nil {
		return nil, errBadToken
	}

	if time.Now().Unix() > c.Exp {
		return nil, errExpired
	}

	return &c, nil
}
