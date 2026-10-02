/*
 * Copyright 2018 The Trickster Authors
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

package options

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestNew(t *testing.T) {
	o := New()
	require.Equal(t, timeconv.Duration(DefaultCacheIndexReap), o.ReapInterval)
	require.Equal(t, timeconv.Duration(DefaultCacheIndexFlush), o.FlushInterval)
	require.Equal(t, timeconv.Duration(DefaultIndexExpiry), o.IndexExpiry)
	require.Equal(t, int64(DefaultCacheMaxSizeBytes), o.MaxSizeBytes)
	require.Equal(t, timeconv.Duration(DefaultScanInterval), o.ScanInterval)
	require.Equal(t, DefaultScanBatchSize, o.ScanBatchSize)
	require.Equal(t, timeconv.Duration(DefaultScanBatchPause), o.ScanBatchPause)
}

func TestEqual(t *testing.T) {
	o := New()
	require.False(t, o.Equal(nil))
	require.True(t, o.Equal(o))
	require.True(t, o.Equal(New()))
	for name, change := range map[string]func(o *Options){
		"reap interval":            func(o *Options) { o.ReapInterval++ },
		"flush interval":           func(o *Options) { o.FlushInterval++ },
		"index expiry":             func(o *Options) { o.IndexExpiry++ },
		"max size bytes":           func(o *Options) { o.MaxSizeBytes++ },
		"max size backoff bytes":   func(o *Options) { o.MaxSizeBackoffBytes++ },
		"max size objects":         func(o *Options) { o.MaxSizeObjects++ },
		"max size backoff objects": func(o *Options) { o.MaxSizeBackoffObjects++ },
		"scan interval":            func(o *Options) { o.ScanInterval++ },
		"scan batch size":          func(o *Options) { o.ScanBatchSize++ },
		"scan batch pause":         func(o *Options) { o.ScanBatchPause++ },
	} {
		o2 := New()
		change(o2)
		require.False(t, o.Equal(o2), name)
	}
}

func TestUnmarshalYAML(t *testing.T) {
	const raw = `
reap_interval: 7s
scan_interval: 12h
scan_batch_size: 64
scan_batch_pause: 250ms
`
	o := &Options{}
	require.NoError(t, yaml.Unmarshal([]byte(raw), o))
	want := New()
	want.ReapInterval = timeconv.Duration(7 * time.Second)
	want.ScanInterval = timeconv.Duration(12 * time.Hour)
	want.ScanBatchSize = 64
	want.ScanBatchPause = timeconv.Duration(250 * time.Millisecond)
	require.Equal(t, want, o)

	o = &Options{}
	require.NoError(t, yaml.Unmarshal([]byte("{}"), o))
	require.Equal(t, New(), o, "what is left out takes its default")
	// a sequence cannot be decoded into the options
	require.Error(t, yaml.Unmarshal([]byte("- boom"), o))
}
