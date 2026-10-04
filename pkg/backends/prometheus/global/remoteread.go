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
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"strings"

	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"

	"github.com/klauspost/compress/snappy"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/tsdb/chunks"
	"github.com/prometheus/prometheus/util/annotations"
)

const (
	remoteReadVersionHeader = "X-Prometheus-Remote-Read-Version"
	remoteReadVersion       = "0.1.0"
	contentTypeProtobuf     = "application/x-protobuf"
	contentTypeStreamed     = "application/x-streamed-protobuf"
	contentEncodingSnappy   = "snappy"
	// maxFrameBytes bounds one frame of a streamed response, as the Prometheus remote-read client does
	maxFrameBytes = 50 << 20
	// maxErrorBodyBytes bounds how much of an error response is quoted in the returned error
	maxErrorBodyBytes = 512
)

var (
	errRemoteRead = errors.New("prometheus remote read failed")
	castagnoli    = crc32.MakeTable(crc32.Castagnoli)
)

// RemoteRead reads a member's raw series over the Prometheus remote-read protocol, preferring
// streamed chunks over samples.
type RemoteRead struct {
	endpoint string
	client   *http.Client
	paths    po.List
}

// NewRemoteRead returns a source reading endpoint; requests carry the client's headers plus the
// request_headers paths sets for the client's path, as a proxied request would.
func NewRemoteRead(endpoint *url.URL, client *http.Client, paths po.List) *RemoteRead {
	return &RemoteRead{endpoint: endpoint.String(), client: client, paths: paths}
}

// SeriesAPI returns the remote-read series API name.
func (*RemoteRead) SeriesAPI() string { return prop.SeriesAPIRemoteRead }

// Select reads the series that match every matcher, with their samples from mint to maxt.
func (s *RemoteRead) Select(ctx context.Context, mint, maxt int64, hints *storage.SelectHints,
	matchers ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	series, err := s.read(ctx, mint, maxt, hints, matchers)
	return series, nil, err
}

func (s *RemoteRead) read(ctx context.Context, mint, maxt int64, hints *storage.SelectHints,
	matchers []*labels.Matcher,
) ([]storage.Series, error) {
	body, err := encodeReadRequest(mint, maxt, hints, matchers)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	s.setHeaders(req, requestFrom(ctx))
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRemoteRead, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, fmt.Errorf("%w: %s: %s", errRemoteRead, resp.Status, strings.TrimSpace(string(msg)))
	}
	budget := budgetFrom(ctx)
	var reader io.Reader = resp.Body
	if limit := budget.responseLimit(); limit > 0 {
		reader = &limitedReader{r: resp.Body, remaining: limit}
	}
	if strings.HasPrefix(resp.Header.Get(headers.NameContentType), contentTypeStreamed) {
		return decodeChunked(reader, budget)
	}
	return decodeSamples(reader, budget)
}

// setHeaders gives out the client request's end-to-end headers and the configured request headers
// for its path, then the remote-read protocol's own
func (s *RemoteRead) setHeaders(out, in *http.Request) {
	if in != nil {
		out.Header = in.Header.Clone()
		headers.StripClientHeaders(out.Header)
		for k := range out.Header {
			if isEntityHeader(k) {
				out.Header.Del(k)
			}
		}
		if pc := s.paths.Match(in.Method, in.URL.Path); pc != nil && len(pc.RequestHeaders) > 0 {
			headers.UpdateHeaders(out.Header, pc.RequestHeaders)
		}
	}
	out.Header.Set(headers.NameContentEncoding, contentEncodingSnappy)
	out.Header.Set(headers.NameContentType, contentTypeProtobuf)
	out.Header.Set(remoteReadVersionHeader, remoteReadVersion)
}

// isEntityHeader reports whether a client request header describes or conditions the client's own
// request or response, which a remote-read request replaces
func isEntityHeader(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "content-") || strings.HasPrefix(name, "accept") ||
		strings.HasPrefix(name, "if-") || name == "range" || name == "expect"
}

func encodeReadRequest(mint, maxt int64, hints *storage.SelectHints, matchers []*labels.Matcher) ([]byte, error) {
	q := &prompb.Query{
		StartTimestampMs: mint, EndTimestampMs: maxt,
		Matchers: make([]*prompb.LabelMatcher, len(matchers)),
	}
	for i, m := range matchers {
		var t prompb.LabelMatcher_Type
		switch m.Type {
		case labels.MatchEqual:
			t = prompb.LabelMatcher_EQ
		case labels.MatchNotEqual:
			t = prompb.LabelMatcher_NEQ
		case labels.MatchRegexp:
			t = prompb.LabelMatcher_RE
		case labels.MatchNotRegexp:
			t = prompb.LabelMatcher_NRE
		default:
			return nil, fmt.Errorf("%w: unsupported matcher type %s", errRemoteRead, m.Type)
		}
		q.Matchers[i] = &prompb.LabelMatcher{Type: t, Name: m.Name, Value: m.Value}
	}
	if hints != nil {
		q.Hints = &prompb.ReadHints{
			StepMs: hints.Step, Func: hints.Func, StartMs: hints.Start, EndMs: hints.End,
			Grouping: hints.Grouping, By: hints.By, RangeMs: hints.Range,
		}
	}
	req := &prompb.ReadRequest{Queries: []*prompb.Query{q}, AcceptedResponseTypes: []prompb.ReadRequest_ResponseType{
		prompb.ReadRequest_STREAMED_XOR_CHUNKS, prompb.ReadRequest_SAMPLES,
	}}
	data, err := req.Marshal()
	if err != nil {
		return nil, err
	}
	return snappy.Encode(nil, data), nil
}

