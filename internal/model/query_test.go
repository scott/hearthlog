package model

import (
	"strings"
	"testing"
	"time"
)

func baseQuery() Query {
	now := time.Date(2026, 1, 2, 3, 15, 0, 0, time.UTC)
	return Query{
		Sources:     []string{"src_demo_api"},
		From:        now.Add(-15 * time.Minute).Format(time.RFC3339Nano),
		To:          now.Format(time.RFC3339Nano),
		MinSeverity: 9,
		Terms:       []string{"synthetic"},
		Limit:       200,
	}
}

func TestValidateQueryBounds(t *testing.T) {
	q := baseQuery()
	if err := ValidateQuery(q); err != nil {
		t.Fatal(err)
	}
	wide := q
	from, _ := time.Parse(time.RFC3339Nano, q.From)
	wide.To = from.Add(MaxWindow).Format(time.RFC3339Nano)
	if err := ValidateQuery(wide); err != nil {
		t.Fatal(err)
	}
	wide.To = from.Add(MaxWindow + time.Microsecond).Format(time.RFC3339Nano)
	if err := ValidateQuery(wide); err == nil {
		t.Fatal("expected window rejection")
	}
	tooMany := q
	tooMany.Terms = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	if err := ValidateQuery(tooMany); err == nil {
		t.Fatal("expected term cap")
	}
	long := q
	long.Terms = []string{strings.Repeat("x", MaxTermBytes+1)}
	if err := ValidateQuery(long); err == nil {
		t.Fatal("expected term length")
	}
	big := q
	big.Cursor = strings.Repeat("a", MaxQueryBytes+1)
	if err := ValidateQuery(big); err == nil {
		t.Fatal("expected 4KiB rejection")
	}
}

func TestPredicatePaths(t *testing.T) {
	q := baseQuery()
	q.Predicates = []Predicate{{Path: []string{"task", "attempt"}, Op: "eq", Type: "integer", Value: []byte("1")}}
	if err := ValidateQuery(q); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		p    Predicate
		path bool
	}{
		{"dot", Predicate{Path: []string{"a.b"}, Op: "eq", Type: "string", Value: []byte(`"x"`)}, true},
		{"quote", Predicate{Path: []string{"a'b"}, Op: "missing"}, true},
		{"index", Predicate{Path: []string{"0"}, Op: "missing"}, true},
		{"bracket", Predicate{Path: []string{"a[0]"}, Op: "missing"}, true},
		{"nine", Predicate{Path: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, Op: "missing"}, false},
		{"missing-with-value", Predicate{Path: []string{"a"}, Op: "missing", Type: "null"}, false},
		{"bool-as-int", Predicate{Path: []string{"a"}, Op: "eq", Type: "integer", Value: []byte("true")}, false},
		{"real-word", Predicate{Path: []string{"a"}, Op: "eq", Type: "real", Value: []byte("true")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := baseQuery()
			q.Predicates = []Predicate{tc.p}
			err := ValidateQuery(q)
			if tc.path {
				if err != ErrUnsupportedPath {
					t.Fatalf("got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
	okReal := baseQuery()
	okReal.Predicates = []Predicate{{Path: []string{"ratio"}, Op: "eq", Type: "real", Value: []byte("1.5")}}
	if err := ValidateQuery(okReal); err != nil {
		t.Fatal(err)
	}
	nine := baseQuery()
	nine.Predicates = make([]Predicate, 9)
	for i := range nine.Predicates {
		nine.Predicates[i] = Predicate{Path: []string{"a"}, Op: "missing"}
	}
	if err := ValidateQuery(nine); err == nil {
		t.Fatal("expected predicate cap")
	}
}
