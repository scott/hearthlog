package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"example.invalid/hearthlog/internal/model"
)

func skeletonPost(t *testing.T, ts *httptest.Server, token string, raw []byte, path string) (int, http.Header, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(body) > 4096 {
		t.Fatalf("response too large: %d", len(body))
	}
	if resp.StatusCode == http.StatusNotImplemented {
		var errResp struct {
			Version  int    `json:"version"`
			Protocol string `json:"protocol"`
			Error    struct {
				Code      string `json:"code"`
				Retryable *bool  `json:"retryable"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &errResp); err != nil {
			t.Fatalf("501 unmarshal: %v", err)
		}
		if errResp.Version != 0 {
			t.Errorf("501 version: %d", errResp.Version)
		}
		if errResp.Protocol != "experimental-skeleton" {
			t.Errorf("501 protocol: %s", errResp.Protocol)
		}
		if errResp.Error.Code != "replay_not_supported" {
			t.Errorf("501 code: %s", errResp.Error.Code)
		}
		if errResp.Error.Retryable == nil || *errResp.Error.Retryable != false {
			t.Errorf("501 retryable: %v", errResp.Error.Retryable)
		}
	}
	return resp.StatusCode, resp.Header, body
}

func TestB2B4FirstReceipt(t *testing.T) {
	store, ts, _, writeTok, _ := newTestApp(t)
	defer store.Close()

	raw := validBatch(t, 1)
	status, _, body := skeletonPost(t, ts, writeTok, raw, "/api/experimental/skeleton/batches")
	if status != http.StatusOK {
		t.Fatalf("status: %d body: %s", status, body)
	}

	var receipt model.Receipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if receipt.Version != 0 {
		t.Errorf("version: %d", receipt.Version)
	}
	if receipt.Protocol != "experimental-skeleton" {
		t.Errorf("protocol: %s", receipt.Protocol)
	}
	if receipt.Accepted != 1 {
		t.Errorf("accepted: %d", receipt.Accepted)
	}
	if receipt.BatchID != "00000000-0000-4000-8000-000000000001" {
		t.Errorf("batch_id: %s", receipt.BatchID)
	}
	if receipt.FirstIngestID != receipt.LastIngestID {
		t.Errorf("first %s != last %s", receipt.FirstIngestID, receipt.LastIngestID)
	}

	// Check no replay_until field
	if bytes.Contains(body, []byte("replay_until")) {
		t.Error("replay_until found in receipt")
	}

	// Verify receipt time matches stored record exactly
	rec, err := store.Detail(t.Context(), model.Scope{WorkspaceID: "ws_demo_a", SourceID: "src_demo_api"}, receipt.FirstIngestID)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}

	wireTime, err := time.Parse(time.RFC3339Nano, receipt.ReceivedAt)
	if err != nil {
		t.Fatalf("parse receipt.ReceivedAt: %v", err)
	}

	storedTime, err := time.Parse(time.RFC3339Nano, rec.ReceivedAt)
	if err != nil {
		t.Fatalf("parse rec.ReceivedAt: %v", err)
	}

	if wireTime.UnixMicro() != storedTime.UnixMicro() {
		t.Errorf("receipt time mismatch: wire=%s stored=%s", receipt.ReceivedAt, rec.ReceivedAt)
	}

	if receipt.ReceivedAt != rec.ReceivedAt {
		t.Errorf("receipt string mismatch: wire=%s stored=%s", receipt.ReceivedAt, rec.ReceivedAt)
	}

	t.Logf("receipt time exact match: %s (unix_micro=%d)", receipt.ReceivedAt, wireTime.UnixMicro())
}
