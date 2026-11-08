package model

import "encoding/json"

// SkeletonVersion is the experimental skeleton protocol version.
const SkeletonVersion = 0

// SkeletonProtocol identifies the experimental skeleton receipt protocol.
const SkeletonProtocol = "experimental-skeleton"

// Scope is server-issued and never client-provided.
type Scope struct {
	WorkspaceID string `json:"-"`
	SourceID    string `json:"-"`
}

// Event is the experimental skeleton event layout, shared for future NATIVE-ID compatibility.
type Event struct {
	EventID        string          `json:"event_id"`
	EventTime      string          `json:"event_time"`
	SeverityNumber int             `json:"severity_number"`
	SeverityText   string          `json:"severity_text"`
	Message        string          `json:"message"`
	Attributes     json.RawMessage `json:"attributes"`
	TraceID        *string         `json:"trace_id,omitempty"`
	SpanID         *string         `json:"span_id,omitempty"`

	// Internal: UTC microseconds, set during validation
	EventTimeMicros int64 `json:"-"`
}

// Batch is the experimental skeleton envelope, not native v1.
type Batch struct {
	Version int     `json:"version"`
	BatchID string  `json:"batch_id"`
	Events  []Event `json:"events"`
}

// Receipt is the experimental skeleton response shape.
type Receipt struct {
	Version       int    `json:"version"`
	Protocol      string `json:"protocol"`
	BatchID       string `json:"batch_id"`
	Accepted      int    `json:"accepted"`
	FirstIngestID string `json:"first_ingest_id"`
	LastIngestID  string `json:"last_ingest_id"`
	ReceivedAt    string `json:"received_at"`
}

// Query params for scoped FTS search.
type Query struct {
	Sources     []string `json:"sources"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	MinSeverity int      `json:"min_severity"`
	Terms       []string `json:"terms"`
	Limit       int      `json:"limit"`
}

// MaxResultBytes is the cumulative materialization budget for search results.
const MaxResultBytes = 2 * 1024 * 1024

// Record is a persisted event row projection.
type Record struct {
	EventID        string          `json:"event_id"`
	IngestID       string          `json:"ingest_id"`
	SourceID       string          `json:"source_id"`
	EventTime      string          `json:"event_time"`
	ReceivedAt     string          `json:"received_at"`
	SeverityNumber int             `json:"severity_number"`
	SeverityText   string          `json:"severity_text"`
	Message        string          `json:"message"`
	Attributes     json.RawMessage `json:"attributes"`
}
