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

// Flavors name the Prometheus-compatible products whose documented routes, limits and defaults a
// prometheus backend can apply
const (
	FlavorCloudWatch = "cloudwatch"
	FlavorAMP        = "amp"
)

const (
	// CloudWatchSigningService is the SigV4 service of the CloudWatch PromQL API
	CloudWatchSigningService = "monitoring"
	// CloudWatchMaxSeries is the most series a CloudWatch PromQL query returns before truncating
	CloudWatchMaxSeries = 500
	// CloudWatchVolatileWindow is refetched on each request so late OTLP and vended data is picked up
	CloudWatchVolatileWindow = 2 * time.Minute

	cloudWatchHostPrefix = "monitoring."
	ampHostPrefix        = "aps-workspaces."
	awsHostSuffix        = ".amazonaws.com"
)

// ErrUnknownFlavor is returned for a flavor that is not one of the Flavor constants
var ErrUnknownFlavor = errors.New("unknown prometheus flavor")

// Flavors returns the supported flavor names.
func Flavors() []string {
	return []string{FlavorCloudWatch, FlavorAMP}
}

// Validate checks the options that can be checked without the enclosing backend.
func (o *Options) Validate() error {
	if o == nil || o.Flavor == "" {
		return nil
	}
	switch o.Flavor {
	case FlavorCloudWatch, FlavorAMP:
		return nil
	}
	return fmt.Errorf("%w %q; supported flavors are %s", ErrUnknownFlavor, o.Flavor,
		strings.Join(Flavors(), ", "))
}

// CloudWatchOriginURL returns the CloudWatch PromQL API base URL for region.
func CloudWatchOriginURL(region string) string {
	return "https://" + cloudWatchHostPrefix + region + awsHostSuffix
}

// OriginRegion returns the AWS region named by a flavor's regional origin host, or "" when the
// host is not the flavor's AWS endpoint.
func OriginRegion(flavor, originURL string) string {
	var prefix string
	switch flavor {
	case FlavorCloudWatch:
		prefix = cloudWatchHostPrefix
	case FlavorAMP:
		prefix = ampHostPrefix
	default:
		return ""
	}
	u, err := url.Parse(originURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasPrefix(host, prefix) || !strings.HasSuffix(host, awsHostSuffix) {
		return ""
	}
	region := strings.TrimSuffix(strings.TrimPrefix(host, prefix), awsHostSuffix)
	if region == "" || strings.Contains(region, ".") {
		return ""
	}
	return region
}
