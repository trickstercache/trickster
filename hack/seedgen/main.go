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

// Command seedgen writes the developer environment's synthetic trips seed
// data: two gzip TSV files with byte-identical content on every run, plus the
// seed-window.env metadata the database loaders use to shift timestamps.
package main

import (
	"bufio"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	fileName1   = "trips_1.gz"
	fileName2   = "trips_2.gz"
	sidecarName = "seed-data.sha256"
	envName     = "seed-window.env"
)

// profiles are the row-count presets whose uncompressed SHA-256 is pinned.
var profiles = map[string]int{"default": 24000, "small": 2400}

// golden is the expected SHA-256 of the uncompressed TSV stream per profile.
// Update it deliberately when the model changes; a mismatch means the output
// is no longer reproducible.
var golden = map[string]string{
	"default": "2022936b3f79323ddfd5dc9485c16c9fa2bfc335b4333cd2a2f42b9e6359ed32",
	"small":   "5f3ab16151d9070d891e598071a4e455d727b198381754c63bc9e61df843cbbe",
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "seedgen:", err)
		os.Exit(1)
	}
}

type options struct {
	out        string
	profile    string
	rowsPerDay int
	seedEpoch  int64
	force      bool
	verifyOnly bool
}

func parseOptions(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("seedgen", flag.ContinueOnError)
	fs.StringVar(&o.out, "out", envOr("SEED_OUT", "./seed-data"), "output directory")
	fs.StringVar(&o.profile, "profile", envOr("SEED_PROFILE", "default"), "row-count profile: default or small")
	fs.IntVar(&o.rowsPerDay, "rows-per-day", envInt("SEED_ROWS_PER_DAY", 0), "override the profile's base rows per day")
	fs.Int64Var(&o.seedEpoch, "seed-epoch", envInt64("SEED_EPOCH", 0), "seed instant as a unix epoch (default: now)")
	fs.BoolVar(&o.force, "force", false, "regenerate even when the cached output already matches")
	fs.BoolVar(&o.verifyOnly, "verify-only", false, "generate to memory and compare against the pinned hash; write nothing")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	base, ok := profiles[o.profile]
	if !ok {
		return o, fmt.Errorf("unknown profile %q", o.profile)
	}
	if o.rowsPerDay == 0 {
		o.rowsPerDay = base
	}
	if o.rowsPerDay < 24 {
		return o, errors.New("rows-per-day must be at least 24")
	}
	if o.seedEpoch == 0 {
		o.seedEpoch = time.Now().Unix()
	}
	return o, nil
}

func run(args []string, log io.Writer) error {
	o, err := parseOptions(args)
	if err != nil {
		return err
	}
	expected := ""
	if profiles[o.profile] == o.rowsPerDay {
		expected = golden[o.profile]
	}
	cfg := config{rowsPerDay: o.rowsPerDay}

	if o.verifyOnly {
		start := time.Now()
		sum, err := generate(cfg, hashOnly{})
		if err != nil {
			return err
		}
		fmt.Fprintf(log, "generated %d rows in %s, sha256=%s\n", sum.rows, time.Since(start).Round(time.Millisecond), sum.sha256)
		return checkGolden(sum.sha256, expected)
	}

	if err := os.MkdirAll(o.out, 0o755); err != nil {
		return err
	}
	sidecar := sidecarContent(expected, o.profile, o.rowsPerDay)
	if !o.force && expected != "" && cachedOutputValid(o.out, sidecar) {
		_, _ = fmt.Fprintln(log, "seed data already generated and verified; skipping (use -force to regenerate)")
		sum, err := generate(config{rowsPerDay: o.rowsPerDay}, hashOnly{})
		if err != nil {
			return err
		}
		return writeSeedWindow(o.out, sum, o.seedEpoch, log)
	}

	start := time.Now()
	fs := &fileSink{dir: o.out}
	sum, err := generate(cfg, fs)
	if err != nil {
		fs.cleanup()
		return err
	}
	fmt.Fprintf(log, "generated %d rows (%d + %d) in %s, sha256=%s\n", sum.rows,
		sum.rowsPerFile[0], sum.rowsPerFile[1], time.Since(start).Round(time.Millisecond), sum.sha256)
	if err := checkGolden(sum.sha256, expected); err != nil {
		fs.cleanup()
		return err
	}
	if err := fs.commit(); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(o.out, sidecarName), []byte(sidecarContent(sum.sha256, o.profile, o.rowsPerDay))); err != nil {
		return err
	}
	return writeSeedWindow(o.out, sum, o.seedEpoch, log)
}

