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

// Package conformance runs the Gateway API conformance suite against a
// Trickster Gateway API controller reachable through the current kubeconfig.
package conformance

import (
	"os"
	"testing"

	"sigs.k8s.io/gateway-api/conformance"
	confv1 "sigs.k8s.io/gateway-api/conformance/apis/v1"
	"sigs.k8s.io/gateway-api/conformance/utils/suite"
	"sigs.k8s.io/gateway-api/pkg/features"
)

// supportedFeatures is what the controller implements of the GATEWAY-HTTP profile beyond its
// core; the suite runs the tests these unlock and reports the profile's other features as unsupported
var supportedFeatures = []features.FeatureName{
	features.SupportGateway,
	features.SupportHTTPRoute,
	features.SupportReferenceGrant,
	features.SupportGatewayPort8080,
	features.SupportHTTPRouteBackendRequestHeaderModification,
	features.SupportHTTPRouteQueryParamMatching,
	features.SupportHTTPRouteMethodMatching,
	features.SupportHTTPRouteResponseHeaderModification,
	features.SupportHTTPRoutePortRedirect,
	features.SupportHTTPRouteSchemeRedirect,
	features.SupportHTTPRoutePathRedirect,
	features.SupportHTTPRouteHostRewrite,
	features.SupportHTTPRoutePathRewrite,
	features.SupportHTTPRouteRequestMirror,
	features.SupportHTTPRouteRequestPercentageMirror,
	features.SupportHTTPRouteRequestTimeout,
	features.SupportHTTPRouteBackendTimeout,
	features.SupportHTTPRouteParentRefPort,
	features.SupportHTTPRouteDestinationPortMatching,
	features.SupportHTTPRouteNamedRouteRule,
	features.SupportHTTPRouteBackendProtocolWebSocket,
	features.SupportHTTPRouteBackendProtocolH2C,
	features.SupportHTTPRouteRetry,
	features.SupportHTTPRouteRetryBackendTimeout,
	features.SupportHTTPRouteRetryConnectionError,
	features.SupportHTTPRoute303RedirectStatusCode,
	features.SupportHTTPRoute307RedirectStatusCode,
	features.SupportHTTPRoute308RedirectStatusCode,
}

// implementation identifies the report's subject; the -version flag overrides the version
var implementation = confv1.Implementation{
	Organization: "trickstercache",
	Project:      "trickster",
	URL:          "https://github.com/trickstercache/trickster",
	Version:      "main",
	Contact:      []string{"https://github.com/trickstercache/trickster/issues"},
}

// skippedTests are core tests one process serving every Gateway of a class cannot satisfy:
// HTTPRouteMultipleGateways expects two Gateways on one port to answer at distinct addresses
var skippedTests = []string{"HTTPRouteMultipleGateways"}

func TestConformance(t *testing.T) {
	if os.Getenv("TRICKSTER_KIND_TEST") != "1" {
		t.Skip("the conformance suite needs a cluster; runs only with TRICKSTER_KIND_TEST=1")
	}
	opts := conformance.DefaultOptions(t)
	opts.SkipTests = append(opts.SkipTests, skippedTests...)
	if len(opts.SupportedFeatures) == 0 && !opts.EnableAllSupportedFeatures {
		opts.SupportedFeatures = supportedFeatures
	}
	if len(opts.ConformanceProfiles) == 0 {
		opts.ConformanceProfiles = []suite.ConformanceProfileName{suite.GatewayHTTPConformanceProfileName}
	}
	fillImplementation(&opts.Implementation)
	conformance.RunConformanceWithOptions(t, opts)
}

func fillImplementation(in *confv1.Implementation) {
	if in.Organization == "" {
		in.Organization = implementation.Organization
	}
	if in.Project == "" {
		in.Project = implementation.Project
	}
	if in.URL == "" {
		in.URL = implementation.URL
	}
	if in.Version == "" {
		in.Version = implementation.Version
	}
	if len(in.Contact) == 0 {
		in.Contact = implementation.Contact
	}
}
