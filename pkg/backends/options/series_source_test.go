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
	"errors"
	"testing"

	alberr "github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	tsmoptions "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"

	"github.com/stretchr/testify/require"
)

const (
	seriesTestOrigin    = "http://10.0.0.1:9009/prometheus"
	seriesTestStoreAPI  = prop.StoreAPIScheme + "://thanos:10901"
	seriesTestRead      = "http://mimir:8080/prometheus/api/v1/read"
	seriesTestALB       = "alb1"
	seriesTestTemplate  = "t1"
	seriesTestBadAPI    = "remoteread"
	seriesTestBadFlavor = "Mimir"
)

func newSeriesTestBackend(provider string, template bool, po *prop.Options) *Options {
	o := New()
	o.Name = seriesTestTemplate
	o.Provider = provider
	o.IsTemplate = template
	o.Prometheus = po
	if !template {
		o.OriginURL = seriesTestOrigin
	}
	return o
}

func TestValidateSeriesSource(t *testing.T) {
	mimir := func() *prop.Options { return &prop.Options{Flavor: prop.FlavorMimir} }
	for name, tc := range map[string]struct {
		o       *Options
		wantErr error
	}{
		"nil options":                  {nil, nil},
		"influxdb":                     {newSeriesTestBackend(providers.InfluxDB, false, nil), nil},
		"clickhouse template":          {newSeriesTestBackend(providers.ClickHouse, true, nil), nil},
		"greptimedb template":          {newSeriesTestBackend(providers.GreptimeDB, true, nil), nil},
		"prometheus without options":   {newSeriesTestBackend(providers.Prometheus, false, nil), prop.ErrSeriesEndpoint},
		"prometheus with no api":       {newSeriesTestBackend(providers.Prometheus, false, &prop.Options{}), prop.ErrSeriesEndpoint},
		"template with no api":         {newSeriesTestBackend(providers.Prometheus, true, &prop.Options{}), prop.ErrSeriesEndpoint},
		"cloudwatch template":          {newSeriesTestBackend(providers.Prometheus, true, &prop.Options{Flavor: prop.FlavorCloudWatch}), prop.ErrSeriesEndpoint},
		"thanos without an endpoint":   {newSeriesTestBackend(providers.Prometheus, false, &prop.Options{Flavor: prop.FlavorThanos}), prop.ErrSeriesEndpoint},
		"thanos template, no endpoint": {newSeriesTestBackend(providers.Prometheus, true, &prop.Options{Flavor: prop.FlavorThanos}), prop.ErrSeriesEndpoint},
		"remote read, bad endpoint template": {newSeriesTestBackend(providers.Prometheus, true,
			&prop.Options{SeriesAPI: prop.SeriesAPIRemoteRead, SeriesEndpoint: "ftp://mimir/read"}), prop.ErrSeriesEndpoint},
		"misspelled series api template": {newSeriesTestBackend(providers.Prometheus, true,
			&prop.Options{SeriesAPI: seriesTestBadAPI}), prop.ErrUnknownSeriesAPI},
		"miscased storeapi": {newSeriesTestBackend(providers.Prometheus, false,
			&prop.Options{SeriesAPI: "storeAPI", SeriesEndpoint: seriesTestStoreAPI}), prop.ErrUnknownSeriesAPI},
		"miscased flavor template": {newSeriesTestBackend(providers.Prometheus, true,
			&prop.Options{Flavor: seriesTestBadFlavor}), prop.ErrUnknownFlavor},
		"cloudwatch remote read template": {newSeriesTestBackend(providers.Prometheus, true,
			&prop.Options{Flavor: prop.FlavorCloudWatch, SeriesAPI: prop.SeriesAPIRemoteRead}), prop.ErrSeriesAPIFlavor},
		"mimir":          {newSeriesTestBackend(providers.Prometheus, false, mimir()), nil},
		"mimir template": {newSeriesTestBackend(providers.Prometheus, true, mimir()), nil},
		"thanos": {newSeriesTestBackend(providers.Prometheus, false,
			&prop.Options{Flavor: prop.FlavorThanos, SeriesEndpoint: seriesTestStoreAPI}), nil},
		"thanos template": {newSeriesTestBackend(providers.Prometheus, true,
			&prop.Options{Flavor: prop.FlavorThanos, SeriesEndpoint: seriesTestStoreAPI}), nil},
		"remote read template": {newSeriesTestBackend(providers.Prometheus, true,
			&prop.Options{SeriesAPI: prop.SeriesAPIRemoteRead}), nil},
		"remote read endpoint": {newSeriesTestBackend(providers.Prometheus, false,
			&prop.Options{SeriesAPI: prop.SeriesAPIRemoteRead, SeriesEndpoint: seriesTestRead}), nil},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.o.ValidateSeriesSource()
			switch {
			case tc.wantErr != nil:
				require.ErrorIs(t, err, tc.wantErr)
			case tc.o == nil || tc.o.Provider != providers.Prometheus:
				require.ErrorContains(t, err, "is not a prometheus backend")
			default:
				require.NoError(t, err)
			}
		})
	}

	// a member's remote-read endpoint derives from its own origin, which must be an absolute http(s) URL
	for _, origin := range []string{"tcp://10.0.0.1:9009", "http:///prometheus", "10.0.0.1:9009"} {
		o := newSeriesTestBackend(providers.Prometheus, false, mimir())
		o.OriginURL = origin
		require.ErrorIs(t, o.ValidateSeriesSource(), prop.ErrSeriesEndpoint, origin)
	}
}