func checkGolden(got, expected string) error {
	if expected == "" {
		return nil
	}
	if got != expected {
		return fmt.Errorf("output is not reproducible: sha256 %s, expected %s", got, expected)
	}
	return nil
}

func sidecarContent(hash, profile string, rowsPerDay int) string {
	return fmt.Sprintf("%s  profile=%s rows_per_day=%d\n", hash, profile, rowsPerDay)
}

func cachedOutputValid(dir, sidecar string) bool {
	b, err := os.ReadFile(filepath.Join(dir, sidecarName))
	if err != nil || string(b) != sidecar {
		return false
	}
	for _, name := range []string{fileName1, fileName2} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || st.Size() == 0 {
			return false
		}
	}
	return true
}

// writeSeedWindow emits the integer-only metadata the loaders validate; the
// shift places the pickup midpoint at the seed instant.
func writeSeedWindow(dir string, sum summary, seedEpoch int64, log io.Writer) error {
	midpoint := sum.pickupMin + (sum.pickupMax-sum.pickupMin)/2
	shift := seedEpoch - midpoint
	content := fmt.Sprintf("SOURCE_ROWS=%d\nSOURCE_PICKUP_MIN_EPOCH=%d\nSOURCE_PICKUP_MAX_EPOCH=%d\n"+
		"SOURCE_DROPOFF_MIN_EPOCH=%d\nSOURCE_DROPOFF_MAX_EPOCH=%d\nSEED_EPOCH=%d\nSHIFT_SECONDS=%d\n",
		sum.rows, sum.pickupMin, sum.pickupMax, sum.dropoffMin, sum.dropoffMax, seedEpoch, shift)
	if err := writeAtomic(filepath.Join(dir, envName), []byte(content)); err != nil {
		return err
	}
	fmt.Fprintf(log, "seed shift ready: rows=%d shift_seconds=%d seed_epoch=%d\n", sum.rows, shift, seedEpoch)
	return nil
}

func writeAtomic(path string, content []byte) error {
	tmp := path + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil { //nolint:gosec // read by other containers' users
		return err
	}
	return os.Rename(tmp, path)
}

// fileSink writes gzip files to temp names and moves them into place only
// after the whole run verified, so loaders never see a partial file.
type fileSink struct {
	dir   string
	temps []string
}

type gzFile struct {
	f  *os.File
	bw *bufio.Writer
	gz *gzip.Writer
}

func (g *gzFile) Write(p []byte) (int, error) { return g.gz.Write(p) }

func (g *gzFile) Close() error {
	if err := g.gz.Close(); err != nil {
		return err
	}
	if err := g.bw.Flush(); err != nil {
		return err
	}
	return g.f.Close()
}

func (s *fileSink) file(index int) (io.WriteCloser, error) {
	name := fileName1
	if index == 1 {
		name = fileName2
	}
	tmp := filepath.Join(s.dir, name+"."+strconv.Itoa(os.Getpid())+".tmp")
	f, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}
	s.temps = append(s.temps, tmp)
	bw := bufio.NewWriterSize(f, 1<<20)
	gz := gzip.NewWriter(bw) // zero ModTime and empty Name keep the header fixed
	return &gzFile{f: f, bw: bw, gz: gz}, nil
}

func (s *fileSink) commit() error {
	for i, tmp := range s.temps {
		name := fileName1
		if i == 1 {
			name = fileName2
		}
		if err := os.Rename(tmp, filepath.Join(s.dir, name)); err != nil {
			return err
		}
	}
	s.temps = nil
	return nil
}

func (s *fileSink) cleanup() {
	for _, tmp := range s.temps {
		os.Remove(tmp)
	}
	s.temps = nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func envInt64(key string, def int64) int64 {
	if v, err := strconv.ParseInt(os.Getenv(key), 10, 64); err == nil {
		return v
	}
	return def
}
