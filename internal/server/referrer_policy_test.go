package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestReferrerPolicyNativeForms(t *testing.T) {
	_, ts, readTok, _, _ := newTestApp(t)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	resp, err := client.Get(ts.URL + "/login")
	if err != nil {
		t.Fatalf("get login: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "strict-origin" {
		t.Fatalf("login referrer-policy: %q", got)
	}
	if !strings.Contains(string(body), `method="POST"`) || !strings.Contains(string(body), `action="/login"`) {
		t.Fatal("login form missing native POST action")
	}

	client2, _ := login(t, ts, readTok)

	resp2, err := client2.Get(ts.URL + "/investigate")
	if err != nil {
		t.Fatalf("get investigate: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("investigate status: %d", resp2.StatusCode)
	}
	if got := resp2.Header.Get("Referrer-Policy"); got != "strict-origin" {
		t.Fatalf("investigate referrer-policy: %q", got)
	}
	if !strings.Contains(string(body2), `<meta name="referrer" content="strict-origin">`) {
		t.Fatal("investigate missing strict-origin meta")
	}
	if strings.Contains(string(body2), `<meta name="referrer" content="no-referrer">`) {
		t.Fatal("investigate has no-referrer meta")
	}
	if !strings.Contains(string(body2), `method="POST"`) || !strings.Contains(string(body2), `action="/investigate/search"`) {
		t.Fatal("search form missing native POST action")
	}
	if strings.Contains(string(body2), "require(") || strings.Contains(string(body2), "requireJS") {
		t.Fatal("investigate page references requireJS")
	}

}
