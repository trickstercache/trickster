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

package gateway

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/internal/translate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"

	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Reasons a filter cannot be honored
var (
	errFilterType          = errors.New("filter type is not supported")
	errFilterRepeated      = errors.New("filter type may not be repeated")
	errFilterBody          = errors.New("filter carries no configuration for its type")
	errRedirectAndRewrite  = errors.New("requestRedirect and urlRewrite cannot both be used")
	errRedirectOnBackend   = errors.New("requestRedirect is not permitted on a backendRef")
	errPrefixMatchNeeded   = errors.New("replacePrefixMatch requires exactly one PathPrefix match")
	errPrefixOnMembers     = errors.New("replacePrefixMatch on a backendRef requires a rule with one backendRef")
	errPathRewrittenTwice  = errors.New("the rule and its backendRef both rewrite the path")
	errBadScheme           = errors.New("scheme must be http or https")
	errBadPort             = errors.New("port must be between 1 and 65535")
	errBadStatusCode       = errors.New("statusCode must be 301, 302, 303, 307 or 308")
	errBadPathModifier     = errors.New("unsupported path modifier type")
	errRelativeReplacement = errors.New("path replacement must begin with '/'")
	errHeaderName          = errors.New("not a valid header name")
	errHeaderValue         = errors.New("not a valid header value")
	errHeaderRepeated      = errors.New("a header may be named once per filter, across set, add and remove")
	errLocationOnRedirect  = errors.New("a response header filter may not modify Location on a redirecting rule")
	errMirrorShare         = errors.New("requestMirror copies no request; percent or fraction must be at least 1%")
	errBadDuration         = errors.New("not a valid duration")
	errBackendTimeout      = errors.New("timeouts.backendRequest must not exceed timeouts.request")
	errRetryAttempts       = fmt.Errorf("retry.attempts must be between 1 and %d", po.MaxRetryAttempts)
	errRetryCode           = errors.New("retry.codes must be valid HTTP status codes")
)

// headerLocation is the response header a redirection is defined by
const headerLocation = "Location"

// ruleFilters is one rule's filters and each of its backendRefs' filters,
// lowered into the IR, with the rule's timeouts and retry policy
type ruleFilters struct {
	rule     []ir.Filter
	refs     [][]ir.Filter
	timeouts *ir.RuleTimeouts
	retry    *ir.RuleRetry
}

// mirrorResolver resolves a mirror filter's backendRef into the Service that receives the
// copies, or explains why it cannot
type mirrorResolver func(ref gwapiv1.BackendObjectReference) (*ir.MirrorFilter, string,
	gwapiv1.RouteConditionReason)

func (rf ruleFilters) redirects() bool {
	return ir.Rule{Filters: rf.rule}.Redirect() != nil
}

func (t *translator) lowerRuleFilters(src ir.Source, hr *gwapiv1.HTTPRoute,
	report *routeReport,
) ([]ruleFilters, bool) {
	// a filter changes what a route means, so one that cannot be honored fails the whole route
	// rather than serving it partially; a mirror whose backendRef does not resolve is the one
	// exception the API defines, dropped with ResolvedRefs lowered, and unimplemented additive
	// rule settings are only reported
	out := make([]ruleFilters, len(hr.Spec.Rules))
	ok := true
	mirrors := t.mirrorResolver(src, hr.Namespace, report)
	for i, rule := range hr.Spec.Rules {
		rf, err := lowerFilters(rule, mirrors)
		if err != nil {
			t.reject(src, "rule %d: %s; the route is not served", i, err)
			ok = false
		}
		if j := requestHeadersAfterRedirect(rf.rule); j >= 0 {
			// filters apply in order and a redirect ends the request, so a request header
			// modifier after it is dropped rather than let alter the Location
			t.reject(src, "rule %d: filter %d (RequestHeaderModifier) follows the "+
				"RequestRedirect and has no effect", i, j)
			rf.rule = slices.Delete(rf.rule, j, j+1)
		}
		rf.rule = dropUnresolvedMirrors(rf.rule)
		for j := range rf.refs {
			rf.refs[j] = dropUnresolvedMirrors(rf.refs[j])
		}
		if rf.timeouts, err = lowerTimeouts(rule.Timeouts); err != nil {
			t.reject(src, "rule %d: %s; the route is not served", i, err)
			ok = false
		}
		if rf.retry, err = lowerRetry(rule.Retry); err != nil {
			t.reject(src, "rule %d: %s; the route is not served", i, err)
			ok = false
		}
		out[i] = rf
		if rule.SessionPersistence != nil {
			t.reject(src, "rule %d: sessionPersistence is not supported and is ignored", i)
		}
	}
	return out, ok
}

