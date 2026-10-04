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

package reverseproxy

import (
	"context"
	"net"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"

	"github.com/stretchr/testify/require"
)

func TestDefaultHealthCheckConfig(t *testing.T) {
	c, _ := NewClient("test", bo.New(), nil, nil, nil, nil)

	dho := c.DefaultHealthCheckConfig()
	require.NotNil(t, dho)

	if dho.Path != "" {
		t.Error("expected / for path", dho.Path)
	}
}

func streamClient(t *testing.T, originURL string) *Client {
	t.Helper()
	o := bo.New()
	o.OriginURL = originURL
	require.NoError(t, o.Initialize("stream-member"))
	c, err := NewClient("stream-member", o, nil, nil, nil, nil)
	require.NoError(t, err)
	return c.(*Client)
}

// a tcp member is healthy when a connection to it opens; nothing is sent
func TestTCPConnectProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	accepted := make(chan struct{}, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			_ = conn.Close()
		}
	}()
	c := streamClient(t, "tcp://"+ln.Addr().String())
	probe := c.HealthCheckProbe()
	require.NotNil(t, probe)
	require.Empty(t, c.HealthCheckUnsupported())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, probe(ctx))
	<-accepted

	// the probe takes no HTTP request options, or the registrar would refuse it
	dho := c.DefaultHealthCheckConfig()
	require.Empty(t, dho.Scheme)
	require.Empty(t, dho.Host)
	require.Empty(t, dho.Path)

	_ = ln.Close()
	require.Error(t, probe(ctx), "nothing is listening any more")
	expired, stop := context.WithCancel(context.Background())
	stop()
	require.Error(t, probe(expired))
}

// every other origin keeps its request probe; a udp origin has none to run at all
func TestProbeByOrigin(t *testing.T) {
	http := streamClient(t, "http://127.0.0.1:9090/base")
	require.Nil(t, http.HealthCheckProbe())
	require.Empty(t, http.HealthCheckUnsupported())
	dho := http.DefaultHealthCheckConfig()
	require.Equal(t, "http", dho.Scheme)
	require.Equal(t, "127.0.0.1:9090", dho.Host)

	udp := streamClient(t, "udp://127.0.0.1:5353")
	require.Nil(t, udp.HealthCheckProbe())
	require.NotEmpty(t, udp.HealthCheckUnsupported())
	require.Empty(t, udp.DefaultHealthCheckConfig().Scheme)
}
