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
	"sync/atomic"
	"unsafe"

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
	// warningBytes is the memory a kept warning takes besides its text: its error, and its key and value in a map of them
	warningBytes = 2*int64(unsafe.Sizeof("")) + int64(unsafe.Sizeof(error(nil)))
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
	// uncompressed is set once the store rejects snappy, so later calls don't ask for it
	uncompressed atomic.Bool
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
	ctx = metadata.NewOutgoingContext(ctx, s.metadata(requestFrom(ctx)))
	compress := !s.uncompressed.Load()
	series, warnings, err := s.series(ctx, req, mint, maxt, compress)
	if compress && status.Code(err) == codes.Unimplemented {
		// a store without the compressor rejects the call before answering it, so it is made again without
		series, warnings, err = s.series(ctx, req, mint, maxt, false)
		if status.Code(err) != codes.Unimplemented {
			s.uncompressed.Store(true)
		}
	}
	return series, warnings, err
}

// series streams the series req asks for, with their messages compressed by snappy when compress is set
func (s *StoreAPI) series(ctx context.Context, req []byte, mint, maxt int64, compress bool,
) ([]storage.Series, annotations.Annotations, error) {
	// a stream is released only once canceled or read to its end, so one left unread is canceled as this returns
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	budget := budgetFrom(ctx)
	maxMessage := maxStoreAPIMessageBytes
	if limit := budget.responseLimit(); limit > 0 && limit < int64(maxMessage) {
		maxMessage = int(limit)
	}
	opts := []grpc.CallOption{grpc.ForceCodecV2(rawCodec{}), grpc.MaxCallRecvMsgSize(maxMessage)}
	if compress {
		opts = append(opts, grpc.UseCompressor(storeAPICompressor))
	}
	stream, err := s.conn.NewStream(ctx, &seriesStreamDesc, storeAPISeriesMethod, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errStoreAPI, err)
	}
	if err := stream.SendMsg(&rawFrame{data: req}); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errStoreAPI, err)
	}
	if err := stream.CloseSend(); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errStoreAPI, err)
	}
	d := newDecoder(budget, mint, maxt, errStoreAPI)
	d.replicaLabels = replicaLabelsFrom(ctx)
	var warnings annotations.Annotations
	var received int64
	// each message is received into one buffer, which grows only for a message larger than any before
	var frame rawFrame
	for {
		held := cap(frame.data)
		if err := stream.RecvMsg(&frame); errors.Is(err, io.EOF) {
			return d.out, warnings, nil
		} else if err != nil {
			return nil, nil, receiveError(err, maxMessage)
		}
		if cap(frame.data) != held {
			d.release()
		}
		received += int64(len(frame.data))
		if limit := budget.responseLimit(); limit > 0 && received > limit {
			return nil, nil, fmt.Errorf("%w: response exceeds %d bytes", ErrFetchLimit, limit)
		}
		warning, err := d.seriesResponse(frame.data)
		if err != nil {
			return nil, nil, err
		}
		if warning != "" {
			warnings.Add(errors.New(warning))
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

// seriesResponse reads one streamed SeriesResponse, keeping its series' chunks from mint to maxt and adding what they
// count to the budget, or returning its warning. A message of more than one result is refused before any is decoded.
func (d *decoder) seriesResponse(b []byte) (string, error) {
	var result wireField
	for len(b) > 0 {
		f, rest, err := d.field(b)
		if err != nil {
			return "", err
		}
		b = rest
		if f.typ != protowire.BytesType {
			return "", fmt.Errorf("%w: unexpected response field %d", errStoreAPI, f.num)
		}
		switch f.num {
		case seriesResponseSeries, seriesResponseWarning, seriesResponseHints:
		default:
			// an unknown kind of answer could hold series this client would silently drop
			return "", fmt.Errorf("%w: unsupported response field %d", errStoreAPI, f.num)
		}
		// the result is a oneof that stores send one value of, so a second leaves which one is meant ambiguous
		if result.num != 0 {
			return "", fmt.Errorf("%w: response fields %d and %d are alternative results", errStoreAPI, result.num, f.num)
		}
		result = f
	}
	switch result.num {
	case seriesResponseSeries:
		if err := d.chunkedSeries(result.b, storeChunk); err != nil {
			return "", err
		}
		return "", d.flush()
	case seriesResponseWarning:
		// an empty warning, like a message without a result, adds nothing
		if len(result.b) == 0 {
			return "", nil
		}
		if err := d.charge(warningBytes + int64(len(result.b))); err != nil {
			return "", err
		}
		return string(result.b), nil
	}
	return "", nil
}

// storeChunk reads an AggrChunk message's span and raw chunk; a downsampled chunk without one is refused
func storeChunk(d *decoder, b []byte) (wireChunk, error) {
	var c wireChunk
	var raw []byte
	for len(b) > 0 {
		f, rest, err := d.field(b)
		if err != nil {
			return c, err
		}
		b = rest
		switch f.num {
		case aggrChunkMinTime, aggrChunkMaxTime:
			if err := d.expect(f, protowire.VarintType); err != nil {
				return c, err
			}
			if f.num == aggrChunkMinTime {
				c.sp.mint = int64(f.u) //nolint:gosec // wire protocol reinterpret
			} else {
				c.sp.maxt = int64(f.u) //nolint:gosec // wire protocol reinterpret
			}
		case aggrChunkRaw:
			if err := d.expect(f, protowire.BytesType); err != nil {
				return c, err
			}
			raw = f.b
		}
	}
	if raw == nil {
		return c, fmt.Errorf("%w: a chunk without raw data; only raw resolution is read", errStoreAPI)
	}
	// proto3 omits a zero type, so a chunk without one is XOR
	typ := uint64(storeChunkXOR)
	for len(raw) > 0 {
		f, rest, err := d.field(raw)
		if err != nil {
			return c, err
		}
		raw = rest
		switch f.num {
		case chunkType:
			if err := d.expect(f, protowire.VarintType); err != nil {
				return c, err
			}
			typ = f.u
		case chunkData:
			if err := d.expect(f, protowire.BytesType); err != nil {
				return c, err
			}
			c.data = f.b
		}
	}
	switch typ {
	case storeChunkXOR:
		c.enc = chunkenc.EncXOR
	case storeChunkHistogram:
		c.enc = chunkenc.EncHistogram
	case storeChunkFloatHistogram:
		c.enc = chunkenc.EncFloatHistogram
	default:
		return c, fmt.Errorf("%w: unsupported chunk encoding %d", errStoreAPI, typ)
	}
	if err := checkChunk(c.enc, c.data); err != nil {
		return c, fmt.Errorf("%w: %w", errStoreAPI, err)
	}
	return c, nil
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
	// the frame's buffer is reused when it is large enough, as nothing decoded from a message keeps its memory
	n := data.Len()
	if cap(f.data) < n {
		f.data = make([]byte, n)
	}
	f.data = f.data[:n]
	data.CopyTo(f.data)
	return nil
}

func (rawCodec) Name() string { return storeAPICodecName }
