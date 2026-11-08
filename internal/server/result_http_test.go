package server

import (
	"strings"
	"testing"

	"example.invalid/hearthlog/internal/model"
)

func TestResultHTTPBytes(t *testing.T) {
	cases := []struct {
		name     string
		count    int
		per      int
		overflow bool
	}{
		{"below", 80, 8192, false},
		{"rawoverflow", 150, 16384, true},
		{"escapeoverflow", 150, 4096, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, ts, readTok, _, _ := newTestApp(t)
			defer store.Close()

			prefix := "boundedmarker "
			var msg string
			if tc.name == "escapeoverflow" {
				msg = prefix + strings.Repeat("<&", 100000)
			} else {
				msg = prefix + strings.Repeat("S", 100000)
			}
			if len(msg) < tc.per {
				msg += strings.Repeat("S", tc.per-len(msg))
			}
			msg = msg[:tc.per]
			if len(msg) != tc.per {
				t.Fatalf("msg len %d want %d", len(msg), tc.per)
			}

			q := seedResultBytes(t, store, tc.count, msg)

			client, csrf := login(t, ts, readTok)
			origin := ts.URL

			code, hdr, body := postForm(t, client, origin, "/investigate/search", resultForm(csrf, q), false)
			checkResultBudget(t, code, hdr, body, tc.overflow)
			if !tc.overflow {
				if got := strings.Count(string(body), `data-ingest-id=`); got != tc.count {
					t.Fatalf("full page rows %d want %d", got, tc.count)
				}
			}

			code2, hdr2, body2 := postForm(t, client, origin, "/investigate/search", resultForm(csrf, q), true)
			checkResultBudget(t, code2, hdr2, body2, tc.overflow)
			if !tc.overflow {
				if got := strings.Count(string(body2), `data-ingest-id=`); got != tc.count {
					t.Fatalf("hx rows %d want %d", got, tc.count)
				}
			}

			if tc.name == "escapeoverflow" {
				rows, err := store.Search(t.Context(), model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, q)
				if err != nil {
					t.Fatalf("search: %v", err)
				}
				if len(rows) != tc.count {
					t.Fatalf("rows %d want %d", len(rows), tc.count)
				}
				v := view{Rows: rows, Query: q, CSRF: csrf, Generation: "2", Sequence: "2"}
				for _, name := range []string{"page", "results"} {
					var sb strings.Builder
					if err := tmpl.ExecuteTemplate(&sb, name, v); err != nil {
						t.Fatalf("exec %s: %v", name, err)
					}
					t.Logf("escaped baseline %s len=%d", name, sb.Len())
					if sb.Len() <= model.MaxResultBytes {
						t.Fatalf("escaped baseline %s %d not > MaxResultBytes %d", name, sb.Len(), model.MaxResultBytes)
					}
				}
			}
		})
	}
}
