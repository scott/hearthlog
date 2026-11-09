// Command slogservice is a compilable synthetic service showing how a Go
// program adopts the Hearthlog slog SDK without losing its existing logging.
//
// Remote logging is opt-in: with no -endpoint the service keeps only its
// prior local handler (safe disable). All names and values are synthetic;
// the endpoint and token come from operator configuration outside version
// control, e.g.
//
//	slogservice -endpoint http://127.0.0.1:<PORT>/api/v1/batches -token-file <PATH_TO_WRITE_TOKEN>
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"example.invalid/hearthlog/internal/emitter"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("slogservice", flag.ContinueOnError)
	fs.SetOutput(stderr)
	endpoint := fs.String("endpoint", "", "Hearthlog ingest URL; empty disables remote logging")
	tokenFile := fs.String("token-file", "", "file holding the source write token (never a flag value)")
	secretFile := fs.String("secret-file", "", "optional file of extra literal values to scrub, one per line")
	events := fs.Int("events", 20, "synthetic requests to simulate")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// 1. The service's existing, already-safe local logging stays in place.
	local := slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	handler := slog.Handler(local)

	// 2. Remote logging is added only when configured (staged rollout); a
	//    misconfiguration falls back to local logging instead of failing.
	var remote *emitter.Handler
	if *endpoint != "" {
		h, err := newRemote(*endpoint, *tokenFile, *secretFile)
		if err != nil {
			slog.New(local).Warn("remote logging disabled", "reason", "configuration")
		} else {
			remote = h
			handler = slog.NewMultiHandler(local, remote)
		}
	}
	logger := slog.New(handler).With("component", "demo-worker", "env_label", "staging")

	// 3. Normal slog usage: groups, attributes and context correlation.
	for i := 0; i < *events; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		cancel() // a canceled request context does not suppress the log
		logger.InfoContext(ctx, "synthetic request handled",
			slog.Group("request", "id", fmt.Sprintf("req-%04d", i), "route", "/demo"),
			"duration_ms", 1.25, "status", 200)
	}
	logger.Warn("synthetic retry scheduled", "attempt", 2)

	if remote == nil {
		fmt.Fprintln(stdout, `{"sdk_version":"`+emitter.SDKVersion+`","remote":"disabled"}`)
		return 0
	}

	// 4. Shutdown with deadlines: Flush, then Close; report counters only.
	flushCtx, cancelFlush := context.WithTimeout(context.Background(), 2*time.Second)
	flushErr := remote.Flush(flushCtx)
	cancelFlush()
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
	closeErr := remote.Close(closeCtx)
	cancelClose()

	st := remote.Stats()
	out, _ := json.Marshal(map[string]any{
		"sdk_version":     emitter.SDKVersion,
		"remote":          "enabled",
		"handled":         st.Handled,
		"acked":           st.AckedEvents,
		"sent_batches":    st.SentBatches,
		"retries":         st.Retries,
		"dropped":         st.Dropped,
		"shutdown_unsent": st.ShutdownUnsent,
		"flush_ok":        flushErr == nil,
		"close_ok":        closeErr == nil,
	})
	fmt.Fprintln(stdout, string(out))
	var de *emitter.DeliveryError
	if errors.As(closeErr, &de) {
		return 1
	}
	return 0
}

func newRemote(endpoint, tokenFile, secretFile string) (*emitter.Handler, error) {
	token, err := readSmall(tokenFile)
	if err != nil {
		return nil, err
	}
	var secrets []string
	if secretFile != "" {
		s, err := readSmall(secretFile)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(s, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				secrets = append(secrets, line)
			}
		}
	}
	return emitter.NewWithConfig(emitter.Config{
		Endpoint: endpoint,
		Token:    token,
		Secrets:  secrets,
		Level:    slog.LevelInfo,
		// Payload-free loss notices; must not block. Counters stay authoritative.
		OnDiagnostic: func(d emitter.Diagnostic) {},
	})
}

func readSmall(path string) (string, error) {
	if path == "" {
		return "", errors.New("missing file")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.Size() > 8192 {
		return "", errors.New("file too large")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
