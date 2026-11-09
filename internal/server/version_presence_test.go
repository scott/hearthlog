package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestExperimentalRequestVersionRequired(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		missing bool
		want    int
	}{
		{"missing", "", true, http.StatusBadRequest},
		{"null", "null", false, http.StatusBadRequest},
		{"string_zero", `"0"`, false, http.StatusBadRequest},
		{"true", "true", false, http.StatusBadRequest},
		{"array", "[]", false, http.StatusBadRequest},
		{"object", "{}", false, http.StatusBadRequest},
		{"float_zero", "0.0", false, http.StatusBadRequest},
		{"float_half", "0.5", false, http.StatusBadRequest},
		{"one", "1", false, http.StatusBadRequest},
		{"neg_one", "-1", false, http.StatusBadRequest},
		{"exp_one", "1e0", false, http.StatusBadRequest},
		{"valid_zero", "0", false, http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, ts, _, writeTok, _ := newTestApp(t)
			defer store.Close()

			raw := validBatch(t, 1)
			var m map[string]json.RawMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("unmarshal batch: %v", err)
			}

			if tc.missing {
				delete(m, "version")
			} else {
				m["version"] = json.RawMessage(tc.raw)
			}

			actual, err := json.Marshal(m)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			status, _, body := skeletonPost(t, ts, writeTok, actual, "/api/experimental/skeleton/batches")
			if status != tc.want {
				t.Fatalf("status: got %d want %d body: %s", status, tc.want, body)
			}

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

			if tc.want == http.StatusOK && len(rows) == 1 {
				if rows[0].EventID == "" {
					t.Error("empty event_id in committed row")
				}
			}
		})
	}
}
