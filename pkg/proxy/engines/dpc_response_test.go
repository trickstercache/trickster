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

package engines

import (
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func TestPrepareDPCResponse(t *testing.T) {
	failure := errors.New("injected serialization failure")
	for _, tc := range []struct {
		name  string
		opts  timeseries.RequestOptions
		body  string
		err   error
		calls int
		reuse bool
	}{
		{"not_fallback", timeseries.RequestOptions{}, "", nil, 0, false},
		{"fast_forward", timeseries.RequestOptions{FallbackToProxyOnError: true}, "result", nil, 1, false},
		{"request_specific", timeseries.RequestOptions{FallbackToProxyOnError: true, FastForwardDisable: true, MarshalVariesByRequest: true}, "result", nil, 1, false},
		{"reuse", timeseries.RequestOptions{FallbackToProxyOnError: true, FastForwardDisable: true}, "result", nil, 1, true},
		{"empty", timeseries.RequestOptions{FallbackToProxyOnError: true, FastForwardDisable: true}, "", nil, 1, true},
		{"failed", timeseries.RequestOptions{FallbackToProxyOnError: true, FastForwardDisable: true}, "partial output", failure, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extents := timeseries.ExtentList{{Start: time.Unix(1, 0), End: time.Unix(10, 0)}}
			ts := &dataset.DataSet{ExtentList: extents}
			final := ts.Clone()
			final.SetExtents(nil)
			calls := 0
			modeler := &timeseries.Modeler{WireMarshalWriter: func(got timeseries.Timeseries, ro *timeseries.RequestOptions, status int, w io.Writer) error {
				calls++
				if got != ts || ro != &tc.opts || status != 200 {
					t.Fatal("marshal inputs changed")
				}
				buffered := tc.opts.FastForwardDisable && !tc.opts.MarshalVariesByRequest
				wantExtents := extents
				if buffered {
					wantExtents = final.Extents()
				}
				if !reflect.DeepEqual(got.Extents(), wantExtents) {
					t.Fatal("pre-render must match final wire extent visibility")
				}
				if _, err := io.WriteString(w, tc.body); err != nil {
					return err
				}
				return tc.err
			}}
			body, err := prepareDPCResponse(ts, &tc.opts, modeler, 200)
			if !errors.Is(err, tc.err) || calls != tc.calls || (body != nil) != tc.reuse {
				t.Fatalf("body=%q err=%v calls=%d", body, err, calls)
			}
			if tc.reuse && string(body) != tc.body {
				t.Fatalf("body=%q want=%q", body, tc.body)
			}
			if !reflect.DeepEqual(ts.Extents(), extents) {
				t.Fatal("cache extents were modified")
			}
		})
	}
}
