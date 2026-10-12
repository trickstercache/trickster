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

package global

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/util/annotations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/mem"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	storeAPISeriesMethod = "/thanos.Store/Series"
	storeAPISeriesStream = "Series"
	storeAPICodecName    = "proto"
	// maxStoreAPIMessageBytes bounds one streamed StoreAPI message
	maxStoreAPIMessageBytes = 64 << 20
	// grpcLocalErrorPrefix starts the errors gRPC raises in this process, as for a message over its size limit
	grpcLocalErrorPrefix = "grpc: "
)

// Thanos storepb field numbers: SeriesRequest, SeriesResponse, Series, AggrChunk, Chunk, Label, LabelMatcher
const (
	seriesRequestMinTime         = 1
	seriesRequestMaxTime         = 2
	seriesRequestMatchers        = 3
	seriesRequestAggregates      = 5
	seriesRequestPartialDisabled = 6
	seriesRequestPartialStrategy = 7
	seriesRequestStep            = 10
	seriesRequestRange           = 11
	seriesResponseSeries         = 1
	seriesResponseWarning        = 2
	seriesResponseHints          = 3
	seriesLabels                 = 1
	seriesChunks                 = 2
	aggrChunkMinTime             = 1
	aggrChunkMaxTime             = 2
	aggrChunkRaw                 = 3
	chunkType                    = 1
	chunkData                    = 2
	labelName                    = 1
	labelValue                   = 2
	matcherType                  = 1
	matcherName                  = 2
	matcherValue                 = 3
	aggrRaw                      = 0
	partialResponseStrategyAbort = 1
	storeChunkXOR                = 0
	storeChunkHistogram          = 1
	storeChunkFloatHistogram     = 2
)

var (
	errStoreAPI = errors.New("thanos storeapi series read failed")

	seriesStreamDesc = grpc.StreamDesc{StreamName: storeAPISeriesStream, ServerStreams: true}
)

// StoreAPI reads a member's raw series over the Thanos StoreAPI Series stream.
type StoreAPI struct {
	SourceIdentity
	conn  *grpc.ClientConn
	paths po.List
}

// NewStoreAPI returns a source reading a grpc:// or grpcs:// StoreAPI endpoint, grpcs with tlsConfig;
// calls carry the client's headers and the request_headers paths sets as metadata.
func NewStoreAPI(endpoint *url.URL, tlsConfig *tls.Config, paths po.List) (*StoreAPI, error) {
	creds := insecure.NewCredentials()
	if endpoint.Scheme == prop.StoreAPISchemeTLS {
		if tlsConfig == nil {
			tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		creds = credentials.NewTLS(tlsConfig)
	}
	return newStoreAPI(endpoint.Host, paths, grpc.WithTransportCredentials(creds))
}

func newStoreAPI(target string, paths po.List, opts ...grpc.DialOption) (*StoreAPI, error) {
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errStoreAPI, err)
	}
	return &StoreAPI{conn: conn, paths: paths}, nil
}

// SeriesAPI returns the StoreAPI series API name.
func (*StoreAPI) SeriesAPI() string { return prop.SeriesAPIStoreAPI }

// Close releases the source's connection.
func (s *StoreAPI) Close() error { return s.conn.Close() }