// decodeChunked reads frames of a uvarint length, big-endian CRC-32C and ChunkedReadResponse;
// a series too large for one frame continues in the next
func decodeChunked(r io.Reader, budget *Budget) ([]storage.Series, error) {
	br := bufio.NewReader(r)
	var out []storage.Series
	var last *chunkSeries
	var frame []byte
	var builder labels.ScratchBuilder
	for {
		size, err := binary.ReadUvarint(br)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: frame length: %w", errRemoteRead, err)
		}
		if size > maxFrameBytes {
			return nil, fmt.Errorf("%w: frame of %d bytes exceeds %d", errRemoteRead, size, maxFrameBytes)
		}
		var crc [4]byte
		if _, err := io.ReadFull(br, crc[:]); err != nil {
			return nil, fmt.Errorf("%w: frame checksum: %w", errRemoteRead, err)
		}
		if cap(frame) < int(size) {
			frame = make([]byte, size)
		}
		frame = frame[:size]
		if _, err := io.ReadFull(br, frame); err != nil {
			return nil, fmt.Errorf("%w: frame: %w", errRemoteRead, err)
		}
		if crc32.Checksum(frame, castagnoli) != binary.BigEndian.Uint32(crc[:]) {
			return nil, fmt.Errorf("%w: frame checksum mismatch", errRemoteRead)
		}
		var res prompb.ChunkedReadResponse
		if err := res.Unmarshal(frame); err != nil {
			return nil, fmt.Errorf("%w: %w", errRemoteRead, err)
		}
		for _, cs := range res.ChunkedSeries {
			lset := cs.ToLabels(&builder, nil)
			var samples int64
			iterables := make([]chunkenc.Iterable, len(cs.Chunks))
			for i, c := range cs.Chunks {
				chk, err := decodeChunk(c)
				if err != nil {
					return nil, err
				}
				samples += int64(chk.NumSamples())
				iterables[i] = chk
			}
			var newSeries int64
			if last != nil && labels.Equal(last.lset, lset) {
				last.chunks = append(last.chunks, iterables...)
			} else {
				last = &chunkSeries{lset: lset, chunks: iterables}
				out = append(out, last)
				newSeries = 1
			}
			if err := budget.add(newSeries, samples); err != nil {
				return nil, err
			}
		}
	}
}

func decodeChunk(c prompb.Chunk) (chunkenc.Chunk, error) {
	var enc chunkenc.Encoding
	switch c.Type {
	case prompb.Chunk_XOR:
		enc = chunkenc.EncXOR
	case prompb.Chunk_XOR2:
		enc = chunkenc.EncXOR2
	case prompb.Chunk_HISTOGRAM:
		enc = chunkenc.EncHistogram
	case prompb.Chunk_FLOAT_HISTOGRAM:
		enc = chunkenc.EncFloatHistogram
	default:
		return nil, fmt.Errorf("%w: unsupported chunk encoding %s", errRemoteRead, c.Type)
	}
	chk, err := chunkenc.FromData(enc, c.Data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRemoteRead, err)
	}
	return chk, nil
}

// decodeSamples reads a snappy-compressed ReadResponse of samples
func decodeSamples(r io.Reader, budget *Budget) ([]storage.Series, error) {
	compressed, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRemoteRead, err)
	}
	size, err := snappy.DecodedLen(compressed)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRemoteRead, err)
	}
	if limit := budget.responseLimit(); limit > 0 && int64(size) > limit {
		return nil, fmt.Errorf("%w: response of %d bytes", ErrFetchLimit, size)
	}
	data, err := snappy.Decode(nil, compressed)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRemoteRead, err)
	}
	var res prompb.ReadResponse
	if err := res.Unmarshal(data); err != nil {
		return nil, fmt.Errorf("%w: %w", errRemoteRead, err)
	}
	var out []storage.Series
	var builder labels.ScratchBuilder
	for _, result := range res.Results {
		for _, ts := range result.Timeseries {
			samples := mergeSamples(ts)
			if err := budget.add(1, int64(len(samples))); err != nil {
				return nil, err
			}
			out = append(out, storage.NewListSeries(ts.ToLabels(&builder, nil), samples))
		}
	}
	return out, nil
}

// mergeSamples interleaves a series' float and histogram samples, each already in time order
func mergeSamples(ts *prompb.TimeSeries) []chunks.Sample {
	out := make([]chunks.Sample, 0, len(ts.Samples)+len(ts.Histograms))
	f, h := 0, 0
	for f < len(ts.Samples) || h < len(ts.Histograms) {
		if h == len(ts.Histograms) || (f < len(ts.Samples) && ts.Samples[f].Timestamp <= ts.Histograms[h].Timestamp) {
			out = append(out, sample{t: ts.Samples[f].Timestamp, f: ts.Samples[f].Value})
			f++
			continue
		}
		out = append(out, sample{t: ts.Histograms[h].Timestamp, fh: ts.Histograms[h].ToFloatHistogram()})
		h++
	}
	return out
}

// limitedReader fails, rather than ending early, once a response exceeds its byte limit
type limitedReader struct {
	r         io.Reader
	remaining int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		var probe [1]byte
		if n, _ := l.r.Read(probe[:]); n > 0 {
			return 0, fmt.Errorf("%w: response exceeds its byte limit", ErrFetchLimit)
		}
		return 0, io.EOF
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.r.Read(p)
	l.remaining -= int64(n)
	return n, err
}
