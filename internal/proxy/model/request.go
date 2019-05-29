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
	"net/url"
	"time"

	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/timeseries"
)

// Request contains information about an inbound HTTP request
type Request struct {
	OriginName         string
	OriginType         string
	HandlerName        string
	HTTPMethod         string
	URL                *url.URL
	TemplateURL        *url.URL
	Headers            http.Header
	ClientRequest      *http.Request
	Timeout            time.Duration
	TimeRangeQuery     *timeseries.TimeRangeQuery
	FastForwardDisable bool
	HTTPClient         *http.Client
}

// NewRequest returns a new proxy request object that can service the downstream request
func NewRequest(originName, originType, handlerName, method string, url *url.URL, headers http.Header, timeout time.Duration, clientRequest *http.Request, client *http.Client) *Request {
	return &Request{
		OriginName:    originName,
		OriginType:    originType,
		HandlerName:   handlerName,
		HTTPMethod:    method,
		URL:           url,
		TemplateURL:   CopyURL(url),
		Headers:       headers,
		ClientRequest: clientRequest,
		Timeout:       timeout,
		HTTPClient:    client,
	}
}

// Copy returns a true copy of the request
func (r *Request) Copy() *Request {
	return &Request{
		OriginName:    r.OriginName,
		OriginType:    r.OriginType,
		HandlerName:   r.HandlerName,
		HTTPMethod:    r.HTTPMethod,
		URL:           CopyURL(r.URL),
		TemplateURL:   CopyURL(r.TemplateURL),
		Headers:       headers.CopyHeaders(r.Headers),
		ClientRequest: r.ClientRequest,
		HTTPClient:    r.HTTPClient,
	}
}

// CopyURL returns a deep copy of a *url.URL
func CopyURL(u *url.URL) *url.URL {
	return &url.URL{
		Scheme:   u.Scheme,
		Host:     u.Host,
		Path:     u.Path,
		RawQuery: u.RawQuery,
		Fragment: u.Fragment,
		User:     u.User,
	}
}
