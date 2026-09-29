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

package registry

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestProviderStepAlignments(t *testing.T) {
	const all = timeseries.StepAlignmentAll
	tests := []struct {
		provider       string
		supported, def timeseries.StepAlignment
	}{
		{providers.Prometheus, timeseries.StepAlignmentOff | timeseries.StepAlignmentTruncate |
			timeseries.StepAlignmentDrop | timeseries.StepAlignmentPartialEnd, timeseries.StepAlignmentPartialEnd},
		{
			providers.Graphite, timeseries.StepAlignmentOff | timeseries.StepAlignmentTruncate,
			timeseries.StepAlignmentTruncate,
		},
		{providers.InfluxDB, all, timeseries.StepAlignmentPartialEnd},
		{providers.GreptimeDB, all, timeseries.StepAlignmentPartialEnd},
		{providers.ClickHouse, all, timeseries.StepAlignmentDrop},
		{providers.Druid, all, timeseries.StepAlignmentPartial},
		{providers.MySQL, all, timeseries.StepAlignmentDrop},
		{providers.Postgres, all, timeseries.StepAlignmentDrop},
		{providers.TimescaleDB, all, timeseries.StepAlignmentDrop},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			client := newTestClient(t, test.provider, nil)
			sa, ok := client.(timeseries.StepAligner)
			if !ok {
				t.Fatal("the client does not declare its step alignment modes")
			}
			supported, def := sa.StepAlignments()
			if supported != test.supported || def != test.def {
				t.Errorf("got (%s; %s) want (%s; %s)", supported, def, test.supported, test.def)
			}
			if !def.IsMode() || supported&def == 0 {
				t.Errorf("the default %s must be one supported mode", def)
			}
			// the default is what the provider does today, so configuring it must be accepted
			o := bo.New()
			o.Name, o.Provider, o.StepAlignment = "test", test.provider, def
			if err := backends.ValidateStepAlignment(client, o); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestEveryTimeSeriesProviderAppliesOff(t *testing.T) {
	for _, provider := range []string{
		providers.Prometheus, providers.Graphite, providers.InfluxDB, providers.ClickHouse,
		providers.Druid, providers.MySQL, providers.Postgres, providers.TimescaleDB, providers.GreptimeDB,
	} {
		t.Run(provider, func(t *testing.T) {
			o := bo.New()
			o.Name, o.Provider, o.StepAlignment = "test", provider, timeseries.StepAlignmentOff
			if err := backends.ValidateStepAlignment(newTestClient(t, provider, nil), o); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestPrometheusFastForwardDisableDefaultsToTruncate(t *testing.T) {
	o := bo.New()
	o.FastForwardDisable = true
	client := newTestClient(t, providers.Prometheus, o)
	if _, def := client.(timeseries.StepAligner).StepAlignments(); def != timeseries.StepAlignmentTruncate {
		t.Errorf("got %s want truncate", def)
	}
}

func TestValidateStepAlignment(t *testing.T) {
	tests := []struct {
		name, provider string
		mode           timeseries.StepAlignment
		want           error
	}{
		{"unset", providers.ReverseProxyCache, 0, nil},
		{"applied", providers.Prometheus, timeseries.StepAlignmentTruncate, nil},
		{
			"supported but not applied yet", providers.Prometheus, timeseries.StepAlignmentDrop,
			bo.ErrStepAlignmentNotImplemented,
		},
		{"unsupported", providers.Graphite, timeseries.StepAlignmentPartial, bo.ErrUnsupportedStepAlignment},
		{"every mode on the http paths", providers.InfluxDB, timeseries.StepAlignmentPartial, nil},
		{
			"not a time series provider", providers.ReverseProxyCache, timeseries.StepAlignmentTruncate,
			bo.ErrUnsupportedStepAlignment,
		},
		// an alb's members are validated against its mode once its pool is known
		{"alb", providers.ALB, timeseries.StepAlignmentTruncate, nil},
		// options built in code bypass YAML's single-name decoding
		{
			"two supported and applied modes", providers.Prometheus,
			timeseries.StepAlignmentTruncate | timeseries.StepAlignmentPartialEnd, timeseries.ErrInvalidStepAlignment,
		},
		{
			"two modes, one applied", providers.Prometheus,
			timeseries.StepAlignmentTruncate | timeseries.StepAlignmentDrop, timeseries.ErrInvalidStepAlignment,
		},
		{"an unknown bit", providers.Prometheus, 1 << 7, timeseries.ErrInvalidStepAlignment},
		{
			"a mode and an unknown bit", providers.Prometheus,
			timeseries.StepAlignmentTruncate | 1<<6, timeseries.ErrInvalidStepAlignment,
		},
		{
			"several modes on an alb", providers.ALB,
			timeseries.StepAlignmentTruncate | timeseries.StepAlignmentOff, timeseries.ErrInvalidStepAlignment,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			o := bo.New()
			o.Name, o.Provider, o.StepAlignment = "test", test.provider, test.mode
			var client backends.Backend
			if test.provider != providers.ALB {
				client = newTestClient(t, test.provider, nil)
			}
			err := backends.ValidateStepAlignment(client, o)
			if test.want == nil {
				if err != nil {
					t.Error(err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Errorf("got %v want %v", err, test.want)
			}
			if errors.Is(err, timeseries.ErrInvalidStepAlignment) && !strings.Contains(err.Error(),
				fmt.Sprintf("%#x is not exactly one mode", uint8(test.mode))) {
				t.Errorf("the error should name the value: %v", err)
			}
		})
	}
}

func TestStepAlignmentProfile(t *testing.T) {
	const promApplied = timeseries.StepAlignmentOff | timeseries.StepAlignmentTruncate |
		timeseries.StepAlignmentPartialEnd
	withMode := func(mode timeseries.StepAlignment) *bo.Options {
		o := bo.New()
		o.StepAlignment = mode
		return o
	}
	fastForwardDisabled := bo.New()
	fastForwardDisabled.FastForwardDisable = true
	tests := []struct {
		name, provider        string
		o                     *bo.Options
		effective, applicable timeseries.StepAlignment
	}{
		{"the provider default", providers.Prometheus, nil, timeseries.StepAlignmentPartialEnd, promApplied},
		{
			"a configured mode", providers.Prometheus, withMode(timeseries.StepAlignmentOff),
			timeseries.StepAlignmentOff, promApplied,
		},
		{
			"fast_forward_disable", providers.Prometheus, fastForwardDisabled,
			timeseries.StepAlignmentTruncate, promApplied,
		},
		{"a native listener's provider", providers.MySQL, nil, timeseries.StepAlignmentDrop, timeseries.StepAlignmentAll},
		// a profile describes what reaches the backend over HTTP, as an ALB's requests do
		{"every mode", providers.InfluxDB, nil, timeseries.StepAlignmentPartialEnd, timeseries.StepAlignmentAll},
		{"not a time series provider", providers.ReverseProxyCache, nil, 0, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			effective, applicable := backends.StepAlignmentProfile(newTestClient(t, test.provider, test.o))
			if effective != test.effective || applicable != test.applicable {
				t.Errorf("got (%s; %s) want (%s; %s)", effective, applicable, test.effective, test.applicable)
			}
		})
	}
	if effective, applicable := backends.StepAlignmentProfile(nil); effective != 0 || applicable != 0 {
		t.Errorf("a nil backend has no profile, got (%s; %s)", effective, applicable)
	}
}

func newTestClient(t *testing.T, provider string, o *bo.Options) backends.Backend {
	t.Helper()
	if o == nil {
		o = bo.New()
	}
	o.Name, o.Provider, o.OriginURL = "test", provider, "http://127.0.0.1"
	f := SupportedProviders()[provider]
	if f == nil {
		t.Fatalf("provider %s is not registered", provider)
	}
	client, err := f("test", o, http.NewServeMux(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}
