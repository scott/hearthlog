package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func TestWorkspaceIsolation(t *testing.T) {
	store, _ := freshStore(t)
	ctx := context.Background()

	scopeA := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
	scopeB := model.Scope{WorkspaceID: "ws_demo_b", SourceID: "src_demo_worker"}

	batchA := negativeBatch(t, 1, "marker_alpha")
	batchB := negativeBatch(t, 1, "marker_beta")
	batchB.BatchID = batchA.BatchID

	_, err := store.Commit(ctx, scopeA, batchA)
	if err != nil {
		t.Fatalf("commit A: %v", err)
	}
	_, err = store.Commit(ctx, scopeB, batchB)
	if err != nil {
		t.Fatalf("commit B: %v", err)
	}

	now := time.Now().UTC()
	qA := negativeQuery("marker_alpha")
	qA.From = now.Add(-time.Minute).Format(time.RFC3339)
	qA.To = now.Add(time.Minute).Format(time.RFC3339)

	resA, err := store.Search(ctx, scopeA, qA)
	if err != nil || len(resA) != 1 {
		t.Fatalf("search A: %v, len=%d", err, len(resA))
	}

	qB := negativeQuery("marker_beta")
	qB.Sources = []string{"src_demo_worker"}
	qB.From = now.Add(-time.Minute).Format(time.RFC3339)
	qB.To = now.Add(time.Minute).Format(time.RFC3339)

	resB, err := store.Search(ctx, scopeB, qB)
	if err != nil || len(resB) != 1 {
		t.Fatalf("search B: %v, len=%d", err, len(resB))
	}

	if resA[0].IngestID == resB[0].IngestID {
		t.Fatal("ingest IDs must differ across workspaces")
	}

	qCross := negativeQuery("marker_beta")
	qCross.Sources = []string{"src_demo_worker"}
	qCross.From = now.Add(-time.Minute).Format(time.RFC3339)
	qCross.To = now.Add(time.Minute).Format(time.RFC3339)
	resCross, err := store.Search(ctx, scopeA, qCross)
	if err != nil {
		t.Fatalf("cross search error: %v", err)
	}
	if len(resCross) != 0 {
		t.Fatalf("scope A leaked B rows: %d", len(resCross))
	}

	readA := model.Scope{WorkspaceID: scopeA.WorkspaceID}
	readB := model.Scope{WorkspaceID: scopeB.WorkspaceID}

	if _, err := store.Detail(ctx, readA, resB[0].IngestID); err != ErrNotFound {
		t.Fatalf("A detail B ID: expected ErrNotFound, got %v", err)
	}
	if _, err := store.Detail(ctx, readB, resA[0].IngestID); err != ErrNotFound {
		t.Fatalf("B detail A ID: expected ErrNotFound, got %v", err)
	}

	if _, err := store.Detail(ctx, readA, resA[0].IngestID); err != nil {
		t.Fatalf("A detail own: %v", err)
	}
	if _, err := store.Detail(ctx, readB, resB[0].IngestID); err != nil {
		t.Fatalf("B detail own: %v", err)
	}

	hostile := negativeQuery(`" OR 1=1 --`)
	hostile.From = now.Add(-time.Minute).Format(time.RFC3339)
	hostile.To = now.Add(time.Minute).Format(time.RFC3339)
	resHostile, err := store.Search(ctx, scopeA, hostile)
	if err != nil {
		t.Fatalf("hostile search error: %v", err)
	}
	if len(resHostile) != 0 {
		t.Fatalf("hostile query leaked rows: %d", len(resHostile))
	}

	for _, r := range resA {
		if strings.Contains(r.Message, "marker_beta") {
			t.Fatal("A result contains B marker")
		}
	}
	for _, r := range resB {
		if strings.Contains(r.Message, "marker_alpha") {
			t.Fatal("B result contains A marker")
		}
	}
}
