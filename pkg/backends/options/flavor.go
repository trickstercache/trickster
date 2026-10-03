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
	"fmt"
	"strings"

	taws "github.com/trickstercache/trickster/v2/pkg/aws"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

// flavor returns the backend's prometheus flavor, or "".
func (o *Options) flavor() string {
	if o.Prometheus == nil {
		return ""
	}
	return o.Prometheus.Flavor
}

// applyPrometheusFlavor fills the defaults a flavor implies wherever the backend left them unset.
// It runs before origin_url is parsed, since the cloudwatch flavor can derive it.
func (o *Options) applyPrometheusFlavor() {
	switch o.flavor() {
	case prop.FlavorCloudWatch:
		if o.SigV4 == nil {
			o.SigV4 = &taws.Options{}
		}
		if strings.TrimSpace(o.SigV4.Service) == "" {
			o.SigV4.Service = prop.CloudWatchSigningService
		}
		if o.OriginURL == "" && o.SigV4.Region != "" {
			o.OriginURL = prop.CloudWatchOriginURL(o.SigV4.Region)
		}
		if !o.volatileWindowExplicit && o.VolatileWindow == 0 && o.VolatileWindowPoints == 0 {
			o.VolatileWindow = timeconv.Duration(prop.CloudWatchVolatileWindow)
		}
	case prop.FlavorAMP:
		// the signing service already defaults to AMP's
		if o.SigV4 == nil {
			o.SigV4 = &taws.Options{}
		}
	}
}

// validatePrometheusFlavor checks a flavor against the rest of the backend.
func (o *Options) validatePrometheusFlavor() error {
	if err := o.Prometheus.Validate(); err != nil {
		return fmt.Errorf("backend %s: %w", o.Name, err)
	}
	flavor := o.flavor()
	if flavor == "" {
		return nil
	}
	if o.Provider != providers.Prometheus {
		return fmt.Errorf("%w: backend %s has provider %q", ErrFlavorProvider, o.Name, o.Provider)
	}
	if o.OriginURL == "" && !o.IsTemplate && flavor == prop.FlavorCloudWatch {
		return fmt.Errorf("%w: backend %s", ErrFlavorMissingOrigin, o.Name)
	}
	if o.SigV4 != nil && o.SigV4.Region != "" {
		if r := prop.OriginRegion(flavor, o.OriginURL); r != "" && r != o.SigV4.Region {
			return fmt.Errorf("%w: backend %s has origin region %q and sigv4.region %q",
				ErrFlavorRegionMismatch, o.Name, r, o.SigV4.Region)
		}
	}
	return nil
}
