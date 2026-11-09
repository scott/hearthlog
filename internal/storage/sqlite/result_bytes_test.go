package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestResultBytesBound(t *testing.T) {
	t.Parallel()
	store, _ := freshStore(t)
	frozen := time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	store.clock.Store(func() time.Time { return frozen })
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

	// One page stays within the byte cap. A full match set that does not fit
	// continues through the keyset cursor.
	page, err := store.ReadPage(ctx, model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, q)
	if err != nil {
		t.Fatalf("search positive: %v", err)
	}
	if len(page.Rows) == 0 || len(page.Rows) > 120 {
		t.Fatalf("page rows %d", len(page.Rows))
	}
	if pageBytes(page.Rows) > model.MaxResultBytes {
		t.Fatalf("page bytes exceed cap")
	}

	// Seed 30 more events (150 total).
	for i := 120; i < 150; i++ {
		b := negativeBatch(t, 1, msg)
		b.BatchID = fmt.Sprintf("%08x-0000-4000-8000-%012d", i, i)
		b.Events[0].EventID = fmt.Sprintf("%08x-0000-4000-8000-%012d", i+1000, i)
		if _, err := store.Commit(ctx, model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, b); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}

	seen := map[string]struct{}{}
	pages := 0
	for {
		page, err = store.ReadPage(ctx, model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, q)
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		if len(page.Rows) == 0 {
			t.Fatal("empty page")
		}
		if pageBytes(page.Rows) > model.MaxResultBytes {
			t.Fatalf("page bytes %d", pageBytes(page.Rows))
		}
		for _, r := range page.Rows {
			if _, ok := seen[r.IngestID]; ok {
				t.Fatalf("duplicate %s", r.IngestID)
			}
			seen[r.IngestID] = struct{}{}
		}
		pages++
		if page.Next == "" {
			break
		}
		q.Cursor = page.Next
		if pages > 10 {
			t.Fatal("too many pages")
		}
	}
	if len(seen) != 150 {
		t.Fatalf("paged %d rows", len(seen))
	}
	if pages < 2 {
		t.Fatal("expected more than one page")
	}
	t.Logf("pages=%d", pages)

	q.Limit = 1
	q.Cursor = ""
	one, err := store.ReadPage(ctx, model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, q)
	if err != nil || len(one.Rows) != 1 || one.Next == "" {
		t.Fatalf("limit 1 page: n=%d err=%v next=%t", len(one.Rows), err, one.Next != "")
	}
}

func pageBytes(rows []model.Record) int {
	total := 0
	for _, r := range rows {
		total += len(r.Message) + len(r.Attributes) + len(r.EventID) + len(r.IngestID) + len(r.SourceID) + len(r.EventTime) + len(r.ReceivedAt) + len(r.SeverityText) + 1024
	}
	return total
}
