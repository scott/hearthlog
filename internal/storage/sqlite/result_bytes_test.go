package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"example.invalid/hearthlog/internal/model"
)

func TestResultBytesBound(t *testing.T) {
	t.Parallel()
	store, _ := freshStore(t)
	ctx := context.Background()

	// Seed 120 events with 16384-byte ASCII messages (below raw cap per event)
	msg := "boundedmarker " + strings.Repeat("a", 16384-len("boundedmarker "))
	if len(msg) != 16384 {
		t.Fatalf("msg length %d, want 16384", len(msg))
	}
	for i := 0; i < 120; i++ {
		b := negativeBatch(t, 1, msg)
		// Mutate IDs to distinct canonical UUIDv4
		b.BatchID = fmt.Sprintf("%08x-0000-4000-8000-%012d", i, i)
		b.Events[0].EventID = fmt.Sprintf("%08x-0000-4000-8000-%012d", i+1000, i)
		if _, err := store.Commit(ctx, model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, b); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}

	q := negativeQuery("boundedmarker")
	q.Sources = []string{"src_demo_api"}
	q.Limit = 200

	// Positive: 120 rows should fit under 2MiB
	rows, err := store.Search(ctx, model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, q)
	if err != nil {
		t.Fatalf("search positive: %v", err)
	}
	if len(rows) != 120 {
		t.Fatalf("expected 120 rows, got %d", len(rows))
	}
	total := 0
	for _, r := range rows {
		total += len(r.Message) + len(r.Attributes) + len(r.EventID) + len(r.IngestID) + len(r.SourceID) + len(r.EventTime) + len(r.ReceivedAt) + len(r.SeverityText) + 256
	}
	if total > model.MaxResultBytes {
		t.Fatalf("measured %d exceeds cap %d", total, model.MaxResultBytes)
	}
	t.Logf("measured bytes: %d", total)

	// Seed 30 more events to exceed cap (150 total)
	for i := 120; i < 150; i++ {
		b := negativeBatch(t, 1, msg)
		b.BatchID = fmt.Sprintf("%08x-0000-4000-8000-%012d", i, i)
		b.Events[0].EventID = fmt.Sprintf("%08x-0000-4000-8000-%012d", i+1000, i)
		if _, err := store.Commit(ctx, model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, b); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}

	// Negative: 150 rows should exceed cap
	rows2, err2 := store.Search(ctx, model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, q)
	if err2 != ErrTooBroad {
		t.Fatalf("expected ErrTooBroad, got %v", err2)
	}
	if rows2 != nil {
		t.Fatalf("expected nil rows, got %d", len(rows2))
	}

	// Extra: limit 1 with multiple matching events must return ErrTooBroad
	q.Limit = 1
	rows3, err3 := store.Search(ctx, model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, q)
	if err3 != ErrTooBroad {
		t.Fatalf("expected ErrTooBroad with limit 1, got %v", err3)
	}
	if rows3 != nil {
		t.Fatalf("expected nil rows with limit 1, got %d", len(rows3))
	}
}
