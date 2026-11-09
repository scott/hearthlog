package server

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
)

func TestNativeOriginDefenses(t *testing.T) {
	_, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	doLogin := func(originHdr, host string, token string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/login", strings.NewReader("token="+token))
		if host != "" {
			req.Host = host
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if originHdr != "" {
			req.Header.Set("Origin", originHdr)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	cases := []struct {
		name, originHdr, host string
	}{
		{"missingOrigin", "", ""},
		{"nullOrigin", "null", ""},
		{"foreignOrigin", "http://evil.example", ""},
		{"localhostAlias", "http://localhost:" + strings.TrimPrefix(ts.Listener.Addr().String(), "127.0.0.1:"), ""},
		{"wrongHost", origin, "127.0.0.1:9999"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "missingOrigin" || c.name == "nullOrigin" {
				// Prove Sec-Fetch-Site does not bypass Origin check
				req, _ := http.NewRequest(http.MethodPost, ts.URL+"/login", strings.NewReader("token="+readTok))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if c.originHdr != "" {
					req.Header.Set("Origin", c.originHdr)
				}
				req.Header.Set("Sec-Fetch-Site", "same-origin")
				resp, err := client.Do(req)
				if err != nil {
					t.Fatalf("sec-fetch: %v", err)
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusForbidden {
					t.Fatalf("sec-fetch bypass: got %d", resp.StatusCode)
				}
			}
			got := doLogin(c.originHdr, c.host, readTok)
			if got != http.StatusForbidden {
				t.Fatalf("expected 403, got %d", got)
			}
		})
	}

	// Bogus token with correct origin -> 401
	got := doLogin(origin, "", strings.Repeat("0", 64))
	if got != http.StatusUnauthorized {
		t.Fatalf("bogus token: expected 401, got %d", got)
	}

	// Genuine session via helper
	client2, csrf := login(t, ts, readTok)

	protected := func(path, originHdr, csrfVal string) int {
		t.Helper()
		vals := url.Values{"csrf": {csrfVal}}
		req, _ := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(vals.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if originHdr != "" {
			req.Header.Set("Origin", originHdr)
		}
		resp, err := client2.Do(req)
		if err != nil {
			t.Fatalf("protected: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// Origin defenses on protected routes
	for _, path := range []string{"/investigate/search", "/investigate/detail"} {
		for _, c := range []struct{ name, origin string }{
			{"nullOrigin", "null"},
			{"foreignOrigin", "http://evil.example"},
			{"missingOrigin", ""},
		} {
			t.Run("protected/"+path+"/"+c.name, func(t *testing.T) {
				if got := protected(path, c.origin, csrf); got != http.StatusForbidden {
					t.Fatalf("expected 403, got %d", got)
				}
			})
		}
		t.Run("protected/"+path+"/wrongCSRF", func(t *testing.T) {
			if got := protected(path, origin, "badcsrf"); got != http.StatusForbidden {
				t.Fatalf("wrong csrf: expected 403, got %d", got)
			}
		})
	}
}
