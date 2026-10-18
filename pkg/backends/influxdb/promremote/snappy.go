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

package promremote

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/remoteread"
	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"github.com/klauspost/compress/snappy"
)

var errBodyTooLarge = errors.New("prometheus remote-read body exceeds decode limit")

func requestDecodeLimit(r *http.Request) int {
	if rsc := request.GetResources(r); rsc != nil && rsc.BackendOptions != nil &&
		rsc.BackendOptions.MaxCaptureBytes > 0 {
		return rsc.BackendOptions.MaxCaptureBytes
	}
	return bo.DefaultMaxCaptureBytes
}

func responseDecodeLimit(trq any) int {
	if parsed, ok := trq.(*parsedRequest); ok && parsed != nil && parsed.decodeLimit > 0 {
		return parsed.decodeLimit
	}
	return bo.DefaultMaxCaptureBytes
}

func decodeSnappyBlock(compressed []byte, maxDecoded int) ([]byte, error) {
	if maxDecoded <= 0 {
		maxDecoded = bo.DefaultMaxCaptureBytes
	}
	return remoteread.DecodeSnappy(compressed, int64(maxDecoded), errBodyTooLarge)
}

func readSnappyBlock(reader io.Reader, maxDecoded int) ([]byte, error) {
	if maxDecoded <= 0 {
		maxDecoded = bo.DefaultMaxCaptureBytes
	}
	maxEncoded := snappy.MaxEncodedLen(maxDecoded)
	if maxEncoded < 0 {
		maxEncoded = maxDecoded
	}
	compressed, err := tbytes.ReadBoundedBody(reader, maxEncoded, false)
	if errors.Is(err, tbytes.ErrBodyTooLarge) {
		return nil, fmt.Errorf("%w: compressed size exceeds %d", errBodyTooLarge, maxEncoded)
	}
	if err != nil {
		return nil, err
	}
	return decodeSnappyBlock(compressed, maxDecoded)
}