func (t *translator) mirrorResolver(src ir.Source, namespace string,
	report *routeReport,
) mirrorResolver {
	// a mirror's backendRef resolves as a forwarding reference does, and a failure lowers the
	// route's ResolvedRefs
	return func(ref gwapiv1.BackendObjectReference) (*ir.MirrorFilter, string,
		gwapiv1.RouteConditionReason,
	) {
		target, tls, reason, why := t.resolveBackendRef(src.Kind, namespace, ref)
		if reason != "" {
			t.reject(src, "requestMirror: %s; the mirror is not applied", reason)
			report.unresolved(why, "requestMirror: "+reason)
			return nil, reason, why
		}
		return &ir.MirrorFilter{Service: target, TLS: tls}, "", ""
	}
}

func dropUnresolvedMirrors(filters []ir.Filter) []ir.Filter {
	// a mirror whose Service did not resolve is kept through lowering, so the list's own checks
	// still see it, and dropped here
	return slices.DeleteFunc(filters, func(f ir.Filter) bool {
		return f.Type == ir.FilterMirror && f.Mirror == nil
	})
}

func lowerTimeouts(in *gwapiv1.HTTPRouteTimeouts) (*ir.RuleTimeouts, error) {
	if in == nil {
		return nil, nil
	}
	out := &ir.RuleTimeouts{}
	var err error
	if out.RequestMS, err = lowerDuration("timeouts.request", in.Request); err != nil {
		return nil, err
	}
	if out.BackendRequestMS, err = lowerDuration("timeouts.backendRequest",
		in.BackendRequest); err != nil {
		return nil, err
	}
	if out.RequestMS > 0 && out.BackendRequestMS > out.RequestMS {
		return nil, errBackendTimeout
	}
	if out.RequestMS == 0 && out.BackendRequestMS == 0 {
		return nil, nil
	}
	return out, nil
}

func lowerRetry(in *gwapiv1.HTTPRouteRetry) (*ir.RuleRetry, error) {
	// an unstated attempt count is one retry, which is what the API's example asks for
	if in == nil {
		return nil, nil
	}
	out := &ir.RuleRetry{Attempts: 1}
	if in.Attempts != nil {
		out.Attempts = *in.Attempts
	}
	if out.Attempts < 1 || out.Attempts > po.MaxRetryAttempts {
		return nil, fmt.Errorf("%w (got %d)", errRetryAttempts, out.Attempts)
	}
	for _, c := range in.Codes {
		if c < 100 || c > 599 {
			return nil, fmt.Errorf("%w (got %d)", errRetryCode, c)
		}
		out.Codes = append(out.Codes, int(c))
	}
	var err error
	if out.BackoffMS, err = lowerDuration("retry.backoff", in.Backoff); err != nil {
		return nil, err
	}
	return out, nil
}

func lowerDuration(field string, d *gwapiv1.Duration) (int64, error) {
	// a Gateway API duration in milliseconds; nil or zero is zero
	if d == nil || *d == "" {
		return 0, nil
	}
	v, err := time.ParseDuration(string(*d))
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%s: %w: %q", field, errBadDuration, *d)
	}
	return v.Milliseconds(), nil
}

