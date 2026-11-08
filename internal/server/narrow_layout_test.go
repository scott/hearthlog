package server

import (
	"strings"
	"testing"
)

// TestNarrowLayoutCSSContract is a STATIC CSS source regression test.
// It does NOT run a browser, measure geometry, or verify computed cascade.
// It asserts that the embedded style.css contains narrow-layout safeguards
// and does not contain overflow masks that would hide content.
func TestNarrowLayoutCSSContract(t *testing.T) {
	data, err := embedFS.ReadFile("web/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := normalizeCSS(string(data))

	// Required declarations for narrow layout
	required := []string{
		"body{margin:0;font-family:system-ui,sans-serif;background:var(--bg);color:var(--fg);line-height:1.5;overflow-wrap:anywhere}",
		"fieldset{border:1px solid var(--border);border-radius:4px;padding:1rem;margin:0;min-width:0}",
		"input,button{font:inherit;padding:.4rem .6rem;border:1px solid var(--border);border-radius:4px;background:var(--card);color:var(--fg);max-width:100%;min-width:0}",
		".detail-meta{display:grid;grid-template-columns:auto minmax(0,1fr);gap:.25rem 1rem;margin:1rem 0}",
		".detail-meta dd{margin:0;min-width:0}",
		".detail-meta{grid-template-columns:minmax(0,1fr)}",
	}
	for _, decl := range required {
		if !strings.Contains(css, decl) {
			t.Errorf("missing required declaration: %s", decl)
		}
	}

	// Forbidden patterns: overflow masks on any selector
	forbidden := []string{
		"overflow-x:hidden",
		"overflow-x:clip",
		"overflow:hidden",
		"overflow:clip",
	}
	for _, pat := range forbidden {
		if strings.Contains(css, pat) {
			t.Errorf("forbidden overflow mask found: %s", pat)
		}
	}

	// Retained features: focus-visible, pre-wrap, vertical scroll
	retained := []string{
		"focus-visible",
		"white-space:pre-wrap",
		"overflow-y:auto",
	}
	for _, feat := range retained {
		if !strings.Contains(css, feat) {
			t.Errorf("retained feature missing: %s", feat)
		}
	}
}

// normalizeCSS collapses whitespace for simple exact-declaration matching.
func normalizeCSS(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}
