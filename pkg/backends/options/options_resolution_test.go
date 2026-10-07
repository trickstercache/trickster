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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	reso "github.com/trickstercache/trickster/v2/pkg/proxy/resolution/options"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const (
	resolutionTestBackend = "origins"
	resolutionTestOrigin  = "http://origins.example.com"
	noOrigin              = "-"
)

func TestOriginResolutionYAML(t *testing.T) {
	const doc = `
provider: rp
origin_url: ` + resolutionTestOrigin + `
origin_resolution:
  mode: SRV
  resolver: 10.0.0.2:53
  min_ttl: 2s
  negative_ttl: 1s
  tls_server_name: target
`
	o := New()
	require.NoError(t, yaml.Unmarshal([]byte(doc), o))
	require.NoError(t, o.Initialize(resolutionTestBackend))
	_, err := o.Validate()
	require.NoError(t, err)
	r := o.OriginResolution
	require.True(t, r.IsSRV())
	require.True(t, r.VerifiesTarget())
	mn, mx, neg := r.TTLs()
	require.Equal(t, 2*time.Second, mn)
	require.Equal(t, reso.DefaultMaxTTL, mx)
	require.Equal(t, time.Second, neg)

	c := o.Clone()
	require.Equal(t, r, c.OriginResolution)
	require.NotSame(t, r, c.OriginResolution)
}

func TestValidateOriginResolution(t *testing.T) {
	srv := func() *reso.Options { return &reso.Options{Mode: reso.ModeSRV} }
	tests := []struct {
		name     string
		provider string
		origin   string
		protocol string
		sni      string
		res      *reso.Options
		err      error
	}{
		{name: "absent", res: nil},
		{name: "mode a", res: &reso.Options{Mode: reso.ModeA}},
		{name: "srv", res: srv()},
		{name: "srv https", origin: "https://_app._tcp.example.internal", res: srv()},
		{name: "srv ip origin", origin: "http://10.0.0.5:8080", res: srv(), err: ErrSRVWithIPOrigin},
		{name: "srv ipv6 origin", origin: "http://[fd00::5]", res: srv(), err: ErrSRVWithIPOrigin},
		{
			name: "srv tcp origin", origin: "tcp://origins.example.com:9000", res: srv(),
			err: ErrSRVRequiresHTTP,
		},
		{
			name: "bad resolver", res: &reso.Options{Mode: reso.ModeSRV, Resolver: "10.0.0.2"},
			err: reso.ErrInvalidResolver,
		},
		{
			name: "target with server_name", sni: "origin.example.com",
			res: &reso.Options{Mode: reso.ModeSRV, TLSServerName: reso.ServerNameTarget},
			err: ErrSRVTargetWithServerName,
		},
		{name: "owner with server_name", sni: "origin.example.com", res: srv()},
		{name: "rule provider", provider: providers.Rule, origin: noOrigin, res: srv()},
		{name: "native protocol", protocol: "native", res: srv()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			o := New()
			o.Name = resolutionTestBackend
			o.Provider = providers.ReverseProxyShort
			if test.provider != "" {
				o.Provider = test.provider
			}
			o.OriginURL = resolutionTestOrigin
			if test.origin != "" {
				o.OriginURL = test.origin
			}
			if o.OriginURL == noOrigin {
				o.OriginURL = ""
			}
			o.Protocol = test.protocol
			o.TLS.ServerName = test.sni
			o.OriginResolution = test.res
			_, err := o.Validate()
			switch {
			case test.provider != "" || test.protocol != "":
				var target *ErrUnsupportedOption
				require.ErrorAs(t, err, &target)
			case test.err != nil:
				require.ErrorIs(t, err, test.err)
			default:
				require.NoError(t, err)
			}
		})
	}
}
