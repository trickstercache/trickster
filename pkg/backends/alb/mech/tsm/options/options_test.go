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
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const testReplicaLabel = "isBackup"

func TestGlobalDefaults(t *testing.T) {
	for _, o := range []*Options{nil, {}} {
		require.False(t, o.IsGlobal())
		require.True(t, o.AllowsPartialResponse())
		require.Equal(t, DeduplicationPenalty, o.GetDeduplication())
		require.Equal(t, DefaultMaxSamples, o.GetMaxSamples())
		require.Equal(t, DefaultQueryTimeout, o.GetQueryTimeout())
		require.Equal(t, DefaultMaxConcurrentQueries, o.GetMaxConcurrentQueries())
	}
	o := &Options{
		Evaluation: EvaluationGlobal, Deduplication: DeduplicationChain, PartialResponse: new(false),
		MaxSamples: 10, QueryTimeout: timeconv.Duration(time.Second), MaxConcurrentQueries: 3,
	}
	require.True(t, o.IsGlobal())
	require.False(t, o.AllowsPartialResponse())
	require.Equal(t, DeduplicationChain, o.GetDeduplication())
	require.Equal(t, 10, o.GetMaxSamples())
	require.Equal(t, time.Second, o.GetQueryTimeout())
	require.Equal(t, 3, o.GetMaxConcurrentQueries())
}

func TestValidate(t *testing.T) {
	global := func(o Options) Options {
		o.Evaluation = EvaluationGlobal
		return o
	}
	tests := []struct {
		name string
		o    Options
		want error
	}{
		{name: "merge", o: Options{Evaluation: EvaluationMerge}},
		{name: "global", o: global(Options{
			ReplicaLabels: []string{testReplicaLabel}, Deduplication: DeduplicationPenalty,
			PartialResponse: new(true), MaxFetchedSeries: 5, MaxFetchedSamples: 50,
		})},
		{name: "unknown evaluation", o: Options{Evaluation: "thanos"}, want: ErrInvalidEvaluation},
		{name: "unknown deduplication", o: global(Options{Deduplication: "first"}), want: ErrInvalidDeduplication},
		{name: "metric name replica", o: global(Options{ReplicaLabels: []string{metricNameLabel}}), want: ErrInvalidReplicaLabel},
		{name: "blank replica", o: global(Options{ReplicaLabels: []string{" "}}), want: ErrInvalidReplicaLabel},
		{
			name: "repeated replica", o: global(Options{ReplicaLabels: []string{testReplicaLabel, testReplicaLabel}}),
			want: ErrInvalidReplicaLabel,
		},
		{name: "negative samples", o: global(Options{MaxSamples: -1}), want: ErrInvalidGlobalLimit},
		{name: "negative fetched samples", o: global(Options{MaxFetchedSamples: -1}), want: ErrInvalidGlobalLimit},
		{name: "replicas without global", o: Options{ReplicaLabels: []string{testReplicaLabel}}, want: ErrGlobalOnly},
		{name: "deduplication without global", o: Options{Deduplication: DeduplicationChain}, want: ErrGlobalOnly},
		{name: "partial without global", o: Options{PartialResponse: new(false)}, want: ErrGlobalOnly},
		{name: "samples without global", o: Options{MaxSamples: 1}, want: ErrGlobalOnly},
		{name: "timeout without global", o: Options{QueryTimeout: 1}, want: ErrGlobalOnly},
		{name: "concurrency without global", o: Options{MaxConcurrentQueries: 1}, want: ErrGlobalOnly},
		{name: "series without global", o: Options{MaxFetchedSeries: 1}, want: ErrGlobalOnly},
		{name: "fetched samples without global", o: Options{MaxFetchedSamples: 1}, want: ErrGlobalOnly},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.o.Validate()
			if test.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, test.want)
		})
	}
}

func TestCloneSharesNothing(t *testing.T) {
	o := Options{
		DedupToleranceMs: new(5), PartialResponse: new(true),
		ReplicaLabels: []string{testReplicaLabel},
	}
	o.ConcurrencyOptions.QueryConcurrencyLimit = new(2)
	o.ConcurrencyOptions.QueryConcurrencyMultiplier = new(3)
	c := o.Clone()
	require.Equal(t, o, c)
	*c.DedupToleranceMs, *c.PartialResponse = 6, false
	*c.ConcurrencyOptions.QueryConcurrencyLimit, *c.ConcurrencyOptions.QueryConcurrencyMultiplier = 4, 5
	c.ReplicaLabels[0] = metricNameLabel
	require.Equal(t, 5, *o.DedupToleranceMs)
	require.True(t, *o.PartialResponse)
	require.Equal(t, 2, *o.ConcurrencyOptions.QueryConcurrencyLimit)
	require.Equal(t, 3, *o.ConcurrencyOptions.QueryConcurrencyMultiplier)
	require.Equal(t, testReplicaLabel, o.ReplicaLabels[0])
}

func TestUnmarshalGlobalOptions(t *testing.T) {
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte(`
evaluation: global
replica_labels: [`+testReplicaLabel+`, replica]
deduplication: chain
partial_response: false
max_samples: 1000
query_timeout: 30s
max_concurrent_queries: 4
max_fetched_series: 100
max_fetched_samples: 100000
`), &o))
	require.NoError(t, o.Validate())
	require.Equal(t, Options{
		Evaluation: EvaluationGlobal, ReplicaLabels: []string{testReplicaLabel, "replica"},
		Deduplication: DeduplicationChain, PartialResponse: new(false), MaxSamples: 1000,
		QueryTimeout: timeconv.Duration(30 * time.Second), MaxConcurrentQueries: 4, MaxFetchedSeries: 100,
		MaxFetchedSamples: 100000,
	}, o)
}

func TestGlobalSettings(t *testing.T) {
	var none *Options
	require.Equal(t, merge.TSMGlobalSettings{
		Deduplication: DeduplicationPenalty, PartialResponse: true, MaxSamples: DefaultMaxSamples,
		QueryTimeout: DefaultQueryTimeout, MaxConcurrentQueries: DefaultMaxConcurrentQueries,
	}, none.GlobalSettings())
	o := &Options{
		ReplicaLabels: []string{testReplicaLabel}, Deduplication: DeduplicationChain, PartialResponse: new(false),
		MaxSamples: 7, QueryTimeout: timeconv.Duration(time.Second), MaxFetchedSeries: 8, MaxFetchedSamples: 9,
	}
	s := o.GlobalSettings()
	require.Equal(t, merge.TSMGlobalSettings{
		ReplicaLabels: []string{testReplicaLabel}, Deduplication: DeduplicationChain, MaxSamples: 7,
		QueryTimeout: time.Second, MaxConcurrentQueries: DefaultMaxConcurrentQueries, MaxFetchedSeries: 8,
		MaxFetchedSamples: 9,
	}, s)
	s.ReplicaLabels[0] = metricNameLabel
	require.Equal(t, testReplicaLabel, o.ReplicaLabels[0])
}
