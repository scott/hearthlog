package server

import (
	"html/template"
	"strings"
	"testing"

	"example.invalid/hearthlog/internal/model"
)

func TestAccessibilityContract(t *testing.T) {
	// Render full page with empty view
	var buf1 strings.Builder
	if err := tmpl.ExecuteTemplate(&buf1, "page", view{}); err != nil {
		t.Fatalf("render empty: %v", err)
	}
	page1 := buf1.String()

	// Render full page with detail view
	var buf2 strings.Builder
	if err := tmpl.ExecuteTemplate(&buf2, "page", view{Detail: &model.Record{Message: "synthetic"}}); err != nil {
		t.Fatalf("render detail: %v", err)
	}
	page2 := buf2.String()

	for name, page := range map[string]string{"empty": page1, "detail": page2} {
		t.Run(name, func(t *testing.T) {
			// 1. Skiplink href and text appear before main/form
			skipHref := `href="#investigation"`
			skipText := `Skip to main content`
			mainTag := `<main id="investigation"`
			formTag := `<form method="POST"`

			skipIdx := strings.Index(page, skipHref)
			if skipIdx < 0 {
				t.Fatal("skiplink href not found")
			}
			if !strings.Contains(page, skipText) {
				t.Fatal("skiplink text not found")
			}
			mainIdx := strings.Index(page, mainTag)
			if mainIdx < 0 {
				t.Fatal("main tag not found")
			}
			formIdx := strings.Index(page, formTag)
			if formIdx < 0 {
				t.Fatal("form tag not found")
			}
			if skipIdx > mainIdx || skipIdx > formIdx {
				t.Fatal("skiplink must appear before main and form")
			}

			// 2. Unique focusable main with id=investigation tabindex=-1
			if !strings.Contains(page, `id="investigation" tabindex="-1"`) {
				t.Fatal("main must have id=investigation and tabindex=-1")
			}
			if strings.Count(page, `id="investigation"`) != 1 {
				t.Fatal("main id must be unique")
			}

			// 3. Safe #status with tabindex=-1 and aria-live=polite
			if !strings.Contains(page, `id="status" aria-live="polite" tabindex="-1"`) {
				t.Fatal("status must have aria-live=polite and tabindex=-1")
			}

			// 4. No inline event handlers or scripts introduced
			for _, bad := range []string{"onclick=", "onload=", "onerror=", "<script>", "javascript:"} {
				if strings.Contains(page, bad) {
					t.Fatalf("unsafe pattern %q found in page", bad)
				}
			}
		})
	}

	// CSS contract checks
	css, err := embedFS.ReadFile("web/style.css")
	if err != nil {
		t.Fatalf("read css: %v", err)
	}
	cssStr := string(css)

	// Skiplink offscreen position
	if !strings.Contains(cssStr, ".skiplink{position:absolute;top:-100px") {
		t.Fatal("skiplink must be offscreen by default")
	}
	// Skiplink focus visible
	if !strings.Contains(cssStr, ".skiplink:focus{top:.5rem") {
		t.Fatal("skiplink must be visible on focus")
	}
	if !strings.Contains(cssStr, ".skiplink:focus{top:.5rem;outline:3px solid var(--accent);outline-offset:2px}") {
		t.Fatal("skiplink focus must have visible outline")
	}
	// Skiplink max-width bound
	if !strings.Contains(cssStr, ".skiplink{position:absolute;top:-100px;left:.5rem;padding:.5rem .75rem;background:var(--card);color:var(--fg);font-weight:600;text-decoration:none;z-index:1000;max-width:calc(100% - 1rem)}") {
		t.Fatal("skiplink must have max-width bound")
	}
	// Main focus outline
	if !strings.Contains(cssStr, "main:focus{outline:2px solid var(--accent);outline-offset:-2px}") {
		t.Fatal("main must have focus outline")
	}
	// No inline script/style in CSS
	if strings.Contains(cssStr, "<script>") || strings.Contains(cssStr, "<style>") {
		t.Fatal("CSS must not contain script or style tags")
	}

	// Ensure template is valid (already executed above, but verify parse)
	if tmpl == nil {
		t.Fatal("template not initialized")
	}
	_ = template.New("check")
}