func lowerFilters(rule gwapiv1.HTTPRouteRule, mirrors mirrorResolver) (ruleFilters, error) {
	// a prefix replacement needs the one prefix it replaces, and a backendRef's cannot be applied
	// behind a weighted dispatch that no longer knows which match was taken
	prefixMatch := singlePrefixMatch(rule.Matches)
	var members int
	for _, ref := range rule.BackendRefs {
		if ref.Weight == nil || *ref.Weight > 0 {
			members++
		}
	}
	var rf ruleFilters
	var err error
	if rf.rule, err = lowerFilterList(rule.Filters, prefixMatch, true, false, mirrors); err != nil {
		return rf, err
	}
	rulePath := rewritesPath(rf.rule)
	rf.refs = make([][]ir.Filter, len(rule.BackendRefs))
	for j, ref := range rule.BackendRefs {
		f, err := lowerFilterList(ref.Filters, prefixMatch, members == 1, true, mirrors)
		if err != nil {
			return rf, fmt.Errorf("backendRef %d: %w", j, err)
		}
		if rulePath && rewritesPath(f) {
			return rf, fmt.Errorf("backendRef %d: %w", j, errPathRewrittenTwice)
		}
		rf.refs[j] = f
	}
	return rf, nil
}

func lowerFilterList(filters []gwapiv1.HTTPRouteFilter, prefixMatch, single, backend bool,
	mirrors mirrorResolver,
) ([]ir.Filter, error) {
	// a filter type appears at most once and a redirect excludes a rewrite, since
	// both decide where a request goes
	if len(filters) == 0 {
		return nil, nil
	}
	seen := make(map[gwapiv1.HTTPRouteFilterType]struct{}, len(filters))
	out := make([]ir.Filter, 0, len(filters))
	var redirect, rewrite bool
	for i, f := range filters {
		if _, dup := seen[f.Type]; dup {
			return nil, fmt.Errorf("filter %d: %w: %s", i, errFilterRepeated, f.Type)
		}
		seen[f.Type] = struct{}{}
		lowered, err := lowerFilter(f, prefixMatch, single, backend, mirrors)
		if err != nil {
			return nil, fmt.Errorf("filter %d (%s): %w", i, f.Type, err)
		}
		switch lowered.Type {
		case ir.FilterRedirect:
			redirect = true
		case ir.FilterURLRewrite:
			rewrite = true
		}
		out = append(out, lowered)
	}
	if redirect && rewrite {
		return nil, errRedirectAndRewrite
	}
	if redirect && modifiesLocation(out) {
		// the redirect filter defines the Location; a modifier fighting over it would apply
		// in one order or the other, and either silently discards half of what was declared
		return nil, errLocationOnRedirect
	}
	return out, nil
}

func requestHeadersAfterRedirect(filters []ir.Filter) int {
	var redirected bool
	for i, f := range filters {
		switch f.Type {
		case ir.FilterRedirect:
			redirected = true
		case ir.FilterRequestHeaders:
			if redirected {
				return i
			}
		}
	}
	return -1
}

func modifiesLocation(filters []ir.Filter) bool {
	for _, f := range filters {
		if f.Type != ir.FilterResponseHeaders || f.ResponseHeaders == nil {
			continue
		}
		h := f.ResponseHeaders
		for _, s := range h.Set {
			if strings.EqualFold(s.Name, headerLocation) {
				return true
			}
		}
		for _, a := range h.Add {
			if strings.EqualFold(a.Name, headerLocation) {
				return true
			}
		}
		for _, r := range h.Remove {
			if strings.EqualFold(r, headerLocation) {
				return true
			}
		}
	}
	return false
}

