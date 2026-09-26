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

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// bounds the snapshots held in memory: the default 15 days at 1m is 21,601 steps
const maxBackfillSteps = 1_000_000

type backfillOptions struct {
	dataDir string
	out     string
	step    time.Duration
	window  time.Duration
	now     time.Time
}

func backfill(o backfillOptions, log io.Writer) error {
	if o.step < time.Second || o.step%time.Second != 0 {
		return errors.New("step must be a whole number of seconds")
	}
	if o.window < o.step {
		return errors.New("window must be at least one step")
	}
	src, err := openTrips(o.dataDir)
	if err != nil {
		return err
	}
	defer src.Close()

	began := time.Now()
	step := int64(o.step / time.Second)
	nowSec := o.now.Unix()
	end := nowSec - nowSec%step
	// the first sample is rounded up so none falls outside the retention window
	first := nowSec - int64(o.window/time.Second)
	first += (step - first%step) % step
	if steps := (end-first)/step + 1; steps > maxBackfillSteps {
		return fmt.Errorf("the window and step make %d samples per series; the most is %d", steps, maxBackfillSteps)
	}

	a := newAccumulator(src)
	var snaps []int64
	var offsets []int
	for t := first; t <= end; t += step {
		if err := a.advance(t); err != nil {
			return err
		}
		offsets = append(offsets, len(snaps))
		snaps = append(snaps, a.vals...)
		a.steps++
	}
	if len(a.vals) == 0 {
		return fmt.Errorf("no trips were picked up by %s", time.Unix(end, 0).UTC().Format(time.RFC3339))
	}

	samples, size, err := writeOpenMetrics(o.out, a, snaps, offsets, first, step)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(log, "backfilled %d samples from %s to %s into %s (%.1f MB) in %s\n", samples,
		time.Unix(first, 0).UTC().Format(time.RFC3339), time.Unix(end, 0).UTC().Format(time.RFC3339),
		o.out, float64(size)/1e6, time.Since(began).Round(time.Millisecond))
	return nil
}

func writeOpenMetrics(path string, a *accumulator, snaps []int64, offsets []int,
	first, step int64,
) (int, int64, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, 0, err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(tmp)
	w := bufio.NewWriterSize(f, 1<<20)
	var b []byte
	var samples int
	// OpenMetrics wants each metric's points contiguous and in time order
	for _, fam := range a.families {
		if len(fam.metrics) == 0 {
			continue
		}
		b = append(b[:0], "# TYPE "+fam.name+" "+string(fam.typ)+"\n# HELP "+fam.name+" "+fam.help+"\n"...)
		perPoint := 1
		if fam.typ == histogram {
			perPoint = len(distanceLabels) + 2 // buckets, count and sum
		}
		for _, m := range fam.metrics {
			for k := m.firstStep; k < len(offsets); k++ {
				b = appendMetric(b, fam, m, snaps[offsets[k]:], first+int64(k)*step)
				samples += perPoint
				if len(b) >= 1<<16 {
					if _, err := w.Write(b); err != nil {
						f.Close()
						return 0, 0, err
					}
					b = b[:0]
				}
			}
		}
		if _, err := w.Write(b); err != nil {
			f.Close()
			return 0, 0, err
		}
	}
	if _, err := w.WriteString("# EOF\n"); err != nil {
		f.Close()
		return 0, 0, err
	}
	if err := errors.Join(w.Flush(), f.Close()); err != nil {
		return 0, 0, err
	}
	st, err := os.Stat(tmp)
	if err != nil {
		return 0, 0, err
	}
	return samples, st.Size(), os.Rename(tmp, path)
}
