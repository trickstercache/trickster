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
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/remoteread"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"

	"github.com/klauspost/compress/snappy"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/storage"
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
	SourceIdentity
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
		d := newDecoder(budget, mint, maxt, errRemoteRead)
		d.replicaLabels = replicaLabelsFrom(ctx)
		return decodeChunked(reader, d)
	}
	return decodeSamples(reader, budget, mint, maxt)
}

func (s *RemoteRead) sentHeaders(in *http.Request) http.Header { return forwardedHeaders(s.paths, in) }

// setHeaders gives out the client request's end-to-end headers and the configured request headers
// for its path, then the remote-read protocol's own
func (s *RemoteRead) setHeaders(out, in *http.Request) {
	out.Header = s.sentHeaders(in)
	for k := range out.Header {
		if isEntityHeader(k) {
			out.Header.Del(k)
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

// decodeChunked reads frames of a uvarint length, big-endian CRC-32C and ChunkedReadResponse through d, a series
// continuing across frames; only what's within d's range is kept, counted against its budget
func decodeChunked(r io.Reader, d *decoder) ([]storage.Series, error) {
	br := bufio.NewReader(r)
	budget := d.budget
	var frame []byte
	for {
		size, err := binary.ReadUvarint(br)
		if errors.Is(err, io.EOF) {
			return d.out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: frame length: %w", errRemoteRead, err)
		}
		// a frame over either limit is refused before its buffer is allocated
		if size > maxFrameBytes {
			return nil, fmt.Errorf("%w: a remote read frame of %d bytes exceeds %d", ErrFetchLimit, size, maxFrameBytes)
		}
		if limit := budget.responseLimit(); limit > 0 && size > uint64(limit) {
			return nil, fmt.Errorf("%w: a remote read frame of %d bytes exceeds %d", ErrFetchLimit, size, limit)
		}
		var crc [4]byte
		if _, err := io.ReadFull(br, crc[:]); err != nil {
			return nil, fmt.Errorf("%w: frame checksum: %w", errRemoteRead, err)
		}
		if cap(frame) < int(size) {
			d.release()
			frame = make([]byte, size)
		}
		frame = frame[:size]
		if _, err := io.ReadFull(br, frame); err != nil {
			return nil, fmt.Errorf("%w: frame: %w", errRemoteRead, err)
		}
		if crc32.Checksum(frame, castagnoli) != binary.BigEndian.Uint32(crc[:]) {
			return nil, fmt.Errorf("%w: frame checksum mismatch", errRemoteRead)
		}
		if err := d.chunkedReadResponse(frame); err != nil {
			return nil, err
		}
	}
}

// decodeSamples reads a snappy-compressed ReadResponse of samples; only what lies from mint to maxt is kept,
// counted against the budget
func decodeSamples(r io.Reader, budget *Budget, mint, maxt int64) ([]storage.Series, error) {
	compressed, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRemoteRead, err)
	}
	data, err := remoteread.DecodeSnappy(compressed, budget.responseLimit(), ErrFetchLimit)
	if errors.Is(err, ErrFetchLimit) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRemoteRead, err)
	}
	d := newDecoder(budget, mint, maxt, errRemoteRead)
	if err := d.readResponse(data); err != nil {
		return nil, err
	}
	return d.out, nil
}

// limitedReader fails, rather than ending early, once a response exceeds its byte limit; at the limit, a response
// ends only at its own EOF, so a transport error there is never mistaken for a complete response
type limitedReader struct {
	r         io.Reader
	remaining int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		// a byte past the limit fails the read whatever error comes with it; otherwise the probe's own result is
		// the read's: EOF, a transport error, or nothing read and no error, which a caller reads again
		var probe [1]byte
		n, err := l.r.Read(probe[:])
		if n > 0 {
			return 0, fmt.Errorf("%w: response exceeds its byte limit", ErrFetchLimit)
		}
		return 0, err
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.r.Read(p)
	l.remaining -= int64(n)
	return n, err
}
