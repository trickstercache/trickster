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

package mysql

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	checksum "github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/prometheus/client_golang/prometheus"
	vtmysql "vitess.io/vitess/go/mysql"
	"vitess.io/vitess/go/mysql/collations"
	"vitess.io/vitess/go/mysql/collations/colldata"
	"vitess.io/vitess/go/mysql/decimal"
	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
	"vitess.io/vitess/go/vt/sqlparser"
)

const (
	cacheIdentityVersion byte = 1
	mysqlDialect              = "mysql"
	cacheModeOPC              = "opc"
	cacheModeDPC              = "dpc"
	cacheModeDPCFallback      = "dpc-fallback"
	// off and partial buckets keep their own objects, so one stored for another TTL never answers them
	cacheModeOff                  = "off"
	cacheModePartial              = "partial"
	metricMethodQuery             = "QUERY"
	metricPathQuery               = "query"
	metricHTTPStatusOK            = "200"
	metricHTTPStatusInternalError = "500"
)

type analysisMetricKey struct {
	mode   sqlanalyzer.CacheMode
	reason string
}

var analysisMetricKeys = [...]analysisMetricKey{
	{sqlanalyzer.CacheModeNone, string(sqlanalyzer.ReasonNondeterministic)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonInvalidSQL)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonUnsupportedStatement)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonNotTimeRange)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonUnsupportedBucket)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonUnsafePredicate)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonAmbiguousTimeAxis)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonUnsupportedGrouping)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonUnsupportedFormat)},
	{sqlanalyzer.CacheModeObject, string(sqlanalyzer.ReasonUnsupportedLimit)},
	{sqlanalyzer.CacheModeDelta, string(sqlanalyzer.ReasonDeltaCacheable)},
}

type cacheMetricKey struct {
	mode   sqlanalyzer.CacheMode
	status cachestatus.LookupStatus
}

type cacheMetricHandles struct {
	native   prometheus.Counter
	requests prometheus.Counter
	elements prometheus.Counter
	duration prometheus.Observer
}

type protocolMetricHandles struct {
	connectLatency prometheus.Observer
	queryLatency   prometheus.Observer
	analysis       map[analysisMetricKey]prometheus.Counter
	cache          map[cacheMetricKey]cacheMetricHandles
}

// resultCodec is the nativedelta payload codec for MySQL wire results: the
// two StatusFlags bytes followed by the vitess proto encoding.
type resultCodec struct{}

func (c resultCodec) Marshal(result *sqltypes.Result) ([]byte, error) {
	return c.AppendMarshal(nil, result)
}

// AppendMarshal writes the proto encoding in place after the status flags, where marshaling it on
// its own would take a copy to put the flags first
func (resultCodec) AppendMarshal(out []byte, result *sqltypes.Result) ([]byte, error) {
	if result == nil {
		return nil, errors.New("nil MySQL cache result")
	}
	protoResult := sqltypes.ResultToProto3(result)
	size := protoResult.SizeVT()
	out = binary.BigEndian.AppendUint16(slices.Grow(out, 2+size), result.StatusFlags)
	start := len(out)
	out = out[:start+size]
	if _, err := protoResult.MarshalToSizedBufferVT(out[start:]); err != nil {
		return nil, err
	}
	return out, nil
}

func (resultCodec) Unmarshal(data []byte) (*sqltypes.Result, error) {
	if len(data) < 2 {
		return nil, errors.New("truncated MySQL cache payload")
	}
	protoResult := &querypb.QueryResult{}
	if err := protoResult.UnmarshalVT(data[2:]); err != nil {
		return nil, err
	}
	result := sqltypes.Proto3ToResult(protoResult)
	result.StatusFlags = binary.BigEndian.Uint16(data[:2])
	return result, nil
}

func (resultCodec) Size(result *sqltypes.Result) int {
	return estimateResultSize(result)
}

// estimateResultSize approximates the heap retained by a typed memory-cache
// result. Slice capacities are intentional: the cache retains the backing
// arrays, not just their populated elements.
func estimateResultSize(result *sqltypes.Result) int {
	if result == nil {
		return 0
	}
	size := uint64(unsafe.Sizeof(*result))
	size += uint64(cap(result.Fields)) * uint64(unsafe.Sizeof((*querypb.Field)(nil)))
	for _, field := range result.Fields {
		if field == nil {
			continue
		}
		size += uint64(unsafe.Sizeof(*field))
		size += uint64(len(field.Name) + len(field.Table) + len(field.OrgTable) +
			len(field.Database) + len(field.OrgName) + len(field.ColumnType))
	}
	size += uint64(cap(result.Rows)) * uint64(unsafe.Sizeof(sqltypes.Row(nil)))
	for _, row := range result.Rows {
		size += uint64(cap(row)) * uint64(unsafe.Sizeof(sqltypes.Value{}))
		for _, value := range row {
			// #nosec G115 -- Value.Len reports the nonnegative length of its byte slice.
			size += uint64(value.Len())
		}
	}
	size += uint64(len(result.SessionStateChanges) + len(result.Info))
	return saturatedSize(size)
}

