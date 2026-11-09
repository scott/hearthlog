package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"time"
)

const sessionCookie = "hl_session"
const sessionTTL = 15 * time.Minute

func (s *Server) readSession(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c == nil {
		return "", false
	}
	if len(c.Value) != 64 {
		return "", false
	}
	if _, err := hex.DecodeString(c.Value); err != nil {
		return "", false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if time.Now().After(s.sessionExp) {
		return "", false
	}
	cookieHash := sha256.Sum256([]byte(c.Value))
	if subtle.ConstantTimeCompare(cookieHash[:], s.sessionHash[:]) != 1 {
		return "", false
	}
	return s.csrf, true
}

func (s *Server) newSession() (string, string, error) {
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		return "", "", err
	}
	raw := hex.EncodeToString(rawBytes)

	csrfBytes := make([]byte, 32)
	if _, err := rand.Read(csrfBytes); err != nil {
		return "", "", err
	}
	csrf := hex.EncodeToString(csrfBytes)

	hash := sha256.Sum256([]byte(raw))

	s.mu.Lock()
	s.sessionHash = hash
	s.sessionExp = time.Now().Add(sessionTTL)
	s.csrf = csrf
	s.mu.Unlock()

	return raw, csrf, nil
}

func (s *Server) verifyWrite(token string) bool {
	if len(token) != 64 {
		return false
	}
	if _, err := hex.DecodeString(token); err != nil {
		return false
	}
	tokenHash := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(tokenHash[:], s.writeHash[:]) == 1
}