// Select streams the raw chunks of the series matching every matcher, from mint to maxt. Stores must
// abort rather than answer partially; a warning one sends anyway is returned.
func (s *StoreAPI) Select(ctx context.Context, mint, maxt int64, hints *storage.SelectHints,
	matchers ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	req, err := encodeSeriesRequest(mint, maxt, hints, matchers)
	if err != nil {
		return nil, nil, err
	}
	budget := budgetFrom(ctx)
	maxMessage := maxStoreAPIMessageBytes
	if limit := budget.responseLimit(); limit > 0 && limit < int64(maxMessage) {
		maxMessage = int(limit)
	}
	ctx = metadata.NewOutgoingContext(ctx, s.metadata(requestFrom(ctx)))
	stream, err := s.conn.NewStream(ctx, &seriesStreamDesc, storeAPISeriesMethod,
		grpc.ForceCodecV2(rawCodec{}), grpc.MaxCallRecvMsgSize(maxMessage))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errStoreAPI, err)
	}
	if err := stream.SendMsg(&rawFrame{data: req}); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errStoreAPI, err)
	}
	if err := stream.CloseSend(); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errStoreAPI, err)
	}
	var out []storage.Series
	var last *chunkSeries
	var lastCounted bool
	var warnings annotations.Annotations
	var builder labels.ScratchBuilder
	var received int64
	for {
		var frame rawFrame
		if err := stream.RecvMsg(&frame); errors.Is(err, io.EOF) {
			return out, warnings, nil
		} else if err != nil {
			return nil, nil, receiveError(err, maxMessage)
		}
		received += int64(len(frame.data))
		if limit := budget.responseLimit(); limit > 0 && received > limit {
			return nil, nil, fmt.Errorf("%w: response exceeds %d bytes", ErrFetchLimit, limit)
		}
		series, warning, samples, err := decodeSeriesResponse(frame.data, &builder, mint, maxt)
		if err != nil {
			return nil, nil, err
		}
		if warning != "" {
			warnings.Add(errors.New(warning))
			continue
		}
		if series == nil {
			continue
		}
		if last != nil && labels.Equal(last.lset, series.lset) {
			last.chunks = append(last.chunks, series.chunks...)
			last.spans = append(last.spans, series.spans...)
		} else {
			last = series
			out = append(out, series)
			lastCounted = false
		}
		var newSeries int64
		if samples > 0 && !lastCounted {
			newSeries, lastCounted = 1, true
		}
		if err := budget.add(newSeries, samples); err != nil {
			return nil, nil, err
		}
	}
}

// receiveError wraps a failed receive; a message over this client's own size limit is a fetch limit,
// which fails even a partial response, rather than a member's failure
func receiveError(err error, maxMessage int) error {
	if st, ok := status.FromError(err); ok && st.Code() == codes.ResourceExhausted &&
		strings.HasPrefix(st.Message(), grpcLocalErrorPrefix) {
		return fmt.Errorf("%w: a storeapi message exceeds %d bytes", ErrFetchLimit, maxMessage)
	}
	return fmt.Errorf("%w: %w", errStoreAPI, err)
}

// metadata returns the client request's end-to-end headers and the configured request headers for its
// path, as lower-case gRPC metadata
func (s *StoreAPI) metadata(in *http.Request) metadata.MD {
	md := metadata.MD{}
	if in == nil {
		return md
	}
	for k, vs := range s.sentHeaders(in) {
		if isEntityHeader(k) || isReservedMetadata(k) {
			continue
		}
		md.Append(strings.ToLower(k), vs...)
	}
	return md
}

func (s *StoreAPI) sentHeaders(in *http.Request) http.Header { return forwardedHeaders(s.paths, in) }

// isReservedMetadata reports whether gRPC sets a header itself, so a client's copy must not be sent
func isReservedMetadata(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "grpc-") || name == "user-agent" || name == "host" || name == "te"
}

