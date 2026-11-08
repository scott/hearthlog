package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"example.invalid/hearthlog/internal/emitter"
)

func main() {
	endpoint := flag.String("endpoint", "", "loopback API endpoint")
	writeTokenFile := flag.String("write-token-file", "", "path to write token file")
	canaryFile := flag.String("canary-file", "", "path to canary file")
	flag.Parse()

	if *endpoint == "" || *writeTokenFile == "" || *canaryFile == "" {
		fmt.Fprintln(os.Stderr, "usage: synthetic -endpoint <url> -write-token-file <path> -canary-file <path>")
		os.Exit(2)
	}

	token, err := readBounded(*writeTokenFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to read write token")
		os.Exit(1)
	}

	canary, err := readBounded(*canaryFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to read canary")
		os.Exit(1)
	}

	secrets := []string{canary}
	h, err := emitter.New(*endpoint, token, secrets)
	if err != nil {
		fmt.Fprintln(os.Stderr, "emitter init failed")
		os.Exit(1)
	}

	for i := int64(1); i <= 100; i++ {
		h.Handle(context.Background(), slogRecord(i, canary))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := h.Flush(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "flush failed")
		os.Exit(1)
	}

	if err := h.Close(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "close failed")
		os.Exit(1)
	}

	fmt.Println("synthetic events acknowledged: 100")
}

func readBounded(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > 8192 {
		return "", fmt.Errorf("file too large")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func slogRecord(ordinal int64, canary string) slog.Record {
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "slice02synthetic marker <script>alert(1)</script> "+canary, 0)
	r.AddAttrs(
		slog.Int64("ordinal", ordinal),
		slog.Int64("exact_large_int64", 9007199254740993),
		slog.Float64("real_float64", 1.0),
		slog.Bool("bool_true", true),
		slog.Group("nested_group",
			slog.String("safe", "value"),
			slog.String("password", canary),
		),
	)
	return r
}
