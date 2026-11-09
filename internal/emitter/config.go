package emitter

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"
)

// SDKVersion identifies this in-repository slog SDK revision in evidence and docs.
const SDKVersion = "0.3.0-dev"

// Wire routes the SDK can target. The endpoint path selects the envelope.
const (
	SkeletonPath = "/api/experimental/skeleton/batches" // Slice 02 skeleton: version 0, one event per batch
	NativePath   = "/api/v1/batches"                    // NATIVE-V1 envelope: version 1, <=200 events per batch
)

// Provisional limits from docs/PLAN.md and issue #4. They are acceptance targets, not measurements.
const (
	DefaultQueueEvents   = 2048
	DefaultQueueBytes    = 8 << 20
	MaxEventBytes        = 32 << 10
	MaxNesting           = 8
	MaxAttributes        = 128
	MaxBatchEvents       = 200
	MaxBatchBytes        = 256 << 10
	BatchInterval        = 250 * time.Millisecond
	MaxInFlight          = 2
	RequestTimeout       = 5 * time.Second
	RetryBudget          = 60 * time.Second
	MinBackoff           = 250 * time.Millisecond
	MaxBackoff           = 5 * time.Second
	maxReceiptBytes      = 4096
	maxRetryAfterSeconds = 3600
)

// Config configures a Handler. Endpoint and Token are required; everything
// else defaults to the provisional limits above. Values must come from operator
// configuration outside version control.
type Config struct {
	// Endpoint is a loopback http URL with an explicit port and either
	// SkeletonPath or NativePath, e.g. http://127.0.0.1:<PORT>/api/v1/batches.
	Endpoint string
	// Token is the write-only source token sent as a Bearer credential. It is
	// also scrubbed from every message and string attribute.
	Token string
	// Secrets are additional literal values scrubbed before enqueue.
	Secrets []string
	// Level is the minimum enabled level; nil means slog.LevelInfo.
	Level slog.Leveler
	// OnDiagnostic, if set, receives payload-free loss/rejection notices. It is
	// never invoked recursively; calls that would nest are only counted.
	// It must not block.
	OnDiagnostic func(Diagnostic)

	// QueueEvents and QueueBytes may only lower the defaults (tests, small hosts).
	QueueEvents int
	QueueBytes  int
}

// Diagnostic is a payload-free notice. It never contains messages, attribute
// values, tokens or response bodies.
type Diagnostic struct {
	Reason   DropReason
	Severity string // TRACE..FATAL, or "" for batch-level notices
	Events   int
	Status   int // HTTP status for rejected batches, 0 otherwise
}

// New keeps the Slice 02 constructor shape.
func New(endpoint, token string, secrets []string) (*Handler, error) {
	return NewWithConfig(Config{Endpoint: endpoint, Token: token, Secrets: secrets})
}

// NewWithConfig validates cfg and starts the background sender.
func NewWithConfig(cfg Config) (*Handler, error) {
	return newHandler(cfg, realClock{}, defaultTuning)
}

// tuning holds the timing limits; tests shorten them, production uses the defaults.
type tuning struct {
	batchInterval time.Duration
	reqTimeout    time.Duration
	retryBudget   time.Duration
}

var defaultTuning = tuning{batchInterval: BatchInterval, reqTimeout: RequestTimeout, retryBudget: RetryBudget}

func newHandler(cfg Config, clk clock, tune tuning) (*Handler, error) {
	if cfg.Token == "" {
		return nil, errors.New("token required")
	}
	if len(cfg.Secrets) > 32 {
		return nil, errors.New("too many secrets")
	}
	for _, s := range cfg.Secrets {
		if len(s) > 8192 {
			return nil, errors.New("secret too long")
		}
	}
	native, err := validateEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	qe, qb := DefaultQueueEvents, DefaultQueueBytes
	if cfg.QueueEvents < 0 || cfg.QueueBytes < 0 || cfg.QueueEvents > qe || cfg.QueueBytes > qb {
		return nil, errors.New("queue limits may only be lowered")
	}
	if cfg.QueueEvents > 0 {
		qe = cfg.QueueEvents
	}
	if cfg.QueueBytes > 0 {
		qb = cfg.QueueBytes
	}
	var level slog.Leveler = slog.LevelInfo
	if cfg.Level != nil {
		level = cfg.Level
	}

	secrets := make([]string, 0, len(cfg.Secrets)+1)
	secrets = append(secrets, cfg.Token)
	for _, s := range cfg.Secrets {
		if s != "" {
			secrets = append(secrets, s)
		}
	}

	batchEvents := MaxBatchEvents
	if !native {
		batchEvents = 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &state{
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:               nil,
				MaxIdleConnsPerHost: MaxInFlight,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		endpoint:    cfg.Endpoint,
		token:       cfg.Token,
		native:      native,
		secrets:     secrets,
		level:       level,
		onDiag:      cfg.OnDiagnostic,
		queueEvents: qe,
		queueBytes:  qb,
		batchEvents: batchEvents,
		clk:         clk,
		tune:        tune,
		life:        ctx,
		cancel:      cancel,
		wake:        make(chan struct{}, 1),
		done:        make(chan struct{}),
		changed:     make(chan struct{}),
		nextSeq:     1,
		inflight:    map[*batch]struct{}{},
	}
	s.wg.Add(1)
	go s.dispatch()
	go func() {
		s.wg.Wait()
		close(s.done)
	}()
	return &Handler{state: s}, nil
}

func validateEndpoint(endpoint string) (native bool, err error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false, errors.New("invalid endpoint")
	}
	if u.Scheme != "http" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || u.Opaque != "" {
		return false, errors.New("invalid endpoint")
	}
	switch u.Path {
	case SkeletonPath:
	case NativePath:
		native = true
	default:
		return false, errors.New("invalid endpoint")
	}
	host := u.Hostname()
	if host == "" {
		return false, errors.New("missing host")
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return false, errors.New("host must be loopback")
	}
	if u.Port() == "" {
		return false, errors.New("explicit port required")
	}
	return native, nil
}
