package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func FuzzLiteralTerm(f *testing.F) {
	f.Add("synthetic")
	f.Add(`" OR 1=1 --`)
	f.Add("AND")
	f.Add("OR")
	f.Add("*")
	f.Add("NOT")
	f.Add(`"`)
	f.Add("café")
	f.Add("a AND b")
	f.Add(strings.Repeat("x", 128))
	f.Add(strings.Repeat("y", 129))
	f.Fuzz(func(t *testing.T, term string) {
		store, _ := freshStore(t)
		frozen := time.Date(2026, 1, 2, 3, 15, 30, 0, time.UTC)
		store.clock.Store(func() time.Time { return frozen })
		scope := model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}
		if _, err := store.Commit(context.Background(), scope, eventBatch(t, 1, frozen.Add(-time.Second), "fuzzbase marker", `{"k":1}`)); err != nil {
			t.Fatal(err)
		}
		q := model.Query{
			Sources: []string{"src_demo_api"}, From: frozen.Add(-time.Minute).Format(time.RFC3339Nano),
			To: frozen.Add(time.Second).Format(time.RFC3339Nano), MinSeverity: 1, Terms: []string{term}, Limit: 10,
		}
		page, err := store.ReadPage(context.Background(), scope, q)
		if err != nil {
			if len(term) > model.MaxTermBytes || term == "" || strings.ContainsRune(term, 0) {
				return
			}
			if err != nil && !isQueryErr(err) {
				t.Fatal(err)
			}
			return
		}
		for _, r := range page.Rows {
			if !strings.Contains(r.Message, "fuzzbase") && !strings.Contains(strings.ToLower(r.Message), strings.ToLower(term)) {
				t.Fatalf("unexpected row %q for term %q", r.Message, term)
			}
		}
	})
}

func isQueryErr(err error) bool {
	return err == model.ErrValidation || err == ErrStorage || err == ErrTooBroad
}

func FuzzPredicatePath(f *testing.F) {
	f.Add("task")
	f.Add("a.b")
	f.Add("a[0]")
	f.Add("a' OR 1=1")
	f.Add("_ok")
	f.Add("1bad")
	f.Add("")
	f.Fuzz(func(t *testing.T, seg string) {
		q := model.Query{
			From:        time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
			To:          time.Date(2026, 1, 2, 3, 15, 0, 0, time.UTC).Format(time.RFC3339Nano),
			MinSeverity: 1, Limit: 10,
			Predicates: []model.Predicate{{Path: []string{seg}, Op: "missing"}},
		}
		err := model.ValidateQuery(q)
		if seg == "" || len(seg) > 128 {
			if err == nil {
				t.Fatal("accepted bad path")
			}
			return
		}
		for _, r := range seg {
			if r > 127 || !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_') {
				if err != model.ErrUnsupportedPath && err != model.ErrValidation {
					t.Fatalf("seg %q err %v", seg, err)
				}
				return
			}
		}
		if len(seg) > 0 && seg[0] >= '0' && seg[0] <= '9' {
			if err != model.ErrUnsupportedPath {
				t.Fatalf("index seg %q err %v", seg, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("seg %q: %v", seg, err)
		}
	})
}