func lowerFilter(f gwapiv1.HTTPRouteFilter, prefixMatch, single, backend bool,
	mirrors mirrorResolver,
) (ir.Filter, error) {
	switch f.Type {
	case gwapiv1.HTTPRouteFilterRequestMirror:
		m, err := lowerMirror(f.RequestMirror, mirrors)
		return ir.Filter{Type: ir.FilterMirror, Mirror: m}, err
	case gwapiv1.HTTPRouteFilterRequestHeaderModifier:
		h, err := lowerHeaderFilter(f.RequestHeaderModifier)
		return ir.Filter{Type: ir.FilterRequestHeaders, RequestHeaders: h}, err
	case gwapiv1.HTTPRouteFilterResponseHeaderModifier:
		h, err := lowerHeaderFilter(f.ResponseHeaderModifier)
		return ir.Filter{Type: ir.FilterResponseHeaders, ResponseHeaders: h}, err
	case gwapiv1.HTTPRouteFilterRequestRedirect:
		if backend {
			return ir.Filter{}, errRedirectOnBackend
		}
		r, err := lowerRedirect(f.RequestRedirect, prefixMatch)
		return ir.Filter{Type: ir.FilterRedirect, Redirect: r}, err
	case gwapiv1.HTTPRouteFilterURLRewrite:
		u, err := lowerURLRewrite(f.URLRewrite, prefixMatch, single || !backend)
		return ir.Filter{Type: ir.FilterURLRewrite, URLRewrite: u}, err
	}
	return ir.Filter{}, errFilterType
}

func lowerMirror(m *gwapiv1.HTTPRequestMirrorFilter, mirrors mirrorResolver) (*ir.MirrorFilter, error) {
	// an unresolvable Service yields a filter with no mirror, dropped once the list is checked
	if m == nil {
		return nil, errFilterBody
	}
	percent := 100
	switch {
	case m.Percent != nil:
		percent = int(*m.Percent)
	case m.Fraction != nil:
		den := int32(100)
		if m.Fraction.Denominator != nil && *m.Fraction.Denominator > 0 {
			den = *m.Fraction.Denominator
		}
		percent = int(math.Round(float64(m.Fraction.Numerator) * 100 / float64(den)))
	}
	if percent < 1 {
		return nil, errMirrorShare
	}
	if percent > 100 {
		percent = 100
	}
	if mirrors == nil {
		return nil, nil
	}
	out, _, _ := mirrors(m.BackendRef)
	if out == nil {
		return nil, nil
	}
	out.Percent = percent
	return out, nil
}

func lowerHeaderFilter(h *gwapiv1.HTTPHeaderFilter) (*ir.HeaderFilter, error) {
	// a header may be named once per filter across all three actions, whatever its case: two
	// actions on one header have no defined order, and the API server catches only exact repeats
	if h == nil {
		return nil, errFilterBody
	}
	out := &ir.HeaderFilter{}
	seen := make(map[string]struct{}, len(h.Set)+len(h.Add)+len(h.Remove))
	once := func(name string) error {
		key := strings.ToLower(name)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("%w: %q", errHeaderRepeated, name)
		}
		seen[key] = struct{}{}
		return nil
	}
	for _, s := range h.Set {
		if err := validHeader(headers.UpdateSet, string(s.Name), s.Value); err != nil {
			return nil, err
		}
		if err := once(string(s.Name)); err != nil {
			return nil, err
		}
		out.Set = append(out.Set, ir.Header{Name: string(s.Name), Value: s.Value})
	}
	for _, a := range h.Add {
		if err := validHeader(headers.UpdateAppend, string(a.Name), a.Value); err != nil {
			return nil, err
		}
		if err := once(string(a.Name)); err != nil {
			return nil, err
		}
		out.Add = append(out.Add, ir.Header{Name: string(a.Name), Value: a.Value})
	}
	for _, r := range h.Remove {
		if err := validHeader(headers.UpdateDelete, r, ""); err != nil {
			return nil, err
		}
		if err := once(r); err != nil {
			return nil, err
		}
		out.Remove = append(out.Remove, r)
	}
	return out, nil
}

func validHeader(op headers.UpdateOp, name, value string) error {
	err := headers.ValidUpdate(op, name, value)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, headers.ErrInvalidHeaderValue):
		return fmt.Errorf("%w for %q: %q", errHeaderValue, name, value)
	}
	return fmt.Errorf("%w: %q", errHeaderName, name)
}

