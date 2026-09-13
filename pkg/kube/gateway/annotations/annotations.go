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

// Package annotations parses the Prefix-namespaced annotations that tune how a
// Kubernetes object is translated.
//
// An annotation outside Prefix is not this controller's business and is
// passed over without comment. One inside it is rejected when it is unknown
// or its value does not parse: the annotation is not applied, the rest of the
// object still translates, and the rejection is reported so the controller
// can log it and publish an Event. Failing the whole object instead would
// let one typo delete a live route, and applying it silently would leave an
// operator believing a setting is in force when it is not.
package annotations

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/internal/translate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

// Prefix is the annotation namespace this controller owns
const Prefix = appinfo.Domain + "/"

// The annotation set this build understands
const (
	// Handler selects the path handler: proxy or proxycache
	Handler = Prefix + "handler"
	// CacheName names the configured cache a caching route uses
	CacheName = Prefix + "cache-name"
	// MaxTTL caps how long a cached object is served before revalidation
	MaxTTL = Prefix + "max-ttl"
	// NegativeCacheName names the configured negative cache that decides
	// how long error responses are cached
	NegativeCacheName = Prefix + "negative-cache-name"
	// RequestHeaders and ResponseHeaders rewrite headers, one "Name: value"
	// per line; a name prefixed with '-' deletes and one with '+' appends
	RequestHeaders  = Prefix + "request-headers"
	ResponseHeaders = Prefix + "response-headers"
	// CORSMode is preserve, merge, replace or disable
	CORSMode = Prefix + "cors-mode"
	// CORSHeaders are the CORS response headers merge and replace apply
	CORSHeaders = Prefix + "cors-headers"
	// Timeout is the upstream request timeout
	Timeout = Prefix + "timeout"
	// CollapsedForwarding is basic or progressive
	CollapsedForwarding = Prefix + "collapsed-forwarding"
	// UseRegex compiles ImplementationSpecific paths as regular expressions
	UseRegex = Prefix + "use-regex"
	// RewriteTarget replaces the matched path on the way upstream; with
	// UseRegex it may interpolate the match's captures as ${1}
	RewriteTarget = Prefix + "rewrite-target"
	// HealthMode selects how the discovered members of a generated ALB are
	// judged healthy in the endpoint routing mode: probe or provider
	HealthMode = Prefix + "health-mode"
)

// Problem is one rejected annotation, for logging and for the Events and
// status conditions the controller publishes
type Problem struct {
	// Annotation is the full annotation key, including the prefix
	Annotation string
	// Value is what the object carried
	Value string
	// Reason explains why it was not applied
	Reason string
}

// String renders the problem for a log line
func (p Problem) String() string {
	return p.Annotation + ": " + p.Reason
}

// Set is one object's parsed annotations
type Set struct {
	// Policy carries every annotation the compiler acts on; Name and Source are the caller's to
	// fill in, because they identify the object rather than the annotations
	Policy ir.Policy
	// UseRegex compiles ImplementationSpecific paths as regular expressions;
	// it is the translator's to act on, not the compiler's
	UseRegex bool
}

// ConfiguresPolicy reports whether the annotations set anything the compiler acts on, so a caller
// can skip emitting a policy no rule reads; UseRegex is the translator's and never reaches a policy
func (s *Set) ConfiguresPolicy() bool {
	if s == nil {
		return false
	}
	p := &s.Policy
	return p.Handler != "" || p.CacheName != "" || p.NegativeCacheName != "" ||
		p.TimeoutMS > 0 || p.MaxTTLMS > 0 || p.CORSMode != "" ||
		p.CollapsedForwarding != "" || p.RewriteTarget != "" ||
		p.HealthMode != "" ||
		len(p.RequestHeaders) > 0 || len(p.ResponseHeaders) > 0 ||
		len(p.CORSHeaders) > 0
}

// reasons for rejection
const (
	reasonUnknown = "not a recognized " + appinfo.Domain + " annotation"
	reasonEmpty   = "value is empty"
)

// Parse reads an object's annotations, returning what they configure and
// every Prefix annotation that was rejected
func Parse(in map[string]string) (*Set, []Problem) {
	s := &Set{}
	var problems []Problem
	reject := func(k, v, reason string) {
		problems = append(problems, Problem{Annotation: k, Value: v, Reason: reason})
	}
	for _, k := range sortedKeys(in) {
		if !strings.HasPrefix(k, Prefix) {
			continue
		}
		v := strings.TrimSpace(in[k])
		if v == "" {
			reject(k, in[k], reasonEmpty)
			continue
		}
		if err := s.apply(k, v); err != nil {
			reject(k, in[k], err.Error())
		}
	}
	return s, problems
}

func sortedKeys(in map[string]string) []string {
	out := make([]string, 0, len(in))
	for k := range in {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (s *Set) apply(key, value string) (err error) {
	switch key {
	case Handler:
		s.Policy.Handler, err = translate.Handler(value)
	case CacheName:
		s.Policy.CacheName = value
		return nil
	case MaxTTL:
		d, err := timeconv.ParsePositiveDuration(value)
		if err != nil {
			return err
		}
		s.Policy.MaxTTLMS = d.Milliseconds()
		return nil
	case Timeout:
		d, err := timeconv.ParsePositiveDuration(value)
		if err != nil {
			return err
		}
		s.Policy.TimeoutMS = d.Milliseconds()
		return nil
	case NegativeCacheName:
		s.Policy.NegativeCacheName = value
		return nil
	case RequestHeaders:
		s.Policy.RequestHeaders, err = translate.Headers(value)
	case ResponseHeaders:
		s.Policy.ResponseHeaders, err = translate.Headers(value)
	case CORSMode:
		s.Policy.CORSMode, err = translate.CORSMode(value)
	case CORSHeaders:
		s.Policy.CORSHeaders, err = translate.Headers(value)
	case CollapsedForwarding:
		s.Policy.CollapsedForwarding, err = translate.CollapsedForwarding(value)
	case UseRegex:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("must be a boolean (got %q)", value)
		}
		s.UseRegex = b
		return nil
	case RewriteTarget:
		if strings.ContainsAny(value, " \t") {
			return fmt.Errorf("must not contain whitespace (got %q)", value)
		}
		s.Policy.RewriteTarget = value
		return nil
	case HealthMode:
		s.Policy.HealthMode, err = translate.HealthMode(value)
	default:
		return errors.New(reasonUnknown)
	}
	return err
}
