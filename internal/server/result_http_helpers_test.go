package server

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"example.invalid/hearthlog/internal/model"
)

func resultForm(csrf string, q model.Query) url.Values {
	v := url.Values{}
	for _, s := range q.Sources {
		v.Add("source", s)
	}
	v.Set("from", q.From)
	v.Set("to", q.To)
	v.Set("severity", itoa(q.MinSeverity))
	for _, t := range q.Terms {
		v.Add("term", t)
	}
	v.Set("generation", "2")
	v.Set("sequence", "2")
	v.Set("csrf", csrf)
	return v
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func checkResultBudget(t *testing.T, code int, hdr http.Header, body []byte, overflow bool) {
	t.Helper()
	t.Logf("response status=%d bytes=%d", code, len(body))

	if !overflow {
		if code < 200 || code >= 300 {
			t.Fatalf("expected 2xx, got %d", code)
		}
		if len(body) > model.MaxResultBytes {
			t.Fatalf("body %d exceeds MaxResultBytes %d", len(body), model.MaxResultBytes)
		}
		if !bytes.Contains(body, []byte("boundedmarker")) {
			t.Fatal("boundedmarker missing in success body")
		}
		return
	}

	if code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", code)
	}
	ctype := hdr.Get("Content-Type")
	if !strings.HasPrefix(ctype, "application/json") {
		t.Fatalf("expected application/json, got %q", ctype)
	}
	if hdr.Get("X-Fragment-Kind") != "" {
		t.Fatal("X-Fragment-Kind must be empty on overflow")
	}
	if len(body) > 4096 {
		t.Fatalf("overflow body %d exceeds 4096", len(body))
	}
	if !bytes.Contains(body, []byte(`"code":"query_too_broad"`)) {
		t.Fatal("error code query_too_broad missing")
	}
	if !bytes.Contains(body, []byte(`"retryable":false`)) {
		t.Fatal("retryable false missing")
	}
	if bytes.Contains(body, []byte("boundedmarker")) {
		t.Fatal("boundedmarker leaked in overflow body")
	}
	if bytes.Contains(body, []byte("<!DOCTYPE")) {
		t.Fatal("doctype leaked in overflow body")
	}
	if bytes.Contains(body, []byte("results-region")) {
		t.Fatal("results-region leaked in overflow body")
	}
}
