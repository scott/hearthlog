package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestSkeletonBoundary(t *testing.T) {
	store, ts, _, writeTok, _ := newTestApp(t)

	// 1. First commit: 200 OK
	raw := validBatch(t, 1)
	status, _, body := skeletonPost(t, ts, writeTok, raw, "/api/experimental/skeleton/batches")
	if status != http.StatusOK {
		t.Fatalf("first commit status: %d body: %s", status, body)
	}

	var receipt model.Receipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatalf("unmarshal receipt: %v", err)
	}
	if receipt.Version != 0 || receipt.Protocol != "experimental-skeleton" {
		t.Fatalf("receipt version/protocol: %d/%s", receipt.Version, receipt.Protocol)
	}
	if receipt.Accepted != 1 {
		t.Fatalf("accepted: %d", receipt.Accepted)
	}

	// 2. Exact same batch again: 501 replay_not_supported
	status, _, body = skeletonPost(t, ts, writeTok, raw, "/api/experimental/skeleton/batches")
	if status != http.StatusNotImplemented {
		t.Fatalf("replay status: %d body: %s", status, body)
	}
	if !bytes.Contains(body, []byte(`"replay_not_supported"`)) {
		t.Fatalf("missing replay_not_supported: %s", body)
	}
	if bytes.Contains(body, []byte("batch_conflict")) || bytes.Contains(body, []byte("replay_until")) {
		t.Fatalf("forbidden field in response: %s", body)
	}

	// 3. Same batch, changed message: 501 replay_not_supported
	var b model.Batch
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("unmarshal batch: %v", err)
	}
	b.Events[0].Message = "changed message"
	changed, _ := json.Marshal(b)
	status, _, body = skeletonPost(t, ts, writeTok, changed, "/api/experimental/skeleton/batches")
	if status != http.StatusNotImplemented {
		t.Fatalf("changed replay status: %d body: %s", status, body)
	}
	if !bytes.Contains(body, []byte(`"replay_not_supported"`)) {
		t.Fatalf("missing replay_not_supported for changed: %s", body)
	}

	// 4. Version 0 on the native route is rejected and does not add a row.
	status, _, body = skeletonPost(t, ts, writeTok, raw, "/api/v1/batches")
	if status != http.StatusBadRequest {
		t.Fatalf("native version 0 status: %d body: %s", status, body)
	}

	// 5. Experimental endpoint with version 1: 400
	b.Version = 1
	v1raw, _ := json.Marshal(b)
	status, _, body = skeletonPost(t, ts, writeTok, v1raw, "/api/experimental/skeleton/batches")
	if status != http.StatusBadRequest {
		t.Fatalf("version1 status: %d body: %s", status, body)
	}

	// 6. Verify store retains exactly 1 event with original IDs
	rec, err := store.Detail(t.Context(), model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, receipt.FirstIngestID)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if rec.EventID != b.Events[0].EventID {
		t.Fatalf("event id mismatch: %s != %s", rec.EventID, b.Events[0].EventID)
	}
	if rec.Message != "synthetic event 0" {
		t.Fatalf("message mismatch: %s", rec.Message)
	}

	// 7. Search retains exactly 1 event
	now := time.Now().UTC().Truncate(time.Microsecond)
	q := model.Query{
		Sources:     []string{"src_demo_api"},
		From:        now.Add(-time.Hour).Format(time.RFC3339),
		To:          now.Add(time.Hour).Format(time.RFC3339),
		MinSeverity: 1,
		Limit:       200,
	}
	rows, err := store.Search(t.Context(), model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, q)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("search rows: %d, want 1", len(rows))
	}
	if rows[0].EventID != b.Events[0].EventID {
		t.Fatalf("search event id: %s != %s", rows[0].EventID, b.Events[0].EventID)
	}
	if rows[0].IngestID != receipt.FirstIngestID {
		t.Fatalf("search ingest id: %s != %s", rows[0].IngestID, receipt.FirstIngestID)
	}

	// 8. Positive control: search for a term that should NOT match
	q.Terms = []string{"nonexistent_term_xyz"}
	rows, err = store.Search(t.Context(), model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, q)
	if err != nil {
		t.Fatalf("search negative: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("negative search rows: %d, want 0", len(rows))
	}

}
