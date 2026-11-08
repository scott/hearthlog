package emitter

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"example.invalid/hearthlog/internal/model"
)

type Handler struct {
	state   *state
	base    map[string]any
	groups  []string
	invalid bool
}

type state struct {
	mu       sync.Mutex
	queue    chan []byte
	client   *http.Client
	endpoint string
	token    string
	secrets  []string
	pending  int
	closed   bool
	done     chan struct{}
	drops    atomic.Uint64
	sent     atomic.Uint64
	fails    atomic.Uint64
}

func New(endpoint, token string, secrets []string) (*Handler, error) {
	if token == "" {
		return nil, errors.New("token required")
	}
	if len(secrets) > 32 {
		return nil, errors.New("too many secrets")
	}
	for _, s := range secrets {
		if len(s) > 8192 {
			return nil, errors.New("secret too long")
		}
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" || u.Path != "/api/experimental/skeleton/batches" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("invalid endpoint")
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("missing host")
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, errors.New("host must be loopback")
	}
	if u.Port() == "" {
		return nil, errors.New("explicit port required")
	}

	all := make([]string, 0, len(secrets)+1)
	all = append(all, token)
	all = append(all, secrets...)

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	s := &state{
		queue:    make(chan []byte, 128),
		client:   client,
		endpoint: endpoint,
		token:    token,
		secrets:  all,
		done:     make(chan struct{}),
	}
	go s.run()

	return &Handler{state: s}, nil
}

func (h *Handler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	if h.invalid {
		h.state.drops.Add(1)
		return nil
	}
	if r.NumAttrs() > 128 {
		h.state.drops.Add(1)
		return nil
	}
	ev, err := h.snapshot(r)
	if err != nil {
		h.state.drops.Add(1)
		return nil
	}
	b := model.Batch{
		Version: 0,
		BatchID: newUUIDv4(),
		Events:  []model.Event{ev},
	}
	payload, err := json.Marshal(b)
	if err != nil || len(payload) > 32*1024 {
		h.state.drops.Add(1)
		return nil
	}
	h.state.mu.Lock()
	if h.state.closed {
		h.state.mu.Unlock()
		h.state.drops.Add(1)
		return nil
	}
	select {
	case h.state.queue <- payload:
		h.state.pending++
	default:
		h.state.drops.Add(1)
	}
	h.state.mu.Unlock()
	return nil
}

func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("cryptographic RNG failure")
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