func encodeSeriesRequest(mint, maxt int64, hints *storage.SelectHints, matchers []*labels.Matcher) ([]byte, error) {
	b := appendInt64Field(nil, seriesRequestMinTime, mint)
	b = appendInt64Field(b, seriesRequestMaxTime, maxt)
	for _, m := range matchers {
		var t uint64
		switch m.Type {
		case labels.MatchEqual, labels.MatchNotEqual, labels.MatchRegexp, labels.MatchNotRegexp:
			// StoreAPI numbers the matcher types as Prometheus does
			t = uint64(m.Type)
		default:
			return nil, fmt.Errorf("%w: unsupported matcher type %s", errStoreAPI, m.Type)
		}
		mb := protowire.AppendTag(nil, matcherType, protowire.VarintType)
		mb = protowire.AppendVarint(mb, t)
		mb = protowire.AppendTag(mb, matcherName, protowire.BytesType)
		mb = protowire.AppendString(mb, m.Name)
		mb = protowire.AppendTag(mb, matcherValue, protowire.BytesType)
		mb = protowire.AppendString(mb, m.Value)
		b = protowire.AppendTag(b, seriesRequestMatchers, protowire.BytesType)
		b = protowire.AppendBytes(b, mb)
	}
	// raw resolution only, so stores holding downsampled blocks never answer with aggregates
	b = protowire.AppendTag(b, seriesRequestAggregates, protowire.BytesType)
	b = protowire.AppendBytes(b, protowire.AppendVarint(nil, aggrRaw))
	b = protowire.AppendTag(b, seriesRequestPartialDisabled, protowire.VarintType)
	b = protowire.AppendVarint(b, protowire.EncodeBool(true))
	b = protowire.AppendTag(b, seriesRequestPartialStrategy, protowire.VarintType)
	b = protowire.AppendVarint(b, partialResponseStrategyAbort)
	if hints != nil {
		b = appendInt64Field(b, seriesRequestStep, hints.Step)
		b = appendInt64Field(b, seriesRequestRange, hints.Range)
	}
	return b, nil
}

// appendInt64Field writes a protobuf int64 field, which is the value's two's complement as a varint
func appendInt64Field(b []byte, num protowire.Number, v int64) []byte {
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, uint64(v)) //nolint:gosec // wire protocol reinterpret
}

// decodeSeriesResponse reads one streamed SeriesResponse: a series with the count of its samples from mint
// to maxt, or a warning
func decodeSeriesResponse(b []byte, builder *labels.ScratchBuilder, mint, maxt int64,
) (*chunkSeries, string, int64, error) {
	var series *chunkSeries
	var warning string
	var samples int64
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, "", 0, fmt.Errorf("%w: %w", errStoreAPI, protowire.ParseError(n))
		}
		b = b[n:]
		if typ != protowire.BytesType {
			return nil, "", 0, fmt.Errorf("%w: unexpected response field %d", errStoreAPI, num)
		}
		v, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return nil, "", 0, fmt.Errorf("%w: %w", errStoreAPI, protowire.ParseError(n))
		}
		b = b[n:]
		switch num {
		case seriesResponseSeries:
			var err error
			if series, samples, err = decodeStoreSeries(v, builder, mint, maxt); err != nil {
				return nil, "", 0, err
			}
		case seriesResponseWarning:
			warning = string(v)
		case seriesResponseHints:
		default:
			// an unknown kind of answer could hold series this client would silently drop
			return nil, "", 0, fmt.Errorf("%w: unsupported response field %d", errStoreAPI, num)
		}
	}
	return series, warning, samples, nil
}

