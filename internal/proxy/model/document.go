/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package model

import (
	"net/http"
	"time"
)

//go:generate msgp

// HTTPDocument represents a full HTTP Response/Cache Document with unbuffered body
type HTTPDocument struct {
	StatusCode    int                 `msg:"status_code"`
	Status        string              `msg:"status"`
	Headers       map[string][]string `msg:"headers"`
	Body          []byte              `msg:"body"`
	CachingPolicy *CachingPolicy      `msg:"caching_policy"`
}

// CachingPolicy ...
type CachingPolicy struct {
	IsFresh               bool      `msg:"is_fresh"`
	NoCache               bool      `msg:"nocache"`
	NoTransform           bool      `msg:"notransform"`
	FreshnessLifetime     int       `msg:"freshness_lifetime"`
	CanRevalidate         bool      `msg:"can_revalidate"`
	MustRevalidate        bool      `msg:"must_revalidate"`
	LastModified          time.Time `msg:"last_modified"`
	Expires               time.Time `msg:"expires"`
	Date                  time.Time `msg:"date"`
	LocalDate             time.Time `msg:"local_date"`
	ETag                  string    `msg:"etag"`
	IfNoneMatchValue      string    `msg:"if_none_match_value"`
	IfMatchValue          string    `msg:"if_match_value"`
	IfModifiedSinceTime   time.Time `msg:"if_modified_since_time"`
	IfUnmodifiedSinceTime time.Time `msg:"if_unmodified_since_time"`
}

// DocumentFromHTTPResponse returns an HTTPDocument from the provided HTTP Response and Body
func DocumentFromHTTPResponse(resp *http.Response, body []byte, cp *CachingPolicy) *HTTPDocument {
	d := &HTTPDocument{}
	d.Headers = resp.Header
	d.StatusCode = resp.StatusCode
	d.Status = resp.Status
	d.Body = body
	d.CachingPolicy = cp
	return d
}