func saturatedSize(size uint64) int {
	if size > uint64(math.MaxInt) {
		return math.MaxInt
	}
	return int(size)
}

func (h *protocolHandler) cacheEligible(session *upstreamSession) bool {
	if h.config.ProxyOnly || h.cacheClient() == nil || session == nil {
		return false
	}
	session.mtx.Lock()
	eligible := !session.inTx && !session.cacheUnsafe
	session.mtx.Unlock()
	return eligible
}

func (h *protocolHandler) cacheClient() cache.Cache {
	if h.config.CacheProvider != nil {
		return h.config.CacheProvider.Cache()
	}
	return h.config.Cache
}

func (h *protocolHandler) executeCached(c *vtmysql.Conn, session *upstreamSession,
	query string, analysis sqlanalyzer.Analysis,
) (*sqltypes.Result, *renderBuffers, cachestatus.LookupStatus, error) {
	// returns the result and, when its rows were rendered for this request alone, the buffers that
	// hold them, which the caller releases once the result is written
	switch analysis.Mode {
	case sqlanalyzer.CacheModeDelta:
		if h.unaligned(analysis) {
			result, lookup, err := h.executeObject(c, session, query, true)
			return result, nil, lookup, err
		}
		if analysis.Plan != nil {
			answer, lookup, err := h.executeDelta(c, session, query, analysis.Plan)
			if err != nil || answer.Delta == nil {
				return answer.Object, nil, lookup, err
			}
			buffers := getRenderBuffers()
			result, err := h.renderDelta(answer.Delta, analysis.Plan, buffers)
			if err != nil {
				buffers.release()
				// rows that cannot be rendered are no reason to fail the client's statement, nor to keep
				h.observeRewriteFailure("render_delta_rows")
				h.deltaEngine().RemoveDelta(h.planCacheKey(c, session, cacheModeDPC, analysis.Plan))
				result, err = h.executeOrigin(session, query)
				return result, nil, cachestatus.LookupStatusProxyOnly, err
			}
			return result, buffers, lookup, nil
		}
	case sqlanalyzer.CacheModeObject:
		result, lookup, err := h.executeObject(c, session, query, false)
		return result, nil, lookup, err
	}
	return nil, nil, cachestatus.LookupStatusProxyOnly, errors.New("uncacheable MySQL query")
}

func (h *protocolHandler) unaligned(analysis sqlanalyzer.Analysis) bool {
	// off answers a delta plan with the origin's result to the client's statement, keyed on its raw range
	return analysis.Mode == sqlanalyzer.CacheModeDelta &&
		nativedelta.RequestStepAlignment(h.config.StepAlignment, analysis.Plan) == timeseries.StepAlignmentOff
}

func (h *protocolHandler) executeObject(c *vtmysql.Conn, session *upstreamSession,
	query string, unaligned bool,
) (*sqltypes.Result, cachestatus.LookupStatus, error) {
	engine, ttl := cacheModeOPC, time.Duration(0)
	if unaligned {
		engine, ttl = cacheModeOff, timeseries.StepAlignmentOffTTL
	}
	key := h.queryCacheKey(c, session, engine, strings.TrimSpace(query))
	return h.deltaEngine().ExecuteObject(key, ttl, func() (*sqltypes.Result, error) {
		return h.executeOrigin(session, query)
	})
}

func (h *protocolHandler) executeDelta(c *vtmysql.Conn, session *upstreamSession,
	query string, plan *sqlanalyzer.QueryPlan,
) (nativedelta.Outcome[*sqltypes.Result], cachestatus.LookupStatus, error) {
	ops := nativedelta.DeltaOps[*sqltypes.Result]{
		Fetch: func(statement string) (*nativedelta.Delta, error) {
			return h.executeOriginRows(session, statement, plan)
		},
		FetchOriginal: func() (*sqltypes.Result, error) {
			return h.executeOrigin(session, query)
		},
		ObjectFallback: func() (*sqltypes.Result, cachestatus.LookupStatus, error) {
			return h.executeObject(c, session, query, false)
		},
		SameHeader: sameResultHeader,
		// the session's one upstream connection runs its fetches in turn, so none is started early
		FetchPartial: func(_ context.Context, statement string, ttl time.Duration,
		) (*sqltypes.Result, cachestatus.LookupStatus, error) {
			key := h.queryCacheKey(c, session, cacheModePartial, strings.TrimSpace(statement))
			return h.deltaEngine().ExecuteObject(key, ttl, func() (*sqltypes.Result, error) {
				return h.executeOrigin(session, statement)
			})
		},
		Model: func(result *sqltypes.Result) (*nativedelta.Delta, error) {
			return h.modelResult(result, plan)
		},
	}
	if h.config.DoesShard {
		ops.Shard = func(missing timeseries.ExtentList) timeseries.ExtentList {
			fetchExtents := make(timeseries.ExtentList, 0, len(missing))
			for _, extent := range missing {
				fetchExtents = append(fetchExtents, timeseries.ExtentList{extent}.Splice(plan.Step, plan.Phase,
					h.config.ShardMaxRange, h.config.ShardStep, h.config.ShardMaxPoints)...)
			}
			return fetchExtents
		}
	}
	return h.deltaEngine().ExecuteDelta(nativedelta.DeltaRequest[*sqltypes.Result]{
		Key:         h.planCacheKey(c, session, cacheModeDPC, plan),
		FallbackKey: h.planCacheKey(c, session, cacheModeDPCFallback, plan),
		Statement:   query, StepAlignment: nativedelta.RequestStepAlignment(h.config.StepAlignment, plan),
		Plan: plan, Now: time.Now(),
		// vitess delta plans always carry closed bounds; open-ended plans
		// proxy rather than run to the present
		RequireUpperBound: true,
		Ops:               ops,
	})
}

