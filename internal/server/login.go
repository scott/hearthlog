package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"html/template"
	"net/http"
)

var loginTmpl = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html><head><title>Sign in</title></head>
<body>
<form method="POST" action="/login">
<label>Token <input type="password" name="token" required></label>
<button type="submit">Sign in</button>
</form>
</body></html>`))

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		loginTmpl.Execute(w, nil)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Header.Get("Origin") != s.origin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	token := r.PostFormValue("token")
	if token == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if len(token) != 64 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if _, err := hex.DecodeString(token); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	tokenHash := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(tokenHash[:], s.readHash[:]) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	session, csrf, err := s.newSession()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	_ = csrf

	cookie := &http.Cookie{
		Name:     sessionCookie,
		Value:    session,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   900,
	}
	http.SetCookie(w, cookie)

	http.Redirect(w, r, "/investigate", http.StatusSeeOther)
}
