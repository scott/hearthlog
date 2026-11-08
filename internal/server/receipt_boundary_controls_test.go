package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestRequestDuplicateControls(t *testing.T) {
	cases := []struct {
		name    string
		inject  string
		want    int
		wantDup bool
	}{
		{"dup_version_null", `"version":null,"version":0`, http.StatusBadRequest, true},
		{"dup_version_0_null", `"version":0,"version":null`, http.StatusBadRequest, true},
		{"dup_version_0_0", `"version":0,"version":0`, http.StatusBadRequest, true},
		{"dup_escaped", `"version":0,"vers\u0069on":0`, http.StatusBadRequest, true},
		{"overflow_pos", `"version":99999999999999999999`, http.StatusBadRequest, false},
		{"overflow_neg", `"version":-99999999999999999999`, http.StatusBadRequest, false},
		{"valid_0", `"version":0`, http.StatusOK, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, ts, _, writeTok, _ := newTestApp(t)
			defer store.Close()

			raw := validBatch(t, 1)
			s := string(raw)

			// Find the original version token and replace it with our crafted fragment
			// We expect validBatch to produce "version":0
			orig := `"version":0`
			if !strings.Contains(s, orig) {
				t.Fatalf("expected %q in raw batch", orig)
			}

			// For duplicate cases, we replace the single version with two versions
			// For overflow cases, we replace with the overflow value
			var newVersion string
			if tc.wantDup {
				newVersion = tc.inject
			} else {
				// Extract just the value part for overflow cases
				// tc.inject is like "version":99999999999999999999
				newVersion = tc.inject
			}

			// Replace the original version token with our crafted one
			// We need to be careful: the original is "version":0
			// We replace it with our new fragment
			newS := strings.Replace(s, orig, newVersion, 1)
			if newS == s && newVersion != orig {
				t.Fatalf("replacement did not occur")
			}

			// Verify the wire bytes show what we expect
			if tc.wantDup {
				// Should have two version occurrences (plain or escaped)
				count := strings.Count(newS, `"version"`) + strings.Count(newS, `"vers\u0069on"`)
				if count != 2 {
					t.Fatalf("expected 2 version occurrences, got %d: %s", count, newS)
				}
			} else {
				count := strings.Count(newS, `"version"`) + strings.Count(newS, `"vers\u0069on"`)
				if count != 1 {
					t.Fatalf("expected 1 version occurrence, got %d: %s", count, newS)
				}
			}

			status, _, body := skeletonPost(t, ts, writeTok, []byte(newS), "/api/experimental/skeleton/batches")
			if status != tc.want {
				t.Fatalf("status: got %d want %d body: %s", status, tc.want, body)
			}

			// Verify persisted rows
			scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
			now := time.Now().UTC()
			q := model.Query{
				Sources:     []string{"src_demo_api"},
				From:        now.Add(-time.Minute).Format(time.RFC3339Nano),
				To:          now.Add(time.Minute).Format(time.RFC3339Nano),
				MinSeverity: 1,
				Terms:       nil,
				Limit:       200,
			}

			rows, err := store.Search(context.Background(), scope, q)
			if err != nil {
				t.Fatalf("search: %v", err)
			}

			wantRows := 0
			if tc.want == http.StatusOK {
				wantRows = 1
			}
			if len(rows) != wantRows {
				t.Errorf("rows: got %d want %d", len(rows), wantRows)
			}
		})
	}
}