func (h *protocolHandler) planCacheKey(c *vtmysql.Conn, session *upstreamSession, mode string,
	plan *sqlanalyzer.QueryPlan,
) string {
	// the second field once held directives, which keys no longer do; it stays empty so keys don't change
	return h.queryCacheKey(c, session, mode, plan.CanonicalSQL, "")
}

func (h *protocolHandler) executeOrigin(session *upstreamSession,
	query string,
) (*sqltypes.Result, error) {
	return h.fetchOrigin(session, query, nil)
}

func (h *protocolHandler) executeOriginRows(session *upstreamSession, statement string,
	plan *sqlanalyzer.QueryPlan,
) (*nativedelta.Delta, error) {
	var sink *rowSink
	result, err := h.fetchOrigin(session, statement, func(fields []*querypb.Field) (*rowSink, error) {
		var err error
		sink, err = h.newRowSink(plan, fields)
		return sink, err
	})
	if err != nil {
		return nil, err
	}
	return sink.finish(result.StatusFlags)
}

func (h *protocolHandler) modelResult(result *sqltypes.Result, plan *sqlanalyzer.QueryPlan,
) (*nativedelta.Delta, error) {
	// an object-tier result's rows, modeled as a fetch's are
	sink, err := h.newRowSink(plan, result.Fields)
	if err != nil {
		return nil, err
	}
	for _, row := range result.Rows {
		if err := sink.row(row); err != nil {
			return nil, err
		}
	}
	return sink.finish(result.StatusFlags)
}

func (h *protocolHandler) fetchOrigin(session *upstreamSession, query string,
	sinkFor func([]*querypb.Field) (*rowSink, error),
) (*sqltypes.Result, error) {
	if err := h.connectSession(session); err != nil {
		return nil, err
	}
	session.mtx.Lock()
	upstream := session.conn
	session.mtx.Unlock()
	var result *sqltypes.Result
	err := h.runOriginQuery(session, upstream, parsedQuery{statementType: sqlparser.StmtSelect},
		func() error {
			var fetchErr error
			result, fetchErr = h.collectOriginResult(session, upstream, query, sinkFor)
			return fetchErr
		})
	return result, err
}

func (h *protocolHandler) collectOriginResult(session *upstreamSession, upstream *vtmysql.Conn,
	query string, sinkFor func([]*querypb.Field) (*rowSink, error),
) (*sqltypes.Result, error) {
	if err := upstream.ExecuteStreamFetch(query); err != nil {
		// The origin rejected the statement before opening a result stream, so
		// it answered with a complete ERR packet and stayed synchronized.
		return nil, err
	}
	// Every failure from here on abandons a stream the origin is still
	// sending, which leaves the connection desynchronized.
	result, sinkErr, err := h.collectStreamedResult(session, upstream, sinkFor)
	if err == nil && sinkErr != nil {
		// rows the sink cannot model were still read to the end, so the stream stays in step
		upstream.CloseResult()
		return nil, sinkErr
	}
	if err != nil {
		// The stream is abandoned partway through, and CloseResult would
		// keep reading until the origin sends a remainder it may never send.
		// The connection is already unusable, so close it instead of draining.
		upstream.Close()
		return nil, upstreamFatal{err: err}
	}
	upstream.CloseResult()
	return result, nil
}

