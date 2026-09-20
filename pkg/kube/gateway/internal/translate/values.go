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

package translate

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	albnames "github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	corso "github.com/trickstercache/trickster/v2/pkg/proxy/cors/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/forwarding"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"golang.org/x/net/http/httpguts"
)

// RoutingMode parses a routing mode value
func RoutingMode(v string) (string, error) {
	if v != kubecfg.RoutingModeService && v != kubecfg.RoutingModeEndpoint {
		return "", fmt.Errorf("must be %q or %q", kubecfg.RoutingModeService,
			kubecfg.RoutingModeEndpoint)
	}
	return v, nil
}

// HealthMode parses the health mode of generated discovery-backed ALBs
func HealthMode(v string) (string, error) {
	if v != ao.HealthModeProbe && v != ao.HealthModeProvider {
		return "", fmt.Errorf("must be %q or %q", ao.HealthModeProbe, ao.HealthModeProvider)
	}
	return v, nil
}

// LoadBalancing parses the mechanism that balances a Service's endpoints: one that commits
// each request or connection to a single member, by its short name
func LoadBalancing(v string) (string, error) {
	if slices.Contains(loadBalancingMechanisms, v) {
		return v, nil
	}
	return "", fmt.Errorf("must be one of %s", strings.Join(loadBalancingMechanisms, ", "))
}

var loadBalancingMechanisms = []string{
	albnames.MechanismRR, albnames.MechanismP2C, albnames.MechanismLC, albnames.MechanismLT, albnames.MechanismHRW,
}

// LoadBalancingKey parses what the hrw mechanism keeps together
func LoadBalancingKey(v string) (string, error) {
	if _, err := ao.ParseKeySource(v); err != nil {
		return "", err
	}
	return strings.TrimSpace(v), nil
}

// Handler parses a path handler name
func Handler(v string) (string, error) {
	switch v {
	case ir.HandlerProxy, ir.HandlerProxyCache:
		return v, nil
	}
	return "", fmt.Errorf("must be %q or %q", ir.HandlerProxy, ir.HandlerProxyCache)
}

// CORSMode parses a CORS policy mode
func CORSMode(v string) (string, error) {
	switch corso.Mode(v) {
	case corso.ModePreserve, corso.ModeMerge, corso.ModeReplace, corso.ModeDisable:
		return v, nil
	}
	return "", fmt.Errorf("must be one of %q, %q, %q, %q", corso.ModePreserve,
		corso.ModeMerge, corso.ModeReplace, corso.ModeDisable)
}

// CollapsedForwarding parses a collapsed forwarding type name
func CollapsedForwarding(v string) (string, error) {
	if _, ok := forwarding.CollapsedForwardingTypeNames[v]; !ok {
		return "", fmt.Errorf("must be %q or %q", forwarding.CFNameBasic,
			forwarding.CFNameProgressive)
	}
	return v, nil
}

// Provider parses a time series provider name: one reached over HTTP, whose API paths the
// proxy accelerates rather than caches as opaque objects
func Provider(v string) (string, error) {
	if !providers.IsSupportedHTTPTimeSeriesProvider(v) {
		return "", fmt.Errorf("must be a time series provider: %s",
			strings.Join(providers.HTTPTimeSeriesProviderNames(), ", "))
	}
	return v, nil
}

// ResultHeader parses a result header disposition, spelled Expose or Hide in any case
func ResultHeader(v string) (string, error) {
	switch strings.ToLower(v) {
	case ir.ResultHeaderExpose:
		return ir.ResultHeaderExpose, nil
	case ir.ResultHeaderHide:
		return ir.ResultHeaderHide, nil
	}
	return "", fmt.Errorf("must be %q or %q", "Expose", "Hide")
}

// Headers reads header updates, one "Name: value" per line; a name may carry a leading '-' to
// delete the header or '+' to append to it, as the proxy's header updates and CORS policy understand
func Headers(value string) (map[string]string, error) {
	out := make(map[string]string)
	for line := range strings.SplitSeq(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, v, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("each line must be 'Name: value' (got %q)", line)
		}
		if err := putHeader(out, strings.TrimSpace(key), strings.TrimSpace(v)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// HeaderMap validates header updates already keyed by name, under the same '-' and '+' spelling;
// a map may name a header once, since it is applied in no particular order
func HeaderMap(in map[string]string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	seen := make(map[string]string, len(in))
	for _, raw := range slices.Sorted(maps.Keys(in)) {
		key := strings.TrimSpace(raw)
		if err := putHeader(out, key, strings.TrimSpace(in[raw])); err != nil {
			return nil, err
		}
		_, name := headers.ParseUpdateKey(key)
		lower := strings.ToLower(name)
		if other, dup := seen[lower]; dup {
			return nil, fmt.Errorf("header %q is named more than once (%q and %q)", name, other, key)
		}
		seen[lower] = key
	}
	return out, nil
}

func putHeader(out map[string]string, key, v string) error {
	op, name := headers.ParseUpdateKey(key)
	if err := headers.ValidUpdate(op, name, v); err != nil {
		if errors.Is(err, headers.ErrInvalidHeaderValue) {
			return fmt.Errorf("%q is not a valid value for header %q", v, name)
		}
		return fmt.Errorf("%q is not a valid header name", key)
	}
	out[key] = v
	return nil
}

// HeaderNames validates a list of header names, as a cache key's header components are
func HeaderNames(in []string) ([]string, error) {
	return names(in, func(v string) error {
		if !httpguts.ValidHeaderFieldName(v) {
			return fmt.Errorf("%q is not a valid header name", v)
		}
		return nil
	})
}

// ParamNames validates a list of query parameter names, which may not be empty or hold whitespace
func ParamNames(in []string) ([]string, error) {
	return names(in, func(v string) error {
		if strings.ContainsAny(v, " \t\r\n") {
			return fmt.Errorf("%q is not a valid parameter name", v)
		}
		return nil
	})
}

func names(in []string, check func(string) error) ([]string, error) {
	// an omitted list is nil and inherits; an explicitly empty one is kept empty and clears
	if in == nil {
		return nil, nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, errors.New("a name is empty")
		}
		if err := check(v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
