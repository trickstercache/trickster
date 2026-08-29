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

package flightsql

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	checksum "github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	dsarrow "github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/arrow"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/flight"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// DeltaConfig enables the delta-proxy-cache tier for statement queries. When
// configured, statements the analyzer classifies as delta-cacheable — and
// whose Arrow schemas the dataset model can represent — are cached by extent,
// with only missing sub-ranges fetched from the upstream; everything else
// falls open to the verbatim-IPC object tier or to a plain proxy.
type DeltaConfig struct {
	// Analyzer classifies statements for the upstream's SQL dialect.
	Analyzer sqlanalyzer.DialectAnalyzer
	// CacheClient resolves the delta tier's cache at call time.
	CacheClient func() cache.Cache
	// CacheTTL bounds delta entry lifetimes (typically the backend's
	// timeseries TTL). Zero uses the engine default.
	CacheTTL time.Duration
	// MaxObjectSize rejects oversized entries when positive.
	MaxObjectSize int64
	// BackfillTolerance widens the volatile tail excluded from cache storage.
	BackfillTolerance time.Duration
}

// WithDeltaCache enables the delta tier on a Server.
func WithDeltaCache(cfg DeltaConfig) ServerOption {
	return func(s *Server) {
		if cfg.Analyzer != nil && cfg.CacheClient != nil {
			s.deltaConfig = &cfg
		}
	}
}

// deltaPayload is the delta tier's cache representation: the response's
// serialized Arrow schema alongside the tag-partitioned dataset, so merged
// results are rebuilt into batches conforming to the original schema. Raw
// carries verbatim IPC bytes on paths that bypass dataset modeling (proxied
// originals, object-tier fallbacks); Raw is never stored in the delta cache.
type deltaPayload struct {
	Schema []byte
	DS     *dataset.DataSet
	Raw    []byte
	// status preserves the object tier's lookup status through the
	// ObjectFallback path.
	status cachestatus.LookupStatus
}

// deltaCodec serializes delta payloads as a length-prefixed schema followed
// by the msgpack dataset encoding.
type deltaCodec struct{}

func (deltaCodec) Marshal(p *deltaPayload) ([]byte, error) {
	if p == nil || p.DS == nil || p.Raw != nil {
		return nil, errors.New("flight delta payload is not cacheable")
	}
	dsBytes, err := dataset.MarshalDataSet(p.DS, nil, 0)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 4+len(p.Schema)+len(dsBytes))
	// #nosec G115 -- a serialized Arrow schema is far below 4GiB.
	binary.BigEndian.PutUint32(out[:4], uint32(len(p.Schema)))
	copy(out[4:], p.Schema)
	copy(out[4+len(p.Schema):], dsBytes)
	return out, nil
}

func (deltaCodec) Unmarshal(data []byte) (*deltaPayload, error) {
	if len(data) < 4 {
		return nil, errors.New("truncated flight delta payload")
	}
	schemaLen := int(binary.BigEndian.Uint32(data[:4]))
	if schemaLen < 0 || schemaLen > len(data)-4 {
		return nil, errors.New("invalid flight delta schema length")
	}
	ts, err := dataset.UnmarshalDataSet(data[4+schemaLen:], nil)
	if err != nil {
		return nil, err
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		return nil, errors.New("invalid flight delta dataset")
	}
	return &deltaPayload{Schema: append([]byte(nil), data[4:4+schemaLen]...), DS: ds}, nil
}

func (deltaCodec) Size(p *deltaPayload) int {
	if p == nil {
		return 0
	}
	size := len(p.Schema) + len(p.Raw)
	if p.DS != nil {
		size += int(p.DS.Size())
	}
	return size
}

// deltaRunner routes statement queries across the delta, object, and proxy
// tiers.
type deltaRunner struct {
	cfg    DeltaConfig
	engine *nativedelta.Engine[*deltaPayload]
}

func newDeltaRunner(cfg DeltaConfig, keyPrefix string) *deltaRunner {
	engineCfg := nativedelta.Config{
		Protocol:      "flightsql",
		BackendName:   keyPrefix,
		CacheClient:   cfg.CacheClient,
		CacheTTL:      cfg.CacheTTL,
		MaxObjectSize: cfg.MaxObjectSize,
	}
	if engineCfg.CacheTTL <= 0 {
		engineCfg.CacheTTL = DefaultCacheTTL
	}
	return &deltaRunner{cfg: cfg, engine: nativedelta.New(engineCfg, deltaCodec{})}
}