func (h *protocolHandler) collectStreamedResult(session *upstreamSession,
	upstream *vtmysql.Conn, sinkFor func([]*querypb.Field) (*rowSink, error),
) (*sqltypes.Result, error, error) {
	// returns the result, whose rows go to a sink when one is given, what the sink could not model,
	// and what failed the stream
	fields, err := upstream.Fields()
	if err != nil {
		return nil, nil, err
	}
	size, overflow := resultFieldsSize(fields, h.config.MaxResultSizeBytes)
	if overflow {
		return nil, nil, h.resultLimitExceeded(session)
	}
	var sink *rowSink
	var sinkErr error
	result := &sqltypes.Result{Fields: fields}
	if sinkFor != nil {
		sink, sinkErr = sinkFor(fields)
	} else {
		result.Rows = make([][]sqltypes.Value, 0, min(h.config.MaxResultRows, resultBatchSize))
	}
	rows := 0
	var reuse []sqltypes.Value
	for {
		row, fetchErr := upstream.FetchNext(reuse)
		if fetchErr != nil {
			return nil, nil, fetchErr
		}
		if row == nil {
			statusFlags, _, stateErr := h.originProtocolState(upstream)
			if stateErr != nil {
				return nil, nil, stateErr
			}
			result.StatusFlags = statusFlags
			return result, sinkErr, nil
		}
		if rows >= h.config.MaxResultRows {
			return nil, nil, h.resultLimitExceeded(session)
		}
		size, overflow = addRowSize(size, row, h.config.MaxResultSizeBytes)
		if overflow {
			return nil, nil, h.resultLimitExceeded(session)
		}
		rows++
		if sinkFor == nil {
			result.Rows = append(result.Rows, row)
			continue
		}
		if sinkErr == nil {
			sinkErr = sink.row(row)
		}
		// the sink copies what it keeps, so the next row is read into this one's slice
		reuse = row[:0]
	}
}

func (h *protocolHandler) observeCacheFailure(reason string) {
	cacheClient := h.cacheClient()
	if cacheClient == nil || cacheClient.Configuration() == nil {
		return
	}
	configuration := cacheClient.Configuration()
	metrics.CacheEvents.WithLabelValues(configuration.Name, configuration.Provider,
		keys.Error, "mysql_"+reason).Inc()
}

func (h *protocolHandler) queryCacheKey(c *vtmysql.Conn, session *upstreamSession,
	engine string, statementIdentity ...string,
) string {
	session.mtx.Lock()
	database := session.database
	timeZone := session.viewLocked().TimeZone
	collation := session.collation
	session.mtx.Unlock()
	var identity strings.Builder
	identitySize := 1 + len(h.config.BackendName) + len(h.config.CacheKeyPrefix) +
		len(c.User) + len(database) + len(timeZone) + len(engine) +
		(7+len(statementIdentity))*binary.MaxVarintLen64
	for _, part := range statementIdentity {
		identitySize += len(part)
	}
	identity.Grow(identitySize)
	identity.WriteByte(cacheIdentityVersion)
	appendCacheIdentityField(&identity, h.config.BackendName)
	appendCacheIdentityField(&identity, h.config.CacheKeyPrefix)
	appendCacheIdentityField(&identity, c.User)
	appendCacheIdentityField(&identity, database)
	appendCacheIdentityField(&identity, timeZone)
	appendCacheIdentityUint(&identity, uint64(collation))
	appendCacheIdentityField(&identity, engine)
	for _, part := range statementIdentity {
		appendCacheIdentityField(&identity, part)
	}
	suffix := checksum.Checksum(identity.String())
	dialect := ""
	if h.config.Engine != nil {
		dialect = h.dialect() + "."
	}
	return h.config.BackendName + "." + h.config.CacheKeyPrefix + "." + dialect + "mysql." + engine + "." + suffix
}

