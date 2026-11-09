package server

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFormSessionGuards(t *testing.T) {
	_, ts, readTok, _, _ := newTestApp(t)
	origin := "http://" + ts.Listener.Addr().String()

	client, csrf := login(t, ts, readTok)

	now := time.Now().UTC()
	from := now.Add(-time.Minute).Format(time.RFC3339)
	to := now.Add(time.Minute).Format(time.RFC3339)

	vals := url.Values{
		"csrf":       {csrf},
		"source":     {"src_demo_api"},
		"from":       {from},
		"to":         {to},
		"severity":   {"1"},
		"term":       {""},
		"generation": {"1"},
		"sequence":   {"1"},
	}

	// Positive: valid POST returns 200
	code, _, _ := postForm(t, client, origin, "/investigate/search", vals, true)
	if code != http.StatusOK {
		t.Fatalf("valid post: got %d want 200", code)
	}

	// Missing CSRF -> 403
	noCsrf := url.Values{}
	for k, v := range vals {
		if k != "csrf" {
			noCsrf[k] = v
		}
	}
	code, _, _ = postForm(t, client, origin, "/investigate/search", noCsrf, true)
	if code != http.StatusForbidden {
		t.Fatalf("missing csrf: got %d want 403", code)
	}

	// Wrong Origin -> 403
	body := vals.Encode()
	req, _ := http.NewRequest(http.MethodPost, origin+"/investigate/search", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("HX-Request", "true")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("wrong origin: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong origin: got %d want 403", resp.StatusCode)
	}

	// Anonymous HX POST -> 401
	anonJar, _ := cookiejar.New(nil)
	anonClient := &http.Client{Jar: anonJar}
	code, _, _ = postForm(t, anonClient, origin, "/investigate/search", vals, true)
	if code != http.StatusUnauthorized {
		t.Fatalf("unauth: got %d want 401", code)
	}

	// Rotate session: second login invalidates first
	_, _ = login(t, ts, readTok)

	// Old client with old CSRF should now be 401
	code, _, _ = postForm(t, client, origin, "/investigate/search", vals, true)
	if code != http.StatusUnauthorized {
		t.Fatalf("rotated session: got %d want 401", code)
	}
}