// serve executes one statement query through the three-tier cache.
func (d *deltaRunner) serve(ctx context.Context, s *Server,
	query string,
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	now := time.Now()
	analysis := d.cfg.Analyzer.Analyze(query, now)
	// nondeterministic statements are never cached; the substring check backs
	// up the analyzer for statements it cannot parse
	if analysis.Mode == sqlanalyzer.CacheModeNone ||
		(analysis.Plan == nil && volatileQuery(query)) {
		b, err := s.upstream.Execute(ctx, query)
		if err != nil {
			return nil, nil, fmt.Errorf("upstream execute: %w", err)
		}
		return streamIPCBytes(ctx, b)
	}
	if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan == nil {
		b, _, err := s.objectTier(ctx, query)
		if err != nil {
			return nil, nil, err
		}
		return streamIPCBytes(ctx, b)
	}

	plan := analysis.Plan
	trq := planTimeRangeQuery(plan)
	baseKey := s.tenantKey(ctx) + ":dpc:" +
		checksum.Checksum(plan.CanonicalSQL+"|"+plan.IdentitySuffix)
	payload, _, err := d.engine.ExecuteDelta(nativedelta.DeltaRequest[*deltaPayload]{
		Key:         baseKey,
		FallbackKey: baseKey + ":fallback",
		EmptyKey:    baseKey + ":empty",
		Plan:        plan,
		Now:         now,
		Ops:         d.ops(ctx, s, query, plan, trq),
	})
	if err != nil {
		return nil, nil, err
	}
	return d.respond(ctx, payload)
}

// respond streams a delta payload: verbatim bytes when present, otherwise the
// dataset rebuilt into batches conforming to the preserved schema.
func (d *deltaRunner) respond(ctx context.Context,
	payload *deltaPayload,
) (*arrow.Schema, <-chan flight.StreamChunk, error) {
	if payload == nil {
		return nil, nil, errors.New("nil flight delta payload")
	}
	if payload.Raw != nil {
		return streamIPCBytes(ctx, payload.Raw)
	}
	schema, err := flight.DeserializeSchema(payload.Schema, memory.DefaultAllocator)
	if err != nil {
		return nil, nil, fmt.Errorf("flight delta schema: %w", err)
	}
	records, err := dsarrow.ToRecords(schema, payload.DS)
	if err != nil {
		return nil, nil, fmt.Errorf("flight delta rebuild: %w", err)
	}
	ipcBytes, err := EncodeRecords(schema, records)
	for _, record := range records {
		record.Release()
	}
	if err != nil {
		return nil, nil, err
	}
	return streamIPCBytes(ctx, ipcBytes)
}

// ops builds the engine callbacks for one request.
func (d *deltaRunner) ops(ctx context.Context, s *Server, query string,
	plan *sqlanalyzer.QueryPlan, trq *timeseries.TimeRangeQuery,
) nativedelta.DeltaOps[*deltaPayload] {
	return nativedelta.DeltaOps[*deltaPayload]{
		Fetch: func(statement string) (*deltaPayload, error) {
			b, err := s.upstream.Execute(ctx, statement)
			if err != nil {
				return nil, fmt.Errorf("upstream execute: %w", err)
			}
			return decodeToPayload(b, plan, trq)
		},
		FetchOriginal: func() (*deltaPayload, error) {
			b, err := s.upstream.Execute(ctx, query)
			if err != nil {
				return nil, fmt.Errorf("upstream execute: %w", err)
			}
			return &deltaPayload{Raw: b}, nil
		},
		Merge: mergePayloads,
		CropResponse: func(payload *deltaPayload,
			requested timeseries.Extent,
		) (*deltaPayload, error) {
			if payload == nil || payload.DS == nil {
				return nil, errors.New("nil cached flight delta payload")
			}
			cropped, ok := payload.DS.CroppedClone(requested).(*dataset.DataSet)
			if !ok {
				return nil, errors.New("invalid cropped flight delta dataset")
			}
			return &deltaPayload{Schema: payload.Schema, DS: cropped}, nil
		},
		Finalize: func(merged *deltaPayload, allExtents timeseries.ExtentList,
			requested timeseries.Extent, now time.Time,
		) (*deltaPayload, *deltaPayload, timeseries.ExtentList, error) {
			return d.finalize(merged, allExtents, requested, plan, now)
		},
		ObjectFallback: func() (*deltaPayload, cachestatus.LookupStatus, error) {
			b, lookupStatus, err := s.objectTier(ctx, query)
			if err != nil {
				return nil, lookupStatus, err
			}
			return &deltaPayload{Raw: b, status: lookupStatus}, lookupStatus, nil
		},
	}
}