func appendCacheIdentityField(identity *strings.Builder, value string) {
	var length [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(length[:], uint64(len(value)))
	_, _ = identity.Write(length[:n])
	_, _ = identity.WriteString(value)
}

func appendCacheIdentityUint(identity *strings.Builder, value uint64) {
	var encoded [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(encoded[:], value)
	_, _ = identity.Write(encoded[:n])
}

func resultIndexes(fields []*querypb.Field,
	plan *sqlanalyzer.QueryPlan,
) (int, []int, error) {
	timeIndex := -1
	groups := make([]int, len(plan.GroupColumns))
	values := make([]int, len(plan.ValueColumns))
	for i := range groups {
		groups[i] = -1
	}
	for i := range values {
		values[i] = -1
	}
	for i, field := range fields {
		if field == nil {
			continue
		}
		if strings.EqualFold(field.Name, plan.OutputColumn) {
			if timeIndex >= 0 {
				return 0, nil, fmt.Errorf("MySQL result has duplicate time column %q", plan.OutputColumn)
			}
			timeIndex = i
		}
		for j, group := range plan.GroupColumns {
			if strings.EqualFold(field.Name, group) {
				if groups[j] >= 0 {
					return 0, nil, fmt.Errorf("MySQL result has duplicate group column %q", group)
				}
				groups[j] = i
			}
		}
		for j, value := range plan.ValueColumns {
			if strings.EqualFold(field.Name, value) {
				if values[j] >= 0 || !sqltypes.IsNumber(field.Type) {
					return 0, nil, fmt.Errorf("MySQL result value column %q is duplicate or non-numeric", value)
				}
				values[j] = i
			}
		}
	}
	if timeIndex < 0 {
		return 0, nil, fmt.Errorf("MySQL result has no time column %q", plan.OutputColumn)
	}
	for i, index := range groups {
		if index < 0 {
			return 0, nil, fmt.Errorf("MySQL result has no group column %q", plan.GroupColumns[i])
		}
	}
	for i, index := range values {
		if index < 0 {
			return 0, nil, fmt.Errorf("MySQL result has no numeric value column %q", plan.ValueColumns[i])
		}
	}
	return timeIndex, groups, nil
}

func resultEpoch(value sqltypes.Value, unit timeseries.FieldDataType) (int64, error) {
	n, err := strconv.ParseInt(value.ToString(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse MySQL time value: %w", err)
	}
	if unit == timeseries.DateTimeUnixSecs {
		return n * int64(time.Second), nil
	}
	if unit == timeseries.DateTimeUnixNano {
		return n, nil
	}
	return 0, fmt.Errorf("unsupported MySQL time unit %d", unit)
}

// groupCompareKind names the one MySQL comparison rule that orders a group
// column. Every rule is chosen from the field's declared type when the
// comparator is built, so the sort itself performs no lookups.
type groupCompareKind uint8

const (
	compareCollatedText groupCompareKind = iota
	compareBytes
	compareSigned
	compareUnsigned
	compareFloat
	compareDecimal
)

// groupColumn describes how to order one group column.
type groupColumn struct {
	index     int
	name      string
	fieldType querypb.Type
	kind      groupCompareKind
	collation colldata.Collation
}

// groupComparator orders rows by their group columns using MySQL's own rules:
// NULLs first in ascending order, numeric ordering for numbers, exact decimal
// ordering, and each text field's declared collation. Types it cannot order
// exactly are rejected outright, which costs DPC optimization rather than
// correctness because the caller falls back to the object cache.
type groupComparator struct {
	columns   []groupColumn
	nullsLast bool
}

func (h *protocolHandler) newGroupComparator(fields []*querypb.Field,
	indexes []int,
) (*groupComparator, error) {
	semantics := h.resultSemantics()
	c := &groupComparator{columns: make([]groupColumn, len(indexes)), nullsLast: semantics.NullsLast}
	for i, index := range indexes {
		// resultIndexes has already proven every group index addresses a field.
		field := fields[index]
		column := groupColumn{index: index, name: field.Name, fieldType: field.Type}
		switch {
		case sqltypes.IsEnum(field.Type), sqltypes.IsSet(field.Type):
			// MySQL orders these by ordinal and bitmask, which live in the
			// column's declaration. A result header does not carry them, so
			// every value would otherwise compare equal.
			return nil, fmt.Errorf("MySQL group column %q of type %v needs declaration "+
				"values that the result metadata does not carry", field.Name, field.Type)
		case sqltypes.IsSigned(field.Type):
			column.kind = compareSigned
		case sqltypes.IsUnsigned(field.Type):
			column.kind = compareUnsigned
		case sqltypes.IsFloat(field.Type):
			column.kind = compareFloat
		case sqltypes.IsDecimal(field.Type):
			column.kind = compareDecimal
		case sqltypes.IsBinary(field.Type), sqltypes.IsDate(field.Type):
			// Binary strings order by raw bytes by definition, and MySQL renders
			// DATE, DATETIME, and TIMESTAMP zero-padded at a fixed width. TIME is
			// deliberately absent: it can be negative, which byte order gets wrong.
			column.kind = compareBytes
		case sqltypes.IsText(field.Type):
			if semantics.BinaryText {
				column.kind = compareBytes
				break
			}
			if field.Charset > math.MaxUint16 {
				return nil, fmt.Errorf("MySQL group column %q uses collation %d, "+
					"which Trickster cannot order", field.Name, field.Charset)
			}
			collationID := collations.ID(field.Charset) //nolint:gosec // range checked above
			if collationID == collations.Unknown {
				collationID = h.collationEnv().DefaultConnectionCharset()
			}
			if collationID == collations.CollationBinaryID {
				column.kind = compareBytes
				break
			}
			column.collation = colldata.Lookup(collationID)
			if column.collation == nil {
				return nil, fmt.Errorf("MySQL group column %q uses collation %d, "+
					"which Trickster cannot order", field.Name, collationID)
			}
			column.kind = compareCollatedText
		default:
			return nil, fmt.Errorf("MySQL group column %q has type %v, "+
				"which Trickster cannot order", field.Name, field.Type)
		}
		c.columns[i] = column
	}
	return c, nil
}

// validateRow rejects a row whose group values do not carry their field's
// declared type. Each comparison rule was chosen from the field, so a value of
// some other type would be ordered by the wrong rule. Rows are validated once
// on the way in rather than on every comparison.
func (c *groupComparator) validateRow(row []sqltypes.Value) error {
	for i := range c.columns {
		column := &c.columns[i]
		value := row[column.index]
		if value.IsNull() || value.Type() == column.fieldType {
			continue
		}
		return fmt.Errorf("MySQL group column %q holds a %v value, want %v",
			column.name, value.Type(), column.fieldType)
	}
	return nil
}

// compare orders left before right by each group column in turn. Failures
// propagate rather than falling back to a byte order that would not match what
// the origin returned.
func (c *groupComparator) compare(left, right []sqltypes.Value) (int, error) {
	for i := range c.columns {
		column := &c.columns[i]
		l, r := left[column.index], right[column.index]
		// MySQL sorts NULL before every value in ascending order.
		switch {
		case l.IsNull() && r.IsNull():
			continue
		case l.IsNull():
			if c.nullsLast {
				return 1, nil
			}
			return -1, nil
		case r.IsNull():
			if c.nullsLast {
				return -1, nil
			}
			return 1, nil
		}
		order, err := column.compareValues(l, r)
		if err != nil {
			return 0, err
		}
		if order != 0 {
			return order, nil
		}
	}
	return 0, nil
}

func (g *groupColumn) compareValues(left, right sqltypes.Value) (int, error) {
	switch g.kind {
	case compareCollatedText:
		return g.collation.Collate(left.Raw(), right.Raw(), false), nil
	case compareBytes:
		return bytes.Compare(left.Raw(), right.Raw()), nil
	case compareSigned:
		l, r, err := twoValues(g, left, right, sqltypes.Value.ToInt64)
		return cmp.Compare(l, r), err
	case compareUnsigned:
		l, r, err := twoValues(g, left, right, sqltypes.Value.ToUint64)
		return cmp.Compare(l, r), err
	case compareFloat:
		l, r, err := twoValues(g, left, right, sqltypes.Value.ToFloat64)
		return cmp.Compare(l, r), err
	case compareDecimal:
		l, err := decimal.NewFromMySQL(left.Raw())
		if err != nil {
			return 0, g.valueError(err)
		}
		r, err := decimal.NewFromMySQL(right.Raw())
		if err != nil {
			return 0, g.valueError(err)
		}
		return l.Cmp(r), nil
	}
	return 0, fmt.Errorf("MySQL group column %q has no comparison rule", g.name)
}

func twoValues[T any](g *groupColumn, left, right sqltypes.Value,
	convert func(sqltypes.Value) (T, error),
) (T, T, error) {
	l, err := convert(left)
	if err != nil {
		var zero T
		return zero, zero, g.valueError(err)
	}
	r, err := convert(right)
	if err != nil {
		var zero T
		return zero, zero, g.valueError(err)
	}
	return l, r, nil
}

func (g *groupColumn) valueError(err error) error {
	return fmt.Errorf("compare MySQL group column %q: %w", g.name, err)
}

// collationEnv returns the environment used to order text group columns. A
// handler built without a Vitess environment falls back to the protocol's own
// advertised server version.
func (h *protocolHandler) collationEnv() *collations.Environment {
	if h.env != nil {
		return h.env.CollationEnv()
	}
	return defaultCollationEnv()
}

var defaultCollationEnv = sync.OnceValue(func() *collations.Environment {
	return collations.NewEnvironment(protocolVersion)
})

func compatibleFields(left, right []*querypb.Field) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] == nil || right[i] == nil {
			if left[i] != right[i] {
				return false
			}
			continue
		}
		// Charset carries the collation that orders a text column. Parts that
		// disagree would all be ordered by the first part's collation.
		if left[i].Name != right[i].Name || left[i].Type != right[i].Type ||
			left[i].Charset != right[i].Charset {
			return false
		}
	}
	return true
}

