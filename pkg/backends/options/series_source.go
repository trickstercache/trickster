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

package options

import (
	"fmt"
	"strings"

	alberr "github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
)

// ValidateSeriesSource returns why the backend cannot serve its own raw series to a TSM global evaluation, or nil.
// A template's remote-read endpoint can derive from each discovered member's origin, so only the members check it.
func (o *Options) ValidateSeriesSource() error {
	if o == nil || o.Provider != providers.Prometheus {
		var provider string
		if o != nil {
			provider = o.Provider
		}
		return fmt.Errorf("provider %q is not a %s backend with a series_api", provider, providers.Prometheus)
	}
	// a misspelled flavor or series_api would otherwise read as no series api at all
	if err := o.Prometheus.Validate(); err != nil {
		return err
	}
	if o.IsTemplate && o.seriesEndpointFromOrigin() {
		return nil
	}
	_, err := o.Prometheus.SeriesEndpointURL(o.OriginURL)
	return err
}

func (o *Options) seriesEndpointFromOrigin() bool {
	return o.Prometheus.EffectiveSeriesAPI() == prop.SeriesAPIRemoteRead && o.Prometheus.SeriesEndpoint == ""
}

func (o *Options) validateTemplateSeriesSource(memberScheme string) error {
	if err := o.ValidateSeriesSource(); err != nil {
		return err
	}
	// a discovery query's scheme is every member's origin scheme, from which remote read can derive its endpoint
	switch memberScheme {
	case "", do.SchemeHTTP, do.SchemeHTTPS:
		return nil
	}
	if o.seriesEndpointFromOrigin() {
		return fmt.Errorf("%w: remote read from each member's origin needs scheme %s or %s, not %q",
			prop.ErrSeriesEndpoint, do.SchemeHTTP, do.SchemeHTTPS, memberScheme)
	}
	return nil
}

// NewErrGlobalEvaluationMember wraps why a backend cannot serve raw series to an alb's TSM global evaluation,
// naming the alb and whichever of the discovery template and the member are not empty
func NewErrGlobalEvaluationMember(albName, templateName, memberName string, cause error) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "alb %q", albName)
	if templateName != "" {
		fmt.Fprintf(&sb, " template %q", templateName)
	}
	if memberName != "" {
		fmt.Fprintf(&sb, " member %q", memberName)
	}
	return fmt.Errorf("%w: %s: %w", alberr.ErrGlobalEvaluationMember, sb.String(), cause)
}
