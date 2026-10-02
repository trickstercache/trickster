/*
 * Copyright 2026 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Command devorigin is the developer environment's origin service. The serve
// subcommand exposes the trips seed data as live Prometheus metrics and hosts
// the in-repo mock origins; backfill writes the same metrics' recent history
// as OpenMetrics for promtool to import before Prometheus starts.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

const usage = "usage: devorigin backfill|serve [flags]"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "devorigin:", err)
		os.Exit(1)
	}
}

func run(args []string, log io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(log)
	dataDir := fs.String("seed-data", envOr("DEVORIGIN_SEED_DATA", "/seed-data"), "directory holding the trips seed files and seed-window.env")
	switch args[0] {
	case "backfill":
		out := fs.String("out", envOr("DEVORIGIN_OUT", "/om/trips.om"), "OpenMetrics output file")
		step := fs.Duration("step", envDuration("DEVORIGIN_STEP", time.Minute), "interval between backfilled samples")
		window := fs.Duration("window", envDuration("DEVORIGIN_WINDOW", 15*24*time.Hour),
			"history to backfill; keep it within the Prometheus retention")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return backfill(backfillOptions{
			dataDir: *dataDir, out: *out, step: *step, window: *window, now: time.Now(),
		}, log)
	case "serve":
		addr := fs.String("addr", envOr("DEVORIGIN_ADDR", ":8482"), "listen address")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return serve(*dataDir, *addr, log)
	}
	return errors.New(usage)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return d
	}
	return def
}