// finalize crops the response to the request window and trims the volatile
// tail from the stored entry so still-filling buckets are refetched.
func (d *deltaRunner) finalize(merged *deltaPayload, allExtents timeseries.ExtentList,
	requested timeseries.Extent, plan *sqlanalyzer.QueryPlan, now time.Time,
) (*deltaPayload, *deltaPayload, timeseries.ExtentList, error) {
	if merged == nil || merged.DS == nil {
		return nil, nil, nil, errors.New("nil merged flight delta payload")
	}
	merged.DS.ExtentList = allExtents.Clone()
	response, ok := merged.DS.CroppedClone(requested).(*dataset.DataSet)
	if !ok {
		return nil, nil, nil, errors.New("invalid cropped flight delta dataset")
	}

	volatileWindow := max(d.cfg.BackfillTolerance, plan.BackfillTolerance)
	if plan.UpperBound == nil {
		// open-ended queries run to the present; at least the final bucket is
		// still filling
		volatileWindow = max(volatileWindow, plan.Step)
	}
	cacheExtents := nativedelta.StableExtents(allExtents, plan.Step, volatileWindow, now)
	retainedDS := merged.DS
	if len(cacheExtents) == 0 {
		retainedDS, ok = merged.DS.CroppedClone(timeseries.Extent{
			Start: time.Unix(0, 0), End: time.Unix(0, 0),
		}).(*dataset.DataSet)
	} else if !cacheExtents[len(cacheExtents)-1].End.Equal(allExtents[len(allExtents)-1].End) {
		retainedDS, ok = merged.DS.CroppedClone(timeseries.Extent{
			Start: cacheExtents[0].Start, End: cacheExtents[len(cacheExtents)-1].End,
		}).(*dataset.DataSet)
	}
	if !ok {
		return nil, nil, nil, errors.New("invalid retained flight delta dataset")
	}
	retainedDS.ExtentList = cacheExtents.Clone()
	retained := &deltaPayload{Schema: merged.Schema, DS: retainedDS}
	return &deltaPayload{Schema: merged.Schema, DS: response}, retained, cacheExtents, nil
}

// decodeToPayload converts an upstream IPC response into the delta cache
// representation, failing to the object tier (via ErrUnmergeable) when the
// schema or data cannot be modeled.
func decodeToPayload(ipcBytes []byte, plan *sqlanalyzer.QueryPlan,
	trq *timeseries.TimeRangeQuery,
) (*deltaPayload, error) {
	schema, records, err := DecodeRecords(ipcBytes)
	defer func() {
		for _, record := range records {
			record.Release()
		}
	}()
	if err != nil {
		return nil, nativedelta.Unmergeable(err)
	}
	if !dsarrow.Representable(schema, plan.OutputColumn) {
		return nil, nativedelta.Unmergeable(
			fmt.Errorf("%w: %v", dsarrow.ErrNotRepresentable, schema))
	}
	ds, err := dsarrow.FromRecords(schema, records, trq)
	if err != nil {
		return nil, nativedelta.Unmergeable(err)
	}
	return &deltaPayload{
		Schema: flight.SerializeSchema(schema, memory.DefaultAllocator),
		DS:     ds,
	}, nil
}

// mergePayloads combines the cached payload and fetched parts; schema drift
// between parts is unmergeable.
func mergePayloads(parts []*deltaPayload) (*deltaPayload, error) {
	if len(parts) == 0 || parts[0] == nil || parts[0].DS == nil {
		return nil, errors.New("empty flight delta merge")
	}
	base := parts[0]
	others := make([]timeseries.Timeseries, 0, len(parts)-1)
	for _, part := range parts[1:] {
		if part == nil || part.DS == nil {
			return nil, errors.New("nil flight delta merge part")
		}
		if string(part.Schema) != string(base.Schema) {
			return nil, errors.New("flight delta schema changed between fetches")
		}
		others = append(others, part.DS)
	}
	if len(others) > 0 {
		base.DS.Merge(true, others...)
	}
	return base, nil
}

// planTimeRangeQuery carries the plan's response-shape facts to the dataset
// conversion: the output timestamp column and the tag columns that partition
// series.
func planTimeRangeQuery(plan *sqlanalyzer.QueryPlan) *timeseries.TimeRangeQuery {
	trq := &timeseries.TimeRangeQuery{
		Statement: plan.CanonicalSQL,
		Step:      plan.Step,
		TimestampDefinition: timeseries.FieldDefinition{
			Name: plan.OutputColumn, Role: timeseries.RoleTimestamp,
		},
	}
	trq.TagFieldDefintions = make(timeseries.FieldDefinitions, len(plan.GroupColumns))
	for i, name := range plan.GroupColumns {
		trq.TagFieldDefintions[i] = timeseries.FieldDefinition{
			Name: name, Role: timeseries.RoleTag,
		}
	}
	return trq
}