func TestNewErrGlobalEvaluationMember(t *testing.T) {
	cause := errors.New("cause")
	for _, tc := range []struct {
		template, member, want string
	}{
		{"", "m1", `invalid tsm global evaluation member: alb "alb1" member "m1": cause`},
		{"t1", "", `invalid tsm global evaluation member: alb "alb1" template "t1": cause`},
		{"t1", "alb1-m1", `invalid tsm global evaluation member: alb "alb1" template "t1" member "alb1-m1": cause`},
	} {
		err := NewErrGlobalEvaluationMember(seriesTestALB, tc.template, tc.member, cause)
		require.EqualError(t, err, tc.want)
		require.ErrorIs(t, err, alberr.ErrGlobalEvaluationMember)
		require.ErrorIs(t, err, cause)
	}
}

func TestValidateDiscoveryGlobalEvaluation(t *testing.T) {
	const (
		merge  = tsmoptions.EvaluationMerge
		global = tsmoptions.EvaluationGlobal
	)
	thanos := &prop.Options{Flavor: prop.FlavorThanos, SeriesEndpoint: seriesTestStoreAPI}
	for name, tc := range map[string]struct {
		mechanism, evaluation, provider string
		po                              *prop.Options
		wantGlobal, wantTSM             bool
	}{
		"influxdb on rr":                   {names.MechanismRR, "", providers.InfluxDB, nil, false, false},
		"influxdb with a residual global":  {names.MechanismRR, global, providers.InfluxDB, nil, false, false},
		"influxdb on merge":                {names.MechanismTSM, merge, providers.InfluxDB, nil, false, true},
		"influxdb on global":               {names.MechanismTSM, global, providers.InfluxDB, nil, true, false},
		"clickhouse on merge":              {names.MechanismTSM, "", providers.ClickHouse, nil, false, true},
		"clickhouse on global":             {names.MechanismTSM, global, providers.ClickHouse, nil, true, false},
		"rp on global":                     {names.MechanismTSM, global, providers.ReverseProxyShort, nil, true, false},
		"greptimedb on merge":              {names.MechanismTSM, merge, providers.GreptimeDB, nil, false, false},
		"greptimedb on global":             {names.MechanismTSM, global, providers.GreptimeDB, nil, true, false},
		"prometheus without api on merge":  {names.MechanismTSM, merge, providers.Prometheus, nil, false, false},
		"prometheus without api on global": {names.MechanismTSM, global, providers.Prometheus, nil, true, false},
		"thanos without endpoint":          {names.MechanismTSM, global, providers.Prometheus, &prop.Options{Flavor: prop.FlavorThanos}, true, false},
		"thanos":                           {names.MechanismTSM, global, providers.Prometheus, thanos, false, false},
		"mimir":                            {names.MechanismTSM, global, providers.Prometheus, &prop.Options{Flavor: prop.FlavorMimir}, false, false},
		"remote read": {
			names.MechanismTSM, global, providers.Prometheus,
			&prop.Options{SeriesAPI: prop.SeriesAPIRemoteRead}, false, false,
		},
		"remote read endpoint": {
			names.MechanismTSM, global, providers.Prometheus,
			&prop.Options{SeriesAPI: prop.SeriesAPIRemoteRead, SeriesEndpoint: seriesTestRead}, false, false,
		},
		"malformed remote read endpoint": {
			names.MechanismTSM, global, providers.Prometheus,
			&prop.Options{SeriesAPI: prop.SeriesAPIRemoteRead, SeriesEndpoint: "mimir:8080"}, true, false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			lb := newTestDiscoveryALB(seriesTestALB)
			lb.ALBOptions.MechanismName = tc.mechanism
			lb.ALBOptions.TSMOptions.Evaluation = tc.evaluation
			l := Lookup{seriesTestTemplate: newSeriesTestBackend(tc.provider, true, tc.po), seriesTestALB: lb}
			err := l.ValidateDiscovery(testDiscoverers())
			var tsmErr *ErrInvalidTemplateTSMProvider
			switch {
			case tc.wantGlobal:
				require.ErrorIs(t, err, alberr.ErrGlobalEvaluationMember)
				require.ErrorContains(t, err, `alb "alb1" template "t1": `)
			case tc.wantTSM:
				require.ErrorAs(t, err, &tsmErr)
			default:
				require.NoError(t, err)
			}
		})
	}
}