func cloneResultMetadata(result *sqltypes.Result) *sqltypes.Result {
	return &sqltypes.Result{
		Fields: result.Fields, RowsAffected: result.RowsAffected,
		InsertID: result.InsertID, InsertIDChanged: result.InsertIDChanged,
		SessionStateChanges: result.SessionStateChanges, StatusFlags: result.StatusFlags,
		Info: result.Info,
	}
}

func (h *protocolHandler) updateSessionStateParsed(session *upstreamSession, parsed parsedQuery) {
	session.mtx.Lock()
	defer session.mtx.Unlock()
	if parsed.statementType == sqlparser.StmtSelect {
		switch {
		case parsed.err != nil || parsed.statement == nil:
			session.cacheUnsafe = true
			session.stateful = true
		case selectChangesSessionState(parsed.statement):
			// SELECT ... INTO, user-variable assignment, and the advisory lock
			// functions all leave connection-scoped state behind.
			session.cacheUnsafe = true
			session.stateful = true
		}
	}
	if parsed.statementType == sqlparser.StmtExplain && parsed.responseShape == responseShapeOK {
		// EXPLAIN ... INTO stores its result in a user variable instead of
		// returning rows, so the successful statement creates unreplayable state.
		session.cacheUnsafe = true
		session.stateful = true
	}
	switch parsed.statementType {
	// Rolling back to or releasing a savepoint does not end its transaction.
	case sqlparser.StmtBegin, sqlparser.StmtSavepoint,
		sqlparser.StmtSRollback, sqlparser.StmtRelease:
		session.inTx = true
	case sqlparser.StmtCommit, sqlparser.StmtRollback:
		session.inTx = false
	case sqlparser.StmtUse:
		if use, ok := parsed.statement.(*sqlparser.Use); parsed.err == nil && ok {
			session.database = use.DBName.String()
			if !session.upstreamParamsReady {
				session.upstream = h.config.Upstream
				session.upstreamParamsReady = true
			}
			session.upstream.DbName = session.database
		}
	case sqlparser.StmtSet:
		if timeZone, ok := cacheSafeTimeZone(parsed.statement); parsed.err == nil && ok {
			session.timeZone = timeZone
		} else {
			// The time zone is the only SET replaySessionState reproduces.
			session.cacheUnsafe = true
			session.stateful = true
		}
	// The rows are durable at the origin, but the write also updates
	// connection-scoped diagnostics — LAST_INSERT_ID above all — that a
	// replacement connection reports differently.
	case sqlparser.StmtInsert, sqlparser.StmtReplace,
		sqlparser.StmtUpdate, sqlparser.StmtDelete,
		// Or Table locks are connection-scoped, and the analyzer does not parse
		// DDL closely enough to rule out CREATE TEMPORARY TABLE.
		sqlparser.StmtDDL, sqlparser.StmtLockTables, sqlparser.StmtUnlockTables:
		session.cacheUnsafe = true
		session.stateful = true
	default:
		switch parsed.statementType {
		case sqlparser.StmtSelect, sqlparser.StmtShow, sqlparser.StmtExplain,
			sqlparser.StmtAnalyze, sqlparser.StmtComment, sqlparser.StmtCommentOnly:
		case sqlparser.StmtUnknown:
			// AST-less row producers admitted by the response classifier are
			// read-only TABLE, CHECK TABLE, and CHECKSUM TABLE statements.
			if parsed.responseShape != responseShapeRows {
				session.cacheUnsafe = true
				session.stateful = true
			}
		default:
			session.cacheUnsafe = true
			session.stateful = true
		}
	}
}

