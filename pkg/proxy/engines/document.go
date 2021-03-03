/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package engines

import (
	"bytes"
	"errors"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"sync"

	tl "github.com/tricksterproxy/trickster/pkg/observability/logging"
	txe "github.com/tricksterproxy/trickster/pkg/proxy/errors"
	"github.com/tricksterproxy/trickster/pkg/proxy/headers"
	"github.com/tricksterproxy/trickster/pkg/proxy/ranges/byterange"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

//go:generate msgp

// HTTPDocument represents a full HTTP Response/Cache Document with unbuffered body
type HTTPDocument struct {
	StatusCode    int                 `msg:"status_code"`
	Status        string              `msg:"status"`
	Headers       map[string][]string `msg:"headers"`
	Body          []byte              `msg:"body"`
	ContentLength int64               `msg:"content_length"`
	ContentType   string              `msg:"content_type"`
	CachingPolicy *CachingPolicy      `msg:"caching_policy"`
	// Ranges is the list of Byte Ranges contained in the body of this document
	Ranges     byterange.Ranges              `msg:"ranges"`
	RangeParts byterange.MultipartByteRanges `msg:"-"`
	// StoredRangeParts is a version of RangeParts that can be exported to MessagePack
	StoredRangeParts map[string]*byterange.MultipartByteRange `msg:"range_parts"`

	rangePartsLoaded bool
	isFulfillment    bool
	isLoaded         bool
	timeseries       timeseries.Timeseries
	headerLock       sync.Mutex
}

// SafeHeaderClone returns a threadsafe copy of the Document Header
func (d *HTTPDocument) SafeHeaderClone() http.Header {
	d.headerLock.Lock()
	h := http.Header(d.Headers).Clone()
	d.headerLock.Unlock()
	return h
}

// Size returns the size of the HTTPDocument's headers, CachingPolicy, RangeParts, Body and timeseries data
func (d *HTTPDocument) Size() int {
	var i int
	d.headerLock.Lock()
	i += len(headers.String(http.Header(d.Headers)))
	d.headerLock.Unlock()
	i += len(d.Body)
	if d.RangeParts != nil {
		for _, p := range d.RangeParts {
			i += p.Msgsize()
		}
	}
	if d.CachingPolicy != nil {
		i += d.CachingPolicy.Msgsize()
	}
	if d.timeseries != nil {
		i += int(d.timeseries.Size())
	}
	return i
}

// SetBody sets the Document Body as well as the Content Length, based on the length of body.
// This assumes that the caller has checked that the request is not a Range request
func (d *HTTPDocument) SetBody(body []byte) {
	if body == nil {
		return
	}
	d.Body = body
	bl := int64(len(d.Body))
	if d.ContentLength == -1 || d.ContentLength != bl {
		d.ContentLength = bl
	}
	if d.Headers == nil {
		d.Headers = make(http.Header)
	}
	d.headerLock.Lock()
	http.Header(d.Headers).Set(headers.NameContentLength, strconv.Itoa(len(body)))
	d.headerLock.Unlock()
}

// LoadRangeParts convert a StoredRangeParts into a RangeParts
func (d *HTTPDocument) LoadRangeParts() {

	if d.rangePartsLoaded {
		return
	}

	if d.StoredRangeParts != nil && len(d.StoredRangeParts) > 0 {
		d.RangeParts = make(byterange.MultipartByteRanges)
		for _, p := range d.StoredRangeParts {
			d.RangeParts[p.Range] = p
		}
		d.Ranges = d.RangeParts.Ranges()
	}
	d.rangePartsLoaded = true
}

// ParsePartialContentBody parses a Partial Content response body into 0 or more discrete parts
func (d *HTTPDocument) ParsePartialContentBody(resp *http.Response, body []byte, logger interface{}) {

	ct := resp.Header.Get(headers.NameContentType)
	if cr := resp.Header.Get(headers.NameContentRange); cr != "" {
		if !strings.HasPrefix(ct, headers.ValueMultipartByteRanges) {
			d.ContentType = ct
		}
		r, cl, err := byterange.ParseContentRangeHeader(cr)
		d.ContentLength = cl
		if err == nil && (r.Start >= 0 || r.End >= 0) {
			mpbr := &byterange.MultipartByteRange{Range: r, Content: body}
			if d.RangeParts == nil {
				d.RangeParts = byterange.MultipartByteRanges{r: mpbr}
			} else {
				d.RangeParts[r] = mpbr
			}
		}
		if d.RangeParts != nil {
			d.RangeParts.Compress()
			d.Ranges = d.RangeParts.Ranges()

			if d.RangeParts != nil {
				d.StoredRangeParts = d.RangeParts.PackableMultipartByteRanges()
			}
		}
	} else if strings.HasPrefix(ct, headers.ValueMultipartByteRanges) {
		p, ct, r, cl, err := byterange.ParseMultipartRangeResponseBody(ioutil.NopCloser(bytes.NewReader(body)), ct)
		if err == nil {
			if d.RangeParts == nil {
				d.Ranges = r
				d.RangeParts = p
			} else {
				d.RangeParts.Merge(p)
				d.Ranges = d.RangeParts.Ranges()
			}
			d.StoredRangeParts = d.RangeParts.PackableMultipartByteRanges()
			d.ContentLength = cl
			if !strings.HasPrefix(ct, headers.ValueMultipartByteRanges) {
				d.ContentType = ct
			}
			d.RangeParts.Compress()
			d.Ranges = d.RangeParts.Ranges()
		} else {
			tl.Error(logger, "unable to parse multipart range response body", tl.Pairs{"detail": err.Error})
		}
	} else {
		if !strings.HasPrefix(ct, headers.ValueMultipartByteRanges) {
			d.ContentType = ct
		}
		d.SetBody(body)
	}

	if d.ContentLength > 0 && len(d.RangeParts) == 1 &&
		d.RangeParts[d.RangeParts.Ranges()[0]].Range.Start == 0 &&
		d.RangeParts[d.RangeParts.Ranges()[0]].Range.End == d.ContentLength-1 {
		d.FulfillContentBody()
	}

	d.headerLock.Lock()
	http.Header(d.Headers).Del(headers.NameContentType)
	d.headerLock.Unlock()
}

// FulfillContentBody will concatenate the document's Range parts into a single, full content body
// the caller must know that document's multipart ranges include full content length before calling this method
func (d *HTTPDocument) FulfillContentBody() error {

	if d.RangeParts == nil || len(d.RangeParts) == 0 {
		d.SetBody(nil)
		return txe.ErrNoRanges
	}

	d.RangeParts.Compress()
	d.Ranges = d.RangeParts.Ranges()

	if len(d.RangeParts) != 1 {
		d.SetBody(nil)
		return errors.New("cached parts do not comprise the full body")
	}

	p := d.RangeParts[d.Ranges[0]]
	r := p.Range

	if r.Start != 0 || r.End != d.ContentLength-1 {
		d.SetBody(nil)
		return errors.New("cached parts do not comprise the full body")
	}

	d.StatusCode = http.StatusOK

	d.Ranges = nil
	d.RangeParts = nil
	d.StoredRangeParts = nil
	d.SetBody(p.Content)
	return nil
}