func newGlobalDiscoveryLookup(mechanism, evaluation, scheme string, po *prop.Options) Lookup {
	lb := newTestDiscoveryALB(seriesTestALB)
	lb.ALBOptions.MechanismName = mechanism
	lb.ALBOptions.TSMOptions.Evaluation = evaluation
	lb.ALBOptions.Discovery.Query.Scheme = scheme
	return Lookup{seriesTestTemplate: newSeriesTestBackend(providers.Prometheus, true, po), seriesTestALB: lb}
}

func TestValidateDiscoveryGlobalMemberScheme(t *testing.T) {
	mimir := &prop.Options{Flavor: prop.FlavorMimir}
	thanos := &prop.Options{Flavor: prop.FlavorThanos, SeriesEndpoint: seriesTestStoreAPI}
	readEndpoint := &prop.Options{SeriesAPI: prop.SeriesAPIRemoteRead, SeriesEndpoint: seriesTestRead}
	for name, tc := range map[string]struct {
		mechanism, evaluation, scheme string
		po                            *prop.Options
		wantErr                       bool
	}{
		"mimir, no scheme":             {names.MechanismTSM, tsmoptions.EvaluationGlobal, "", mimir, false},
		"mimir over http":              {names.MechanismTSM, tsmoptions.EvaluationGlobal, do.SchemeHTTP, mimir, false},
		"mimir over https":             {names.MechanismTSM, tsmoptions.EvaluationGlobal, do.SchemeHTTPS, mimir, false},
		"mimir over tcp":               {names.MechanismTSM, tsmoptions.EvaluationGlobal, do.SchemeTCP, mimir, true},
		"mimir over udp":               {names.MechanismTSM, tsmoptions.EvaluationGlobal, do.SchemeUDP, mimir, true},
		"mimir over tcp, on merge":     {names.MechanismTSM, tsmoptions.EvaluationMerge, do.SchemeTCP, mimir, false},
		"mimir over tcp, residual":     {names.MechanismRR, tsmoptions.EvaluationGlobal, do.SchemeTCP, mimir, false},
		"thanos over tcp":              {names.MechanismTSM, tsmoptions.EvaluationGlobal, do.SchemeTCP, thanos, false},
		"remote read endpoint, on tcp": {names.MechanismTSM, tsmoptions.EvaluationGlobal, do.SchemeTCP, readEndpoint, false},
	} {
		t.Run(name, func(t *testing.T) {
			err := newGlobalDiscoveryLookup(tc.mechanism, tc.evaluation, tc.scheme, tc.po).
				ValidateDiscovery(testDiscoverers())
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, alberr.ErrGlobalEvaluationMember)
			require.ErrorIs(t, err, prop.ErrSeriesEndpoint)
			require.ErrorContains(t, err, `alb "alb1" template "t1": `)
			require.ErrorContains(t, err, `not "`+tc.scheme+`"`)
		})
	}
}

func TestValidateDiscoveryGlobalTemplateTypos(t *testing.T) {
	for name, tc := range map[string]struct {
		po      *prop.Options
		wantErr error
	}{
		"misspelled series api": {&prop.Options{SeriesAPI: seriesTestBadAPI}, prop.ErrUnknownSeriesAPI},
		"miscased storeapi": {
			&prop.Options{SeriesAPI: "storeAPI", SeriesEndpoint: seriesTestStoreAPI},
			prop.ErrUnknownSeriesAPI,
		},
		"miscased flavor": {&prop.Options{Flavor: seriesTestBadFlavor}, prop.ErrUnknownFlavor},
	} {
		t.Run(name, func(t *testing.T) {
			err := newGlobalDiscoveryLookup(names.MechanismTSM, tsmoptions.EvaluationGlobal, "", tc.po).
				ValidateDiscovery(testDiscoverers())
			require.ErrorIs(t, err, tc.wantErr)
			require.ErrorIs(t, err, alberr.ErrGlobalEvaluationMember)
			require.ErrorContains(t, err, `alb "alb1" template "t1": `)
			require.NotErrorIs(t, err, prop.ErrSeriesEndpoint)
		})
	}
}
