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

package options

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Series APIs name the transports a backend can serve its raw series over, for TSM global evaluation
const (
	SeriesAPIRemoteRead = "remote_read"
	SeriesAPIStoreAPI   = "storeapi"
)

const (
	// RemoteReadPath is the Prometheus remote-read route beneath a backend's origin path
	RemoteReadPath = "/api/v1/read"
	// StoreAPIScheme dials a StoreAPI series_endpoint in plaintext
	StoreAPIScheme = "grpc"
	// StoreAPISchemeTLS dials a StoreAPI series_endpoint with the backend's TLS client settings
	StoreAPISchemeTLS = "grpcs"

	schemeHTTP  = "http"
	schemeHTTPS = "https"

	// SeriesVolatileWindow is how far back from now a mimir or thanos backend's raw series may still change,
	// as its replicas and ingesters receive samples late
	SeriesVolatileWindow = 5 * time.Minute
)

var (
	// ErrUnknownSeriesAPI is returned for a series_api that is not one of the SeriesAPI constants
	ErrUnknownSeriesAPI = errors.New("unknown prometheus series_api")
	// ErrSeriesAPIFlavor is returned when a hosted flavor that serves no raw series sets a series option
	ErrSeriesAPIFlavor = errors.New("prometheus flavor serves no raw series")
	// ErrSeriesEndpoint is returned for a missing or malformed series_endpoint
	ErrSeriesEndpoint = errors.New("invalid prometheus series_endpoint")
)

// SeriesAPIs returns the supported series_api values.
func SeriesAPIs() []string {
	return []string{SeriesAPIRemoteRead, SeriesAPIStoreAPI}
}

// EffectiveSeriesAPI returns series_api, or the series API the flavor serves by default; it is
// empty when the backend serves no raw series.
func (o *Options) EffectiveSeriesAPI() string {
	if o == nil {
		return ""
	}
	if o.SeriesAPI != "" {
		return o.SeriesAPI
	}
	switch o.Flavor {
	case FlavorMimir:
		return SeriesAPIRemoteRead
	case FlavorThanos:
		return SeriesAPIStoreAPI
	}
	return ""
}

// EffectiveSeriesVolatileWindow returns how far back from now the backend's raw series may still change:
// SeriesVolatileWindow for the mimir and thanos flavors, and none for any other backend
func (o *Options) EffectiveSeriesVolatileWindow() time.Duration {
	if o != nil && (o.Flavor == FlavorMimir || o.Flavor == FlavorThanos) {
		return SeriesVolatileWindow
	}
	return 0
}

// SeriesEndpointURL returns where the backend serves its series API: series_endpoint when set,
// or else, for remote read, RemoteReadPath beneath originURL.
func (o *Options) SeriesEndpointURL(originURL string) (*url.URL, error) {
	switch o.EffectiveSeriesAPI() {
	case SeriesAPIRemoteRead:
		if o.SeriesEndpoint != "" {
			return parseRemoteReadEndpoint(o.SeriesEndpoint)
		}
		u, err := parseRemoteReadEndpoint(originURL)
		if err != nil {
			return nil, err
		}
		u.Path = strings.TrimSuffix(u.Path, "/") + RemoteReadPath
		u.RawPath, u.RawQuery, u.Fragment = "", "", ""
		return u, nil
	case SeriesAPIStoreAPI:
		if o.SeriesEndpoint == "" {
			return nil, fmt.Errorf("%w: series_api %s requires one", ErrSeriesEndpoint, SeriesAPIStoreAPI)
		}
		return parseStoreAPIEndpoint(o.SeriesEndpoint)
	}
	return nil, fmt.Errorf("%w: the backend has no series_api", ErrSeriesEndpoint)
}

func (o *Options) validateSeries() error {
	switch o.SeriesAPI {
	case "", SeriesAPIRemoteRead, SeriesAPIStoreAPI:
	default:
		return fmt.Errorf("%w %q; supported series APIs are %s", ErrUnknownSeriesAPI, o.SeriesAPI,
			strings.Join(SeriesAPIs(), ", "))
	}
	if (o.SeriesAPI != "" || o.SeriesEndpoint != "") && (o.Flavor == FlavorCloudWatch || o.Flavor == FlavorAMP) {
		return fmt.Errorf("%w: %s", ErrSeriesAPIFlavor, o.Flavor)
	}
	if o.SeriesEndpoint == "" {
		return nil
	}
	var err error
	switch o.EffectiveSeriesAPI() {
	case SeriesAPIRemoteRead:
		_, err = parseRemoteReadEndpoint(o.SeriesEndpoint)
	case SeriesAPIStoreAPI:
		_, err = parseStoreAPIEndpoint(o.SeriesEndpoint)
	default:
		err = fmt.Errorf("%w: it requires a series_api", ErrSeriesEndpoint)
	}
	return err
}

func parseRemoteReadEndpoint(endpoint string) (*url.URL, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != schemeHTTP && u.Scheme != schemeHTTPS) || u.Host == "" {
		return nil, fmt.Errorf("%w %q: remote read needs an absolute http or https URL", ErrSeriesEndpoint, endpoint)
	}
	return u, nil
}

func parseStoreAPIEndpoint(endpoint string) (*url.URL, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != StoreAPIScheme && u.Scheme != StoreAPISchemeTLS) || u.Hostname() == "" ||
		u.Port() == "" || strings.Trim(u.Path, "/") != "" {
		return nil, fmt.Errorf("%w %q: StoreAPI needs %s://host:port or %s://host:port", ErrSeriesEndpoint,
			endpoint, StoreAPIScheme, StoreAPISchemeTLS)
	}
	return u, nil
}
