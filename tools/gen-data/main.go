// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Red Hat, Inc.

// Command gen-data generates the RHDS data tree embedded by rhds-mcp.
//
// `-update` resolves the current npm and GitHub inputs and rewrites the lock.
// Without `-update` it reads the lock and generates internal/data/dist. When
// the existing index matches the lock digest, generation is a scoped no-op and
// makes no network requests.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/redhat-ux/mcp/tools/gen-data/internal/fetch"
	"github.com/redhat-ux/mcp/tools/gen-data/internal/generate"
	"github.com/redhat-ux/mcp/tools/gen-data/internal/lock"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "gen-data: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("gen-data", flag.ContinueOnError)
	flags.SetOutput(stderr)
	lockPath := flags.String("lock", "sources.lock.json", "path to sources.lock.json")
	outDir := flags.String("out", "dist", "output directory")
	update := flags.Bool("update", false, "resolve current inputs and rewrite the lock")
	force := flags.Bool("force", false, "regenerate even when the index matches the lock")
	verbose := flags.Bool("verbose", false, "log fetch and generation progress")
	if err := flags.Parse(args); err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	if !*verbose {
		logger = slog.New(slog.DiscardHandler)
	}

	client := fetch.New()
	client.Token = fetch.TokenFromEnvironment()

	if *update {
		updated, err := lock.Update(ctx, client)
		if err != nil {
			return err
		}
		if err := lock.Save(*lockPath, updated); err != nil {
			return fmt.Errorf("writing %s: %w", *lockPath, err)
		}
		_, _ = fmt.Fprintf(stderr, "gen-data: wrote %s\n", *lockPath)
		return nil
	}

	pinned, err := lock.Load(*lockPath)
	if err != nil {
		return err
	}
	digest, err := pinned.Digest()
	if err != nil {
		return err
	}
	if !*force {
		upToDate, err := generate.IsUpToDate(*outDir, digest)
		if err != nil {
			return err
		}
		if upToDate {
			logger.Info("data is up to date", "lock", *lockPath, "out", *outDir)
			_, _ = fmt.Fprintf(stderr, "gen-data: %s is up to date\n", *outDir)
			return nil
		}
	}

	logger.Info("fetching pinned inputs")
	inputs, err := generate.Fetch(ctx, pinned, client)
	if err != nil {
		return err
	}
	logger.Info("generating data tree", "out", *outDir)
	index, err := generate.Generate(inputs, *outDir)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "gen-data: wrote %d artifacts to %s\n", len(index.Artifacts), *outDir)
	return nil
}