// updateSessionStateFailed records state a failed statement may still have left
// on the connection. MySQL applies a multi-assignment SET left to right and can
// abort partway, and DDL is not transactional, so an error is not proof that
// nothing changed. Values the origin definitely did not apply — the database,
// the time zone, and the transaction flag — are deliberately left alone.
func (h *protocolHandler) updateSessionStateFailed(session *upstreamSession, parsed parsedQuery) {
	if parsed.statementType == sqlparser.StmtUnknown && parsed.responseShape == responseShapeRows {
		return
	}
	switch parsed.statementType {
	case sqlparser.StmtSelect:
		// A failed read leaves nothing behind unless its own AST assigns a
		// variable, selects INTO, or takes an advisory lock. A read with no
		// recorded AST has nothing to inspect.
		if parsed.statement == nil || parsed.err != nil ||
			!selectChangesSessionState(parsed.statement) {
			return
		}
	case sqlparser.StmtShow, sqlparser.StmtExplain, sqlparser.StmtAnalyze,
		sqlparser.StmtComment, sqlparser.StmtCommentOnly, sqlparser.StmtUse,
		sqlparser.StmtBegin, sqlparser.StmtCommit, sqlparser.StmtRollback,
		sqlparser.StmtSavepoint, sqlparser.StmtSRollback, sqlparser.StmtRelease:
		return
	}
	// Everything else — SET, DDL, table locks, and unrecognized statements —
	// may have applied part of its work before the origin reported the error.
	session.mtx.Lock()
	session.cacheUnsafe = true
	session.stateful = true
	session.mtx.Unlock()
}

func selectChangesSessionState(stmt sqlparser.Statement) bool {
	unsafe := false
	_ = sqlparser.Walk(func(node sqlparser.SQLNode) (bool, error) {
		switch n := node.(type) {
		case *sqlparser.Select:
			unsafe = n.Into != nil || n.SQLCalcFoundRows
		case *sqlparser.Variable, *sqlparser.AssignmentExpr:
			unsafe = true
		case *sqlparser.LockingFunc:
			unsafe = true
		case *sqlparser.FuncExpr:
			switch strings.ToLower(n.Name.String()) {
			case "get_lock", "release_all_locks", "release_lock":
				unsafe = true
			}
		}
		return !unsafe, nil
	}, stmt)
	return unsafe
}

func cacheSafeTimeZone(stmt sqlparser.Statement) (string, bool) {
	set, ok := stmt.(*sqlparser.Set)
	if !ok || len(set.Exprs) != 1 || set.Exprs[0].Var == nil {
		return "", false
	}
	expr := set.Exprs[0]
	if !expr.Var.Name.EqualString("time_zone") ||
		(expr.Var.Scope != sqlparser.NoScope && expr.Var.Scope != sqlparser.SessionScope) {
		return "", false
	}
	literal, ok := expr.Expr.(*sqlparser.Literal)
	if !ok || literal.Type != sqlparser.StrVal || literal.Val == "" {
		return "", false
	}
	return literal.Val, true
}

