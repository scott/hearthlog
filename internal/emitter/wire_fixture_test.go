package emitter

import (
	"bytes"
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

var updateWire = flag.Bool("update-wire", false, "rewrite testdata/wire golden fixtures")

var (
	uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`)
	timePattern = regexp.MustCompile(`"event_time":"[^"]+"`)
)

// sanitizeWire replaces random IDs and wall-clock times with placeholders so
// the committed fixture is deterministic and contains only synthetic data.
func sanitizeWire(body []byte) []byte {
	ids := map[string]string{}
	out := uuidPattern.ReplaceAllFunc(body, func(m []byte) []byte {
		if _, ok := ids[string(m)]; !ok {
			ids[string(m)] = "<UUID_" + string(rune('A'+len(ids))) + ">"
		}
		return []byte(ids[string(m)])
	})
	out = timePattern.ReplaceAll(out, []byte(`"event_time":"<EVENT_TIME>"`))
	var pretty bytes.Buffer
	_ = json.Indent(&pretty, out, "", "  ")
	pretty.WriteByte('\n')
	return pretty.Bytes()
}

// TestWireFixtures pins the exact redacted wire shape for both envelopes.
// The canary/secret inputs are synthetic; the fixtures prove what leaves the
// process for messages, WithAttrs, groups and LogValuers.
func TestWireFixtures(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"native_batch.json", NativePath},
		{"skeleton_batch.json", SkeletonPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const canary = "canary_wire_fixture_value"
			ns := newNativeServer(t)
			ns.respond = func(w http.ResponseWriter, r *http.Request, body []byte, attempt int) bool {
				var b model.Batch
				if err := json.Unmarshal(body, &b); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return true
				}
				rc := map[string]any{"version": b.Version, "batch_id": b.BatchID, "accepted": len(b.Events)}
				if b.Version == model.SkeletonVersion {
					rc["protocol"] = model.SkeletonProtocol
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(rc)
				return true
			}
			h := newTestHandler(t, Config{Endpoint: ns.srv.URL + tc.path, Secrets: []string{canary}}, nil, tuning{})
			l := slog.New(h).With("component", "demo-api", "api_token", canary).WithGroup("job")
			l.Info("synthetic job "+canary+" finished",
				"attempt", 2, "ok", true, "ratio", 0.5, "big", int64(9007199254740993),
				"took", 1500*time.Millisecond, "nothing", nil,
				slog.Group("ctx", "password", canary, "echo", canary, "region_label", "staging"),
				"valuer", secretGroupValuer{canary})
			if err := flushWithin(t, h, 5*time.Second); err != nil {
				t.Fatal(err)
			}
			bodies, _, _ := ns.snapshot()
			if len(bodies) != 1 || bytes.Contains(bodies[0], []byte(canary)) {
				t.Fatalf("bodies=%d", len(bodies))
			}
			got := sanitizeWire(bodies[0])
			path := filepath.Join("testdata", "wire", tc.name)
			if *updateWire {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("wire fixture mismatch for %s:\n%s", tc.name, got)
			}
		})
	}
}
