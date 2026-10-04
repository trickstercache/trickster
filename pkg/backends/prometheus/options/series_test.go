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

	"github.com/stretchr/testify/require"
)

const (
	testMimirOrigin    = "http://mimir:8080/prometheus/"
	testRemoteReadURL  = "https://reader.example.com/api/v1/read"
	testStoreAPIPlain  = StoreAPIScheme + "://thanos-query:10901"
	testStoreAPISecure = StoreAPISchemeTLS + "://thanos-query:10901"
)

func TestEffectiveSeriesAPI(t *testing.T) {
	var o *Options
	require.Empty(t, o.EffectiveSeriesAPI())
	require.Empty(t, (&Options{}).EffectiveSeriesAPI())
	require.Equal(t, SeriesAPIRemoteRead, (&Options{Flavor: FlavorMimir}).EffectiveSeriesAPI())
	require.Equal(t, SeriesAPIStoreAPI, (&Options{Flavor: FlavorThanos}).EffectiveSeriesAPI())
	require.Equal(t, SeriesAPIRemoteRead,
		(&Options{Flavor: FlavorThanos, SeriesAPI: SeriesAPIRemoteRead}).EffectiveSeriesAPI())
	require.Equal(t, SeriesAPIRemoteRead, (&Options{SeriesAPI: SeriesAPIRemoteRead}).EffectiveSeriesAPI())
}

func TestSeriesEndpointURL(t *testing.T) {
	u, err := (&Options{Flavor: FlavorMimir}).SeriesEndpointURL(testMimirOrigin + "?x=1")
	require.NoError(t, err)
	require.Equal(t, "http://mimir:8080/prometheus"+RemoteReadPath, u.String())

	u, err = (&Options{Flavor: FlavorMimir, SeriesEndpoint: testRemoteReadURL}).SeriesEndpointURL(testMimirOrigin)
	require.NoError(t, err)
	require.Equal(t, testRemoteReadURL, u.String())

	u, err = (&Options{Flavor: FlavorThanos, SeriesEndpoint: testStoreAPISecure}).SeriesEndpointURL("")
	require.NoError(t, err)
	require.Equal(t, StoreAPISchemeTLS, u.Scheme)

	_, err = (&Options{Flavor: FlavorThanos}).SeriesEndpointURL(testMimirOrigin)
	require.ErrorIs(t, err, ErrSeriesEndpoint)
	_, err = (&Options{}).SeriesEndpointURL(testMimirOrigin)
	require.ErrorIs(t, err, ErrSeriesEndpoint)
	_, err = (&Options{Flavor: FlavorMimir}).SeriesEndpointURL("mimir:8080")
	require.ErrorIs(t, err, ErrSeriesEndpoint)
}

func TestValidateSeries(t *testing.T) {
	tests := []struct {
		name string
		o    Options
		want error
	}{
		{name: "none", o: Options{}},
		{name: "mimir default", o: Options{Flavor: FlavorMimir}},
		{name: "thanos endpoint", o: Options{Flavor: FlavorThanos, SeriesEndpoint: testStoreAPIPlain}},
		{name: "explicit remote read", o: Options{SeriesAPI: SeriesAPIRemoteRead, SeriesEndpoint: testRemoteReadURL}},
		{name: "unknown api", o: Options{SeriesAPI: "export"}, want: ErrUnknownSeriesAPI},
		{name: "cloudwatch", o: Options{Flavor: FlavorCloudWatch, SeriesAPI: SeriesAPIRemoteRead}, want: ErrSeriesAPIFlavor},
		{name: "amp endpoint", o: Options{Flavor: FlavorAMP, SeriesEndpoint: testRemoteReadURL}, want: ErrSeriesAPIFlavor},
		{name: "endpoint without api", o: Options{SeriesEndpoint: testRemoteReadURL}, want: ErrSeriesEndpoint},
		{name: "remote read scheme", o: Options{Flavor: FlavorMimir, SeriesEndpoint: testStoreAPIPlain}, want: ErrSeriesEndpoint},
		{name: "storeapi scheme", o: Options{Flavor: FlavorThanos, SeriesEndpoint: testRemoteReadURL}, want: ErrSeriesEndpoint},
		{name: "storeapi port", o: Options{Flavor: FlavorThanos, SeriesEndpoint: StoreAPIScheme + "://thanos"}, want: ErrSeriesEndpoint},
		{
			name: "storeapi path", o: Options{Flavor: FlavorThanos, SeriesEndpoint: testStoreAPIPlain + "/store"},
			want: ErrSeriesEndpoint,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.o.Validate()
			if test.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, test.want)
		})
	}
}