func (h *protocolHandler) observeAnalysis(statementType sqlparser.StatementType,
	analysis sqlanalyzer.Analysis,
) {
	reason := string(analysis.Reason)
	if reason == "" {
		reason = "unknown"
	}
	key := analysisMetricKey{mode: analysis.Mode, reason: reason}
	if h.metricHandles != nil {
		if counter := h.metricHandles.analysis[key]; counter != nil {
			counter.Inc()
		} else {
			metrics.SQLQueryAnalysis.WithLabelValues(h.config.BackendName, h.dialect(),
				analysis.Mode.String(), reason).Inc()
		}
	} else {
		metrics.SQLQueryAnalysis.WithLabelValues(h.config.BackendName, h.dialect(),
			analysis.Mode.String(), reason).Inc()
	}
	if logger.Level() == level.Debug {
		logger.Debug("mysql query analyzed", logging.Pairs{
			keys.BackendName: h.config.BackendName, "cache_mode": analysis.Mode.String(),
			"analysis_reason": reason, "statement_type": statementType.String(),
		})
	}
}

func (h *protocolHandler) observeRewriteFailure(reason string) {
	metrics.SQLQueryRewriteFailures.WithLabelValues(h.config.BackendName, h.dialect(), reason).Inc()
	logger.Error("mysql query extent rewrite failed", logging.Pairs{
		keys.BackendName: h.config.BackendName, keys.Reason: reason,
	})
}

func (h *protocolHandler) observeCache(mode sqlanalyzer.CacheMode,
	status cachestatus.LookupStatus, points int, elapsed time.Duration,
) {
	handles, ok := cacheMetricHandles{}, false
	if h.metricHandles != nil {
		handles, ok = h.metricHandles.cache[cacheMetricKey{mode: mode, status: status}]
	}
	if !ok {
		handles = resolveCacheMetricHandles(h.config.BackendName, h.dialect(), mode, status)
	}
	handles.native.Inc()
	handles.requests.Inc()
	handles.elements.Add(float64(points))
	handles.duration.Observe(elapsed.Seconds())
	if logger.Level() == level.Debug {
		logger.Debug("mysql query cache completed", logging.Pairs{
			keys.BackendName: h.config.BackendName, "cache_mode": mode.String(),
			"cache_status": status.String(),
		})
	}
}

func newProtocolMetricHandles(backendName, dialect string) *protocolMetricHandles {
	handles := &protocolMetricHandles{
		connectLatency: metrics.MySQLCommandLatency.WithLabelValues(backendName, "connect"),
		queryLatency:   metrics.MySQLCommandLatency.WithLabelValues(backendName, metricPathQuery),
		analysis:       make(map[analysisMetricKey]prometheus.Counter, len(analysisMetricKeys)),
		cache:          make(map[cacheMetricKey]cacheMetricHandles, 10),
	}
	for _, key := range analysisMetricKeys {
		handles.analysis[key] = metrics.SQLQueryAnalysis.WithLabelValues(backendName, dialect,
			key.mode.String(), key.reason)
	}
	statuses := map[sqlanalyzer.CacheMode][]cachestatus.LookupStatus{
		sqlanalyzer.CacheModeObject: {
			cachestatus.LookupStatusHit,
			cachestatus.LookupStatusKeyMiss,
			cachestatus.LookupStatusProxyError,
			cachestatus.LookupStatusProxyOnly,
		},
		sqlanalyzer.CacheModeDelta: {
			cachestatus.LookupStatusHit,
			cachestatus.LookupStatusPartialHit,
			cachestatus.LookupStatusRangeMiss,
			cachestatus.LookupStatusKeyMiss,
			cachestatus.LookupStatusProxyError,
			cachestatus.LookupStatusProxyOnly,
		},
	}
	for mode, values := range statuses {
		for _, status := range values {
			key := cacheMetricKey{mode: mode, status: status}
			handles.cache[key] = resolveCacheMetricHandles(backendName, dialect, mode, status)
		}
	}
	return handles
}

func resolveCacheMetricHandles(backendName, dialect string, mode sqlanalyzer.CacheMode,
	status cachestatus.LookupStatus,
) cacheMetricHandles {
	httpStatus := metricHTTPStatusOK
	if status == cachestatus.LookupStatusProxyError || status == cachestatus.LookupStatusError {
		httpStatus = metricHTTPStatusInternalError
	}
	statusLabel := status.String()
	return cacheMetricHandles{
		native: metrics.SQLQueryCache.WithLabelValues(backendName, dialect,
			mode.String(), statusLabel),
		requests: metrics.ProxyRequestStatus.WithLabelValues(backendName, dialect,
			metricMethodQuery, statusLabel, httpStatus, metricPathQuery),
		elements: metrics.ProxyRequestElements.WithLabelValues(backendName, dialect,
			statusLabel, metricPathQuery),
		duration: metrics.ProxyRequestDuration.WithLabelValues(backendName, dialect,
			metricMethodQuery, statusLabel, httpStatus, metricPathQuery),
	}
}
