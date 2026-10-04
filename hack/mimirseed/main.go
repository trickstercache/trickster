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

// Command mimirseed writes two regions' pod counters to the developer Mimir instances: some pods
// in both regions, and each region holding a backup copy of one of the other's pods.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	// Metric is the counter each pod reports, rising by one each second
	Metric = "tsm_pod_cpu_seconds_total"
	// BackupLabel marks a region's copy of another region's pod
	BackupLabel = "isBackup"

	defaultBackfill = 3 * time.Hour
	interval        = 15 * time.Second
	tenantHeader    = "X-Scope-OrgID"
	pushPath        = "/api/v1/push"
	readyPath       = "/ready"
	readyTimeout    = 3 * time.Minute
	// maxLiteral is the longest literal one snappy element holds
	maxLiteral = 1 << 16
)

// Series is one series a region holds.
type Series struct {
	Pod, Namespace string
	Backup         bool
}

// Topology lists each region's series: s1 is in both regions, and each region keeps a backup of one
// of the other region's pods.
var Topology = map[string][]Series{
	"east": {
		{Pod: "a1", Namespace: "alpha"},
		{Pod: "a2", Namespace: "alpha"},
		{Pod: "s1", Namespace: "shared"},
		{Pod: "w1", Namespace: "west", Backup: true},
	},
	"west": {
		{Pod: "w1", Namespace: "west"},
		{Pod: "w2", Namespace: "west"},
		{Pod: "s1", Namespace: "shared"},
		{Pod: "a1", Namespace: "alpha", Backup: true},
	},
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx)
	stop()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "mimirseed:", err)
		os.Exit(1)
	}
}

// run waits for each region, backfills it, then keeps writing samples until ctx ends
func run(ctx context.Context) error {
	urls, err := regionURLs(os.Getenv("MIMIR_URLS"))
	if err != nil {
		return err
	}
	// a multitenant Mimir needs MIMIR_TENANT; the developer Mimir stores every write as its anonymous tenant
	tenant := os.Getenv("MIMIR_TENANT")
	client := &http.Client{Timeout: 30 * time.Second}
	for region, url := range urls {
		if err := waitReady(ctx, client, url); err != nil {
			return fmt.Errorf("%s: %w", region, err)
		}
	}
	end := time.Now().Truncate(interval)
	start := end.Add(-defaultBackfill)
	for {
		for region, url := range urls {
			body := Encode(WriteRequest(Topology[region], start, end))
			if err := push(ctx, client, url, tenant, body); err != nil {
				return fmt.Errorf("%s: %w", region, err)
			}
		}
		fmt.Printf("mimirseed: wrote %s to %s\n", start.Format(time.RFC3339), end.Format(time.RFC3339))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
		start, end = end.Add(interval), time.Now().Truncate(interval)
	}
}

func regionURLs(spec string) (map[string]string, error) {
	if spec == "" {
		spec = "east=http://127.0.0.1:11080,west=http://127.0.0.1:11081"
	}
	urls := make(map[string]string)
	for part := range strings.SplitSeq(spec, ",") {
		region, url, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || Topology[region] == nil {
			return nil, fmt.Errorf("MIMIR_URLS entry %q must be region=url for a region in the topology", part)
		}
		urls[region] = strings.TrimSuffix(url, "/")
	}
	return urls, nil
}

func waitReady(ctx context.Context, client *http.Client, url string) error {
	deadline := time.Now().Add(readyTimeout)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+readyPath, nil)
		if err != nil {
			return err
		}
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s not ready after %s", url, readyTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func push(ctx context.Context, client *http.Client, url, tenant string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+pushPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	if tenant != "" {
		req.Header.Set(tenantHeader, tenant)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("push answered %s", resp.Status)
	}
	return nil
}

// WriteRequest encodes a remote-write WriteRequest of series sampled every interval from start to end.
// A counter's value is its timestamp in seconds, so every rate is one per second.
func WriteRequest(series []Series, start, end time.Time) []byte {
	var out []byte
	for _, s := range series {
		var ts []byte
		labels := [][2]string{{"__name__", Metric}, {"namespace", s.Namespace}, {"pod", s.Pod}}
		if s.Backup {
			labels = append(labels, [2]string{BackupLabel, "true"})
		}
		for _, l := range sortLabels(labels) {
			var label []byte
			label = appendBytesField(label, 1, []byte(l[0]))
			label = appendBytesField(label, 2, []byte(l[1]))
			ts = appendBytesField(ts, 1, label)
		}
		for t := start; !t.After(end); t = t.Add(interval) {
			var sample []byte
			sample = append(sample, 1<<3|1)
			sample = binary.LittleEndian.AppendUint64(sample, math.Float64bits(float64(t.Unix())))
			sample = append(sample, 2<<3)
			sample = binary.AppendUvarint(sample, uint64(t.UnixMilli()))
			ts = appendBytesField(ts, 2, sample)
		}
		out = appendBytesField(out, 1, ts)
	}
	return out
}

func sortLabels(labels [][2]string) [][2]string {
	for i := 1; i < len(labels); i++ {
		for j := i; j > 0 && labels[j][0] < labels[j-1][0]; j-- {
			labels[j], labels[j-1] = labels[j-1], labels[j]
		}
	}
	return labels
}

func appendBytesField(b []byte, field uint64, v []byte) []byte {
	b = binary.AppendUvarint(b, field<<3|2)
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}

// Encode returns data in the snappy block format as literals only, which every snappy decoder reads
func Encode(data []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(len(data)))
	for len(data) > 0 {
		n := min(len(data), maxLiteral)
		// a literal longer than 60 bytes gives its length less one in the two bytes that follow
		switch {
		case n <= 60:
			out = append(out, byte(n-1)<<2) //nolint:gosec // n is at most 60 here
		default:
			out = append(out, 61<<2)
			out = binary.LittleEndian.AppendUint16(out, uint16(n-1))
		}
		out = append(out, data[:n]...)
		data = data[n:]
	}
	return out
}
