package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.invalid/hearthlog/internal/model"
)

func TestBoundedWriterExactBoundary(t *testing.T) {
	bw := &boundedWriter{}
	max := model.MaxResultBytes

	// Fill to exactly max
	chunk := make([]byte, 1024)
	for i := 0; i < max/1024; i++ {
		if _, err := bw.Write(chunk); err != nil {
			t.Fatalf("write at %d: %v", i*1024, err)
		}
	}
	if bw.buf.Len() != max {
		t.Fatalf("len = %d, want %d", bw.buf.Len(), max)
	}
	if bw.overflow {
		t.Fatal("overflow set prematurely")
	}

	// 1 byte overflow
	snap := make([]byte, bw.buf.Len())
	copy(snap, bw.buf.Bytes())
	if _, err := bw.Write([]byte{0x00}); err == nil {
		t.Fatal("expected error on overflow")
	}
	if !bw.overflow {
		t.Fatal("overflow flag not set")
	}
	if !bytes.Equal(bw.buf.Bytes(), snap) {
		t.Fatal("buffer mutated on overflow")
	}

	// Subsequent write fails
	if _, err := bw.Write([]byte{0x01}); err == nil {
		t.Fatal("expected error after overflow")
	}
}

func TestBoundedWriterInitialRefuse(t *testing.T) {
	bw := &boundedWriter{}
	big := make([]byte, model.MaxResultBytes+1)
	n, err := bw.Write(big)
	if n != 0 {
		t.Fatalf("n = %d, want 0", n)
	}
	if err == nil {
		t.Fatal("expected error")
	}
	if bw.buf.Len() != 0 {
		t.Fatalf("buf len = %d, want 0", bw.buf.Len())
	}

	// io.WriteString bypass
	bw2 := &boundedWriter{}
	if _, err := io.WriteString(bw2, strings.Repeat("a", model.MaxResultBytes+1)); err == nil {
		t.Fatal("expected error from WriteString")
	}
	if bw2.buf.Len() != 0 {
		t.Fatalf("buf len = %d, want 0", bw2.buf.Len())
	}
}

func TestRenderOverflow422(t *testing.T) {
	s := &Server{}
	v := view{
		Query: model.Query{Limit: 1},
		Rows: []model.Record{{
			EventID:        "e1",
			IngestID:       "1",
			SourceID:       "s",
			EventTime:      "2026-01-01T00:00:00Z",
			ReceivedAt:     "2026-01-01T00:00:00Z",
			SeverityNumber: 9,
			SeverityText:   "INFO",
			Message:        strings.Repeat("<&", 300000),
			Attributes:     json.RawMessage(`{}`),
		}},
	}

	// Full page
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/investigate/search", nil)
	s.render(w, r, "results", v, 200)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("full page status = %d, want 422", w.Code)
	}
	if len(w.Body.String()) > 4096 {
		t.Fatalf("body too large: %d", len(w.Body.String()))
	}
	var errResp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if errResp.Error.Code != "query_too_broad" {
		t.Fatalf("code = %q, want query_too_broad", errResp.Error.Code)
	}
	if w.Header().Get("X-Fragment-Kind") != "" {
		t.Fatal("fragment header set on error")
	}
	if strings.Contains(w.Body.String(), "<script>") {
		t.Fatal("row marker leaked")
	}

	// HX partial
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/investigate/search", nil)
	r2.Header.Set("HX-Request", "true")
	s.render(w2, r2, "results", v, 200)
	if w2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("partial status = %d, want 422", w2.Code)
	}
	if len(w2.Body.String()) > 4096 {
		t.Fatalf("partial body too large: %d", len(w2.Body.String()))
	}
}
