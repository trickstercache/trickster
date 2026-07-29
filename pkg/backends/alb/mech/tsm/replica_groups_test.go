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

package tsm

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	tsmerge "github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

func replicaTarget(name, group string) *pool.Target {
	status := &healthcheck.Status{}
	status.Set(healthcheck.StatusPassing)
	return pool.NewTarget(http.NotFoundHandler(), status, &stripKeysStubBackend{
		cfg: &bo.Options{Name: name, ReplicaGroup: group},
	})
}

func replicaContribution(member int, values map[int64]string) *gatherContribution {
	points := make(dataset.Points, 0, len(values))
	for timestamp, value := range values {
		points = append(points, dataset.Point{
			Epoch: epoch.Epoch(timestamp), Values: []any{value},
		})
	}
	ds := &dataset.DataSet{Results: dataset.Results{{
		SeriesList: dataset.SeriesList{{
			Header: dataset.SeriesHeader{Name: "result"},
			Points: points,
		}},
	}}}
	return &gatherContribution{
		data:           ds,
		mergeFunc:      merge.TimeseriesMergeFuncWithStrategy(nil, int(tsmerge.StrategySum)),
		batchMergeFunc: merge.TimeseriesBatchMergeFuncWithStrategy(int(tsmerge.StrategySum)),
		member:         member,
	}
}

func TestReplicaGroupCoalescingPrecedesCrossShardReduction(t *testing.T) {
	targets := pool.Targets{
		replicaTarget("a-primary", "shard-a"),
		replicaTarget("a-replica", "shard-a"),
		replicaTarget("b-primary", "shard-b"),
	}
	contributions := []*gatherContribution{
		replicaContribution(0, map[int64]string{100: "1", 200: "2"}),
		replicaContribution(1, map[int64]string{100: "9", 300: "3"}),
		replicaContribution(2, map[int64]string{100: "1"}),
	}

	logical, warnings, failed := coalesceReplicaContributions(
		context.Background(), targets, targets, contributions, "primary", 0)
	if failed || len(warnings) != 0 {
		t.Fatalf("coalesce failures = %v, warnings = %v", failed, warnings)
	}
	if len(logical) != 2 {
		t.Fatalf("logical contributions = %d, want 2", len(logical))
	}

	accumulator := merge.NewAccumulator()
	if failedMembers := mergeGatherContributions(context.Background(), accumulator, logical); len(failedMembers) != 0 {
		t.Fatalf("merge failures = %v", failedMembers)
	}
	ds := accumulator.GetTSData().(*dataset.DataSet)
	points := ds.Results[0].SeriesList[0].Points
	got := make(map[int64]string, len(points))
	for _, point := range points {
		got[int64(point.Epoch)] = point.Values[0].(string)
	}
	want := map[int64]string{100: "2", 200: "2", 300: "3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("points = %v, want %v", got, want)
	}
}

func TestReplicaGroupTolerantDedupKeepsConfiguredFirst(t *testing.T) {
	contributions := []*gatherContribution{
		replicaContribution(0, map[int64]string{100: "primary"}),
		replicaContribution(1, map[int64]string{103: "replica"}),
	}

	logical := coalesceReplicaGroup(contributions, 5)
	ds := logical.data.(*dataset.DataSet)
	points := ds.Results[0].SeriesList[0].Points
	if len(points) != 1 {
		t.Fatalf("points = %v, want one tolerant-deduped point", points)
	}
	if points[0].Epoch != epoch.Epoch(100) || points[0].Values[0] != "primary" {
		t.Fatalf("point = %+v, want configured-first replica at epoch 100", points[0])
	}
}

func TestReplicaTopologyDefaultsToDistinctMembers(t *testing.T) {
	targets := pool.Targets{
		replicaTarget("a", "a"),
		replicaTarget("b", "b"),
	}
	groups := replicaTopology(targets, targets)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
}

func TestTargetReplicaGroupIsImmutable(t *testing.T) {
	cfg := &bo.Options{Name: "primary", ReplicaGroup: "shard-a"}
	status := &healthcheck.Status{}
	target := pool.NewTarget(http.NotFoundHandler(), status,
		&stripKeysStubBackend{cfg: cfg})
	cfg.ReplicaGroup = "shard-b"
	if target.ReplicaGroup() != "shard-a" {
		t.Fatalf("target replica group changed to %q", target.ReplicaGroup())
	}
}

func TestReplicaFallbackCoversPhysicalFailure(t *testing.T) {
	targets := pool.Targets{
		replicaTarget("primary", "shard-a"),
		replicaTarget("replica", "shard-a"),
	}
	contributions := []*gatherContribution{
		nil,
		replicaContribution(1, map[int64]string{100: "4"}),
	}
	logical, warnings, failed := coalesceReplicaContributions(
		context.Background(), targets, targets, contributions, "primary", 0)
	if failed || len(warnings) != 0 || len(logical) != 1 {
		t.Fatalf("logical=%d failed=%v warnings=%v", len(logical), failed, warnings)
	}
	results := coalesceReplicaResults(targets, targets, []gatherResult{
		{statusCode: http.StatusBadGateway, failed: true},
		{statusCode: http.StatusOK, contrib: contributions[1]},
	})
	if len(results) != 1 || results[0].failed || results[0].statusCode != http.StatusOK {
		t.Fatalf("logical results = %+v", results)
	}
}

func TestReplicaCompletenessNeverPairsVariantsAcrossMembers(t *testing.T) {
	plan := &tsmerge.TSMMergePlan{
		Variants: []tsmerge.TSMQueryVariant{
			{Name: "sum"},
			{Name: "count"},
		},
		Completeness: tsmerge.TSMCompletenessAllVariants,
	}
	executions := []planVariantExecution{
		{
			results: []gatherResult{{}, {failed: true}},
			contributions: []*gatherContribution{
				replicaContribution(0, map[int64]string{100: "4"}), nil,
			},
		},
		{
			results: []gatherResult{{failed: true}, {}},
			contributions: []*gatherContribution{
				nil, replicaContribution(1, map[int64]string{100: "2"}),
			},
		},
	}
	applyPlanCompleteness(plan, executions, 2)
	for variant := range executions {
		for member, contribution := range executions[variant].contributions {
			if contribution != nil {
				t.Fatalf("variant %d member %d contribution was not excluded", variant, member)
			}
		}
	}
}

func TestReplicaPointCompletenessNeverPairsAcrossMembers(t *testing.T) {
	plan := &tsmerge.TSMMergePlan{
		OriginalQuery: "avg(result)",
		Variants: []tsmerge.TSMQueryVariant{
			{Name: "sum"},
			{Name: "count"},
		},
		Completeness: tsmerge.TSMCompletenessAllVariants,
	}
	executions := []planVariantExecution{
		{contributions: []*gatherContribution{
			replicaContribution(0, map[int64]string{100: "4"}),
			replicaContribution(1, map[int64]string{200: "8"}),
		}},
		{contributions: []*gatherContribution{
			replicaContribution(0, map[int64]string{200: "2"}),
			replicaContribution(1, map[int64]string{100: "2"}),
		}},
	}
	applyPlanPointCompleteness(plan, executions, 2)
	for variant := range executions {
		for member, contribution := range executions[variant].contributions {
			ds := contribution.data.(*dataset.DataSet)
			if len(ds.Results[0].SeriesList) != 0 {
				t.Fatalf("variant %d member %d retained an unpaired point", variant, member)
			}
		}
	}
}
