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
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	o := New()
	require.Equal(t, DefaultProxyListenPort, o.ListenPort)
	require.Equal(t, DefaultProxyListenAddress, o.ListenAddress)
	require.Equal(t, DefaultTLSProxyListenPort, o.TLSListenPort)
	require.Equal(t, DefaultTLSProxyListenAddress, o.TLSListenAddress)
	require.Equal(t, timeconv.Duration(DefaultReadHeaderTimeout), o.ReadHeaderTimeout)
	require.NotNil(t, o.MaxRequestBodySizeBytes)
	require.Equal(t, DefaultMaxRequestBodySizeBytes, *o.MaxRequestBodySizeBytes)
	require.Equal(t, 10*time.Second, time.Duration(o.ReadHeaderTimeout))
}

func TestFrontendOptionsEqual(t *testing.T) {
	f1 := New()
	f2 := New()
	f1.MaxRequestBodySizeBytes = nil
	f2.MaxRequestBodySizeBytes = nil
	require.True(t, f1.Equal(f2))

	f2 = f1.Clone()
	f1.ListenAddress = "trickster"
	require.NotEqual(t, f1.ListenAddress, f2.ListenAddress)
	require.False(t, f1.Equal(f2))
}

func TestClone(t *testing.T) {
	o := New()
	o.ListenAddress = "127.0.0.1"
	o.ConnectionsLimit = 100
	o.TruncateRequestBodyTooLarge = true
	*o.MaxRequestBodySizeBytes = 42

	c := o.Clone()
	require.NotSame(t, o, c)
	require.Equal(t, o.ListenAddress, c.ListenAddress)
	require.Equal(t, o.ConnectionsLimit, c.ConnectionsLimit)
	require.Equal(t, o.TruncateRequestBodyTooLarge, c.TruncateRequestBodyTooLarge)
	require.NotSame(t, o.MaxRequestBodySizeBytes, c.MaxRequestBodySizeBytes)
	require.Equal(t, *o.MaxRequestBodySizeBytes, *c.MaxRequestBodySizeBytes)
	require.True(t, o.Equal(c))

	*c.MaxRequestBodySizeBytes = 99
	require.Equal(t, int64(42), *o.MaxRequestBodySizeBytes)
	require.Equal(t, int64(99), *c.MaxRequestBodySizeBytes)
	require.False(t, o.Equal(c))

	o.MaxRequestBodySizeBytes = nil
	c = o.Clone()
	require.Nil(t, c.MaxRequestBodySizeBytes)
	require.True(t, o.Equal(c))
}

func TestInitialize(t *testing.T) {
	o := &Options{}
	require.NoError(t, o.Initialize())
	require.NotNil(t, o.MaxRequestBodySizeBytes)
	require.Equal(t, DefaultMaxRequestBodySizeBytes, *o.MaxRequestBodySizeBytes)

	custom := int64(1234)
	o = &Options{MaxRequestBodySizeBytes: &custom}
	require.NoError(t, o.Initialize())
	require.Equal(t, int64(1234), *o.MaxRequestBodySizeBytes)
}

func TestValidate(t *testing.T) {
	tlsCfg := &tls.Config{}
	okTLS := TLSConfigFunc(func() (*tls.Config, error) { return tlsCfg, nil })
	errTLS := TLSConfigFunc(func() (*tls.Config, error) {
		return nil, errors.New("tls config error")
	})

	tests := []struct {
		name    string
		opts    *Options
		f       TLSConfigFunc
		wantErr string
	}{
		{
			name:    "no listeners",
			opts:    &Options{},
			wantErr: "no http or https listeners configured",
		},
		{
			name: "http listener only",
			opts: &Options{ListenPort: 8480},
		},
		{
			name: "tls listener without serve tls",
			opts: &Options{TLSListenPort: 8483},
		},
		{
			name: "serve tls with valid config",
			opts: &Options{TLSListenPort: 8483, ServeTLS: true},
			f:    okTLS,
		},
		{
			name:    "serve tls with config error",
			opts:    &Options{TLSListenPort: 8483, ServeTLS: true},
			f:       errTLS,
			wantErr: "tls config error",
		},
		{
			name: "serve tls with zero tls port skips config",
			opts: &Options{ListenPort: 8480, ServeTLS: true},
			f:    errTLS,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.opts.Validate(tc.f)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tc.wantErr)
		})
	}
}
