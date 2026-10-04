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

package static

import (
	"io"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// cacheStatus is how the Fileserver cache figured in a response, as reported in metrics
type cacheStatus int

const (
	// statusHit is a response served as it was held
	statusHit cacheStatus = iota
	// statusPartialHit is a held file encoded for the response, and the rendition then held
	statusPartialHit
	// statusMiss is a file read from disk for the response, and then held
	statusMiss
	// statusDisk is a file sent from disk without being held
	statusDisk
	numCacheStatuses
)

const (
	statusDiskLabel = "disk"
	identityLabel   = "identity"
	// maxRendition is the highest-valued encoding, which sizes tables indexed by encoding
	maxRendition = providers.Deflate
)

var cacheStatusLabels = [numCacheStatuses]string{
	statusHit:        status.StatusHit,
	statusPartialHit: status.StatusPartialHit,
	statusMiss:       status.StatusKeyMiss,
	statusDisk:       statusDiskLabel,
}

func encodingLabel(enc providers.Provider) string {
	if enc == providers.Identity {
		return identityLabel
	}
	return enc.String()
}

// minRenditionSize is the smallest file worth encoding. Below it an encoding's own
// framing outweighs what it saves, so the file is sent, and held, only as it is stored.
const minRenditionSize = 512

// newEncoder returns a streaming encoder for enc. It is replaced in tests to fail.
var newEncoder = func(enc providers.Provider, w io.Writer) io.WriteCloser {
	init, _ := providers.SelectEncoderInitializer(enc)
	return init(w, -1)
}

// acceptedEncodings returns what a request can be served a rendition in, most preferred
// first. That is nothing for one that isn't a GET or HEAD, or asks for a byte range,
// as ranges address the stored file.
func acceptedEncodings(r *http.Request) providers.Accepted {
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) ||
		r.Header.Get(headers.NameRange) != "" {
		return providers.Accepted{}
	}
	// already worked out by the response path where there is one, which may also have
	// narrowed what it will pass through since
	if ep := profile.FromContext(r.Context()); ep != nil {
		return ep.Accepted.Filter(ep.Supported)
	}
	return providers.ParseAcceptEncoding(r.Header[headers.NameAcceptEncoding]...)
}

// teeBuffer collects what is written to it up to a limit, past which it gives up
// collecting rather than fail the write, as the response it shadows must carry on.
type teeBuffer struct {
	buf      []byte
	limit    int
	overflow bool
}

func (t *teeBuffer) Write(b []byte) (int, error) {
	if !t.overflow {
		if len(t.buf)+len(b) > t.limit {
			t.overflow, t.buf = true, nil
		} else {
			t.buf = append(t.buf, b...)
		}
	}
	return len(b), nil
}