func lowerRedirect(r *gwapiv1.HTTPRequestRedirectFilter, prefixMatch bool) (*ir.RedirectFilter, error) {
	// the redirection status is 301 or 302, and a hostname must be precise since
	// it becomes the Location
	if r == nil {
		return nil, errFilterBody
	}
	out := &ir.RedirectFilter{}
	if r.Scheme != nil && *r.Scheme != "" {
		if *r.Scheme != "http" && *r.Scheme != "https" {
			return nil, fmt.Errorf("%w (got %q)", errBadScheme, *r.Scheme)
		}
		out.Scheme = *r.Scheme
	}
	if r.Hostname != nil && *r.Hostname != "" {
		h, err := translate.Hostname(string(*r.Hostname), translate.HostnamePrecise)
		if err != nil {
			return nil, err
		}
		out.Hostname = h
	}
	if r.Port != nil {
		if *r.Port < 1 || *r.Port > 65535 {
			return nil, fmt.Errorf("%w (got %d)", errBadPort, *r.Port)
		}
		out.Port = int(*r.Port)
	}
	if r.Path != nil {
		pm, err := lowerPathModifier(r.Path, prefixMatch, true)
		if err != nil {
			return nil, err
		}
		out.Path = pm
	}
	if r.StatusCode != nil {
		switch *r.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
			http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			out.StatusCode = *r.StatusCode
		default:
			return nil, fmt.Errorf("%w (got %d)", errBadStatusCode, *r.StatusCode)
		}
	}
	return out, nil
}

func lowerURLRewrite(u *gwapiv1.HTTPURLRewriteFilter, prefixMatch, prefixOK bool) (*ir.URLRewriteFilter, error) {
	if u == nil {
		return nil, errFilterBody
	}
	out := &ir.URLRewriteFilter{}
	if u.Hostname != nil && *u.Hostname != "" {
		h, err := translate.Hostname(string(*u.Hostname), translate.HostnamePrecise)
		if err != nil {
			return nil, err
		}
		out.Hostname = h
	}
	if u.Path != nil {
		pm, err := lowerPathModifier(u.Path, prefixMatch, prefixOK)
		if err != nil {
			return nil, err
		}
		out.Path = pm
	}
	return out, nil
}

func lowerPathModifier(pm *gwapiv1.HTTPPathModifier, prefixMatch, prefixOK bool) (*ir.PathModifier, error) {
	// a prefix replacement is only meaningful where exactly one prefix was matched, and only
	// where the backend applying it still knows which match that was
	switch pm.Type {
	case gwapiv1.FullPathHTTPPathModifier:
		if pm.ReplaceFullPath == nil || !strings.HasPrefix(*pm.ReplaceFullPath, "/") {
			return nil, errRelativeReplacement
		}
		return &ir.PathModifier{Type: ir.PathReplaceFull, Value: *pm.ReplaceFullPath}, nil
	case gwapiv1.PrefixMatchHTTPPathModifier:
		if !prefixMatch {
			return nil, errPrefixMatchNeeded
		}
		if !prefixOK {
			return nil, errPrefixOnMembers
		}
		var value string
		if pm.ReplacePrefixMatch != nil {
			value = *pm.ReplacePrefixMatch
		}
		if value != "" && !strings.HasPrefix(value, "/") {
			return nil, errRelativeReplacement
		}
		return &ir.PathModifier{Type: ir.PathReplacePrefix, Value: value}, nil
	}
	return nil, fmt.Errorf("%w: %q", errBadPathModifier, pm.Type)
}

func singlePrefixMatch(matches []gwapiv1.HTTPRouteMatch) bool {
	// a rule with no match stands for a prefix match on the root
	if len(matches) == 0 {
		return true
	}
	if len(matches) != 1 {
		return false
	}
	p := matches[0].Path
	return p == nil || p.Type == nil || *p.Type == gwapiv1.PathMatchPathPrefix
}

func rewritesPath(filters []ir.Filter) bool {
	for _, f := range filters {
		if f.URLRewrite != nil && f.URLRewrite.Path != nil {
			return true
		}
		if f.Redirect != nil && f.Redirect.Path != nil {
			return true
		}
	}
	return false
}
