/*
 * Copyright 2026 The Trickster Authors
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

package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"regexp/syntax"

	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// grpcCatchAll is the path a GRPCRoute match naming no method lowers to
const grpcCatchAll = "^/.*"

// errAnchorPlacement refuses a segment expression whose anchor does not sit at the segment's
// edge, since the lowering cannot keep its meaning there
var errAnchorPlacement = errors.New(
	"a ^ or $ in a service or method expression must be at the expression's start or end")

func grpcAsHTTP(gr *gwapiv1.GRPCRoute) (*gwapiv1.HTTPRoute, error) {
	// gRPC is HTTP on the wire: every call is a POST to /{service}/{method}, so a method
	// match is a path match, a header match is the same, and filters and backendRefs share types
	out := &gwapiv1.HTTPRoute{ObjectMeta: gr.ObjectMeta}
	out.Spec.CommonRouteSpec = gr.Spec.CommonRouteSpec
	out.Spec.Hostnames = gr.Spec.Hostnames
	for i, rule := range gr.Spec.Rules {
		hr := gwapiv1.HTTPRouteRule{
			Name:               rule.Name,
			Filters:            grpcFilters(rule.Filters),
			SessionPersistence: rule.SessionPersistence,
		}
		for j, m := range rule.Matches {
			hm, err := grpcMatch(m)
			if err != nil {
				return nil, fmt.Errorf("rule %d match %d: %w", i, j, err)
			}
			hr.Matches = append(hr.Matches, hm)
		}
		if len(hr.Matches) == 0 {
			hm, _ := grpcMatch(gwapiv1.GRPCRouteMatch{})
			hr.Matches = []gwapiv1.HTTPRouteMatch{hm}
		}
		for _, ref := range rule.BackendRefs {
			hr.BackendRefs = append(hr.BackendRefs, gwapiv1.HTTPBackendRef{
				BackendRef: ref.BackendRef, Filters: grpcFilters(ref.Filters),
			})
		}
		out.Spec.Rules = append(out.Spec.Rules, hr)
	}
	return out, nil
}

func grpcMatch(m gwapiv1.GRPCRouteMatch) (gwapiv1.HTTPRouteMatch, error) {
	// an exact service and method is the exact path, a service alone the prefix beneath it,
	// and anything else a pattern over both segments
	method := gwapiv1.HTTPMethod(http.MethodPost)
	path, err := grpcPath(m.Method)
	if err != nil {
		return gwapiv1.HTTPRouteMatch{}, err
	}
	out := gwapiv1.HTTPRouteMatch{Method: &method, Path: path}
	for _, h := range m.Headers {
		hm := gwapiv1.HTTPHeaderMatch{Name: gwapiv1.HTTPHeaderName(h.Name), Value: h.Value}
		if h.Type != nil {
			t := gwapiv1.HeaderMatchType(*h.Type)
			hm.Type = &t
		}
		out.Headers = append(out.Headers, hm)
	}
	return out, nil
}

func grpcPath(m *gwapiv1.GRPCMethodMatch) (*gwapiv1.HTTPPathMatch, error) {
	// a match naming no method is a catch-all in the regex tier rather than the root prefix,
	// so a method-only match, which is also a pattern, still outranks it: the router tries
	// prefixes before patterns and orders patterns longest first
	if m == nil || (deref(m.Service) == "" && deref(m.Method) == "") {
		typ, value := gwapiv1.PathMatchRegularExpression, grpcCatchAll
		return &gwapiv1.HTTPPathMatch{Type: &typ, Value: &value}, nil
	}
	prefix := gwapiv1.PathMatchPathPrefix
	service, method := deref(m.Service), deref(m.Method)
	exact := m.Type == nil || *m.Type == gwapiv1.GRPCMethodMatchExact
	switch {
	case exact && method == "":
		value := "/" + service
		return &gwapiv1.HTTPPathMatch{Type: &prefix, Value: &value}, nil
	case exact && service != "":
		typ, value := gwapiv1.PathMatchExact, "/"+service+"/"+method
		return &gwapiv1.HTTPPathMatch{Type: &typ, Value: &value}, nil
	}
	// a regular expression names one segment each; an absent one is any segment, and an
	// exact method without a service is the method under any service
	if exact {
		service, method = anySegment, regexp.QuoteMeta(method)
	} else {
		var err error
		if service, err = grpcSegment(service); err != nil {
			return nil, fmt.Errorf("service %q: %w", deref(m.Service), err)
		}
		if method, err = grpcSegment(method); err != nil {
			return nil, fmt.Errorf("method %q: %w", deref(m.Method), err)
		}
	}
	typ, value := gwapiv1.PathMatchRegularExpression, "^/"+service+"/"+method+"$"
	return &gwapiv1.HTTPPathMatch{Type: &typ, Value: &value}, nil
}

// anySegment matches one path segment, which a gRPC service or method name is
const anySegment = "[^/]+"

func grpcSegment(expr string) (string, error) {
	// a segment's expression is grouped, so its alternatives stay within the segment; an anchor
	// at the expression's edge is already satisfied, since the group spans the whole segment,
	// and becomes an empty match, while one anywhere else changes meaning and is refused
	if expr == "" {
		return anySegment, nil
	}
	re, err := syntax.Parse(expr, syntax.Perl)
	if err != nil {
		// the compile error is reported where the path is lowered
		return "(?:" + expr + ")", nil
	}
	if !edgeAnchors(re, true, true) {
		return "", errAnchorPlacement
	}
	if unanchor(re) {
		expr = re.String()
	}
	return "(?:" + expr + ")", nil
}

func edgeAnchors(re *syntax.Regexp, atStart, atEnd bool) bool {
	// an anchor is at the edge when nothing that consumes input can precede (^) or follow ($)
	// it on every way through the expression; a repetition puts its body at neither edge
	switch re.Op {
	case syntax.OpBeginLine, syntax.OpBeginText:
		return atStart
	case syntax.OpEndLine, syntax.OpEndText:
		return atEnd
	case syntax.OpConcat:
		for i, sub := range re.Sub {
			end := atEnd
			for _, later := range re.Sub[i+1:] {
				if !zeroWidth(later) {
					end = false
					break
				}
			}
			if !edgeAnchors(sub, atStart, end) {
				return false
			}
			if !zeroWidth(sub) {
				atStart = false
			}
		}
		return true
	case syntax.OpAlternate, syntax.OpCapture:
		for _, sub := range re.Sub {
			if !edgeAnchors(sub, atStart, atEnd) {
				return false
			}
		}
		return true
	case syntax.OpQuest:
		return edgeAnchors(re.Sub[0], atStart, atEnd)
	case syntax.OpStar, syntax.OpPlus:
		return edgeAnchors(re.Sub[0], false, false)
	case syntax.OpRepeat:
		if re.Max == 1 {
			return edgeAnchors(re.Sub[0], atStart, atEnd)
		}
		return edgeAnchors(re.Sub[0], false, false)
	}
	return true
}

func zeroWidth(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText,
		syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return true
	}
	return false
}

func unanchor(re *syntax.Regexp) bool {
	// every anchor in the parsed expression, wherever it is nested, is replaced
	switch re.Op {
	case syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText:
		re.Op = syntax.OpEmptyMatch
		return true
	}
	var found bool
	for _, sub := range re.Sub {
		if unanchor(sub) {
			found = true
		}
	}
	return found
}

func grpcFilters(in []gwapiv1.GRPCRouteFilter) []gwapiv1.HTTPRouteFilter {
	if len(in) == 0 {
		return nil
	}
	out := make([]gwapiv1.HTTPRouteFilter, 0, len(in))
	for _, f := range in {
		out = append(out, gwapiv1.HTTPRouteFilter{
			Type:                   gwapiv1.HTTPRouteFilterType(f.Type),
			RequestHeaderModifier:  f.RequestHeaderModifier,
			ResponseHeaderModifier: f.ResponseHeaderModifier,
			RequestMirror:          f.RequestMirror,
			ExtensionRef:           f.ExtensionRef,
		})
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
