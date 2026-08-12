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

package urls

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

type upstreamURLComponent uint8

const (
	upstreamScheme upstreamURLComponent = iota
	upstreamHost
	upstreamHostname
	upstreamPort
)

type upstreamURLRewrite struct {
	component upstreamURLComponent
	value     string
}

type upstreamURLRewritesKey struct{}

// SetUpstreamScheme records a request rewriter's explicit upstream scheme.
func SetUpstreamScheme(r *http.Request, scheme string) {
	setUpstreamURLRewrite(r, upstreamScheme, scheme)
}

// SetUpstreamHost records a request rewriter's explicit upstream host and port.
func SetUpstreamHost(r *http.Request, host string) {
	setUpstreamURLRewrite(r, upstreamHost, host)
}

// SetUpstreamHostname records a request rewriter's explicit upstream hostname.
func SetUpstreamHostname(r *http.Request, hostname string) {
	setUpstreamURLRewrite(r, upstreamHostname, hostname)
}

// SetUpstreamPort records a request rewriter's explicit upstream port.
func SetUpstreamPort(r *http.Request, port string) {
	setUpstreamURLRewrite(r, upstreamPort, port)
}

func setUpstreamURLRewrite(r *http.Request, component upstreamURLComponent, value string) {
	if r == nil {
		return
	}
	rewrites, _ := r.Context().Value(upstreamURLRewritesKey{}).([]upstreamURLRewrite)
	rewrites = append(slices.Clone(rewrites), upstreamURLRewrite{
		component: component,
		value:     value,
	})
	*r = *r.WithContext(context.WithValue(r.Context(), upstreamURLRewritesKey{}, rewrites))
}

func applyUpstreamURLRewrites(r *http.Request, u *url.URL) {
	if r == nil || u == nil {
		return
	}
	rewrites, _ := r.Context().Value(upstreamURLRewritesKey{}).([]upstreamURLRewrite)
	for _, rewrite := range rewrites {
		switch rewrite.component {
		case upstreamScheme:
			u.Scheme = rewrite.value
		case upstreamHost:
			u.Host = rewrite.value
		case upstreamHostname:
			u.Host = replaceHostname(u.Host, rewrite.value)
		case upstreamPort:
			u.Host = replacePort(u.Host, rewrite.value)
		}
	}
}

// UpstreamURLRewriteCacheKey returns a cache key component when a request
// rewriter changed the configured upstream authority.
func UpstreamURLRewriteCacheKey(r *http.Request, base *url.URL) string {
	if r == nil || base == nil {
		return ""
	}
	rewrites, _ := r.Context().Value(upstreamURLRewritesKey{}).([]upstreamURLRewrite)
	if len(rewrites) == 0 {
		return ""
	}
	rewritten := Clone(base)
	applyUpstreamURLRewrites(r, rewritten)
	if rewritten.Scheme == base.Scheme && rewritten.Host == base.Host {
		return ""
	}
	return "\x00upstream=" + rewritten.Scheme + "://" + rewritten.Host + "\x00"
}

func replaceHostname(host, hostname string) string {
	port := (&url.URL{Host: host}).Port()
	return joinHostPort(hostname, port)
}

func replacePort(host, port string) string {
	hostname := (&url.URL{Host: host}).Hostname()
	return joinHostPort(hostname, port)
}

func joinHostPort(hostname, port string) string {
	hostname = strings.TrimPrefix(strings.TrimSuffix(hostname, "]"), "[")
	if port != "" {
		return net.JoinHostPort(hostname, port)
	}
	if strings.Contains(hostname, ":") {
		return "[" + hostname + "]"
	}
	return hostname
}