func decodeStoreSeries(b []byte, builder *labels.ScratchBuilder, mint, maxt int64) (*chunkSeries, int64, error) {
	builder.Reset()
	var iterables []chunkenc.Iterable
	var spans []span
	var samples int64
	err := forEachField(b, func(num protowire.Number, v []byte) error {
		switch num {
		case seriesLabels:
			var name, value string
			if err := forEachField(v, func(num protowire.Number, v []byte) error {
				switch num {
				case labelName:
					name = string(v)
				case labelValue:
					value = string(v)
				}
				return nil
			}); err != nil {
				return err
			}
			builder.Add(name, value)
		case seriesChunks:
			chk, sp, err := decodeAggrChunk(v)
			if err != nil {
				return err
			}
			n, err := samplesWithin(chk, sp, mint, maxt)
			if err != nil {
				return fmt.Errorf("%w: %w", errStoreAPI, err)
			}
			samples += n
			iterables = append(iterables, chk)
			spans = append(spans, sp)
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	builder.Sort()
	return &chunkSeries{lset: builder.Labels(), chunks: iterables, spans: spans}, samples, nil
}

// decodeAggrChunk returns an AggrChunk's raw chunk and its span; a downsampled chunk without one is refused
func decodeAggrChunk(b []byte) (chunkenc.Chunk, span, error) {
	var chk chunkenc.Chunk
	var sp span
	err := forEachField(b, func(num protowire.Number, v []byte) error {
		if num != aggrChunkRaw {
			return nil
		}
		// proto3 omits a zero type, so a chunk without one is XOR
		enc := chunkenc.EncXOR
		var data []byte
		if err := forEachField(v, func(num protowire.Number, v []byte) error {
			if num == chunkData {
				data = v
			}
			return nil
		}, func(num protowire.Number, v uint64) {
			if num != chunkType {
				return
			}
			switch v {
			case storeChunkXOR:
				enc = chunkenc.EncXOR
			case storeChunkHistogram:
				enc = chunkenc.EncHistogram
			case storeChunkFloatHistogram:
				enc = chunkenc.EncFloatHistogram
			default:
				enc = chunkenc.EncNone
			}
		}); err != nil {
			return err
		}
		if enc == chunkenc.EncNone {
			return fmt.Errorf("%w: unsupported chunk encoding", errStoreAPI)
		}
		var err error
		// the chunk keeps data, so it is copied out of the message buffer
		if chk, err = newChunk(enc, append([]byte(nil), data...)); err != nil {
			return fmt.Errorf("%w: %w", errStoreAPI, err)
		}
		return nil
	}, func(num protowire.Number, v uint64) {
		switch num {
		case aggrChunkMinTime:
			sp.mint = int64(v) //nolint:gosec // wire protocol reinterpret
		case aggrChunkMaxTime:
			sp.maxt = int64(v) //nolint:gosec // wire protocol reinterpret
		}
	})
	if err != nil {
		return nil, sp, err
	}
	if chk == nil {
		return nil, sp, fmt.Errorf("%w: a chunk without raw data; only raw resolution is read", errStoreAPI)
	}
	return chk, sp, nil
}

// forEachField calls bytesField for every length-delimited field of a message and varintField, when
// given, for every varint; fields of other wire types are skipped
func forEachField(b []byte, bytesField func(protowire.Number, []byte) error,
	varintField ...func(protowire.Number, uint64),
) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return fmt.Errorf("%w: %w", errStoreAPI, protowire.ParseError(n))
		}
		b = b[n:]
		switch typ {
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return fmt.Errorf("%w: %w", errStoreAPI, protowire.ParseError(n))
			}
			if err := bytesField(num, v); err != nil {
				return err
			}
			b = b[n:]
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return fmt.Errorf("%w: %w", errStoreAPI, protowire.ParseError(n))
			}
			for _, f := range varintField {
				f(num, v)
			}
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return fmt.Errorf("%w: %w", errStoreAPI, protowire.ParseError(n))
			}
			b = b[n:]
		}
	}
	return nil
}

// rawFrame is one protobuf message, encoded or decoded by its caller rather than the codec
type rawFrame struct {
	data []byte
}

// rawCodec passes rawFrame messages through gRPC unchanged
type rawCodec struct{}

func (rawCodec) Marshal(v any) (mem.BufferSlice, error) {
	f, ok := v.(*rawFrame)
	if !ok {
		return nil, fmt.Errorf("%w: cannot encode %T", errStoreAPI, v)
	}
	return mem.BufferSlice{mem.SliceBuffer(f.data)}, nil
}

func (rawCodec) Unmarshal(data mem.BufferSlice, v any) error {
	f, ok := v.(*rawFrame)
	if !ok {
		return fmt.Errorf("%w: cannot decode into %T", errStoreAPI, v)
	}
	f.data = data.Materialize()
	return nil
}

func (rawCodec) Name() string { return storeAPICodecName }
