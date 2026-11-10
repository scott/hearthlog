package server

import (
	"crypto/sha256"
	"errors"
	"net"
	"net/url"
	"sync"
	"time"

	"example.invalid/hearthlog/internal/storage/sqlite"
)

type Server struct {
	store       *sqlite.Store
	readHash    [32]byte
	writeHash   [32]byte
	origin      string
	host        string
	mu          sync.Mutex
	sessionHash [32]byte
	sessionExp  time.Time
	csrf        string
	live        liveHub
}

func New(store *sqlite.Store, readToken, writeToken, origin string) (*Server, error) {
	if len(readToken) < 32 || len(writeToken) < 32 {
		return nil, errors.New("invalid token length")
	}
	if readToken == writeToken {
		return nil, errors.New("tokens must differ")
	}

	u, err := url.Parse(origin)
	if err != nil {
		return nil, errors.New("invalid origin")
	}
	if u.Scheme != "http" {
		return nil, errors.New("origin must be http")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("origin must be bare host:port")
	}

	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return nil, errors.New("origin must include port")
	}
	if port == "" {
		return nil, errors.New("origin must include port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("origin must be loopback")
	}

	return &Server{
		store:     store,
		readHash:  sha256.Sum256([]byte(readToken)),
		writeHash: sha256.Sum256([]byte(writeToken)),
		origin:    u.String(),
		host:      u.Host,
	}, nil
}
