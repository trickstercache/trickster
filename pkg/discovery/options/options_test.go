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

	"github.com/trickstercache/trickster/v2/pkg/config/types"
	awsopts "github.com/trickstercache/trickster/v2/pkg/discovery/aws/options"
	consulopts "github.com/trickstercache/trickster/v2/pkg/discovery/consul/options"
	dnsopts "github.com/trickstercache/trickster/v2/pkg/discovery/dns/options"
	fileopts "github.com/trickstercache/trickster/v2/pkg/discovery/file/options"
	httpsdopts "github.com/trickstercache/trickster/v2/pkg/discovery/httpsd/options"
	kubeopts "github.com/trickstercache/trickster/v2/pkg/discovery/kubernetes/options"
	nomadopts "github.com/trickstercache/trickster/v2/pkg/discovery/nomad/options"
	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const testLookupYAML = `
in-cluster:
  provider: kubernetes
corp-dns:
  provider: dns_srv
  dns:
    resolver: 10.0.0.53:53
    interval: 45s
`

func TestLookupUnmarshalAndInitialize(t *testing.T) {
	var l Lookup
	require.NoError(t, yaml.Unmarshal([]byte(testLookupYAML), &l))
	require.NoError(t, l.Initialize())
	require.NoError(t, l.Validate())

	k := l["in-cluster"]
	require.NotNil(t, k)
	require.Equal(t, "in-cluster", k.Name)
	require.Equal(t, providers.Kubernetes, k.Provider)
	require.NotNil(t, k.Kubernetes)
	require.True(t, k.Kubernetes.InCluster, "kubernetes defaults to in_cluster")

	d := l["corp-dns"]
	require.NotNil(t, d)
	require.Equal(t, providers.DNSSRV, d.Provider)
	require.NotNil(t, d.DNS)
	require.Equal(t, "10.0.0.53:53", d.DNS.Resolver)
	require.Equal(t, timeconv.Duration(45*time.Second), d.DNS.Interval)
}

func TestInitializeDefaults(t *testing.T) {
	o := &Options{Provider: providers.DNSA}
	require.NoError(t, o.Initialize("d"))
	require.NotNil(t, o.DNS)
	require.Equal(t, timeconv.Duration(dnsopts.DefaultInterval), o.DNS.Interval)

	o = &Options{Provider: providers.Kubernetes,
		Kubernetes: &kubeopts.Options{Kubeconfig: "/tmp/kc"}}
	require.NoError(t, o.Initialize("k"))
	require.False(t, o.Kubernetes.InCluster,
		"a provided kubeconfig must not be overridden to in_cluster")
}

func TestOptionsValidate(t *testing.T) {
	tests := []struct {
		name string
		o    *Options
		ok   bool
	}{
		{"missing provider", &Options{}, false},
		{"invalid provider", &Options{Provider: "consul"}, false},
		{"valid file", &Options{Provider: providers.File}, true},
		{"kube block on file provider", &Options{Provider: providers.File,
			Kubernetes: &kubeopts.Options{}}, false},
		{"dns block on kube provider", &Options{Provider: providers.Kubernetes,
			DNS: &dnsopts.Options{}}, false},
		{"in_cluster + kubeconfig", &Options{Provider: providers.Kubernetes,
			Kubernetes: &kubeopts.Options{InCluster: true, Kubeconfig: "/x"}}, false},
		{"bad resolver", &Options{Provider: providers.DNSSRV,
			DNS: &dnsopts.Options{Resolver: "not-host-port"}}, false},
		{"interval too low", &Options{Provider: providers.DNSA,
			DNS: &dnsopts.Options{Interval: timeconv.Duration(time.Millisecond)}}, false},
		{"valid dns", &Options{Provider: providers.DNSSRV,
			DNS: &dnsopts.Options{Resolver: "10.0.0.53:53",
				Interval: timeconv.Duration(time.Minute)}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.o.Validate()
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestLookupValidateNilEntry(t *testing.T) {
	l := Lookup{"bad": nil}
	require.Error(t, l.Validate())
}

func TestClone(t *testing.T) {
	o := &Options{Provider: providers.DNSSRV, Name: "d",
		DNS: &dnsopts.Options{Resolver: "r:53"}}
	c := o.Clone()
	require.Equal(t, o, c)
	c.DNS.Resolver = "other:53"
	require.Equal(t, "r:53", o.DNS.Resolver)

	l := Lookup{"d": o, "nil": nil}
	lc := l.Clone()
	require.Len(t, lc, 1)
	require.Equal(t, o, lc["d"])
}

func TestFileOptions(t *testing.T) {
	// defaults applied for the file provider
	o := &Options{Provider: providers.File}
	require.NoError(t, o.Initialize("f"))
	require.NotNil(t, o.File)
	require.Equal(t, timeconv.Duration(fileopts.DefaultPollInterval),
		o.File.PollInterval)
	_, err := o.Validate()
	require.NoError(t, err)

	// explicit interval preserved
	o = &Options{Provider: providers.File,
		File: &fileopts.Options{PollInterval: timeconv.Duration(5 * time.Second)}}
	require.NoError(t, o.Initialize("f"))
	require.Equal(t, timeconv.Duration(5*time.Second), o.File.PollInterval)

	// file block on a non-file provider is rejected
	o = &Options{Provider: providers.Kubernetes, File: &fileopts.Options{}}
	_, err = o.Validate()
	require.Error(t, err)

	// sub-minimum poll interval is rejected
	o = &Options{Provider: providers.File,
		File: &fileopts.Options{PollInterval: timeconv.Duration(time.Millisecond)}}
	_, err = o.Validate()
	require.Error(t, err)

	// clone is deep
	o = &Options{Provider: providers.File,
		File: &fileopts.Options{PollInterval: timeconv.Duration(time.Minute)}}
	c := o.Clone()
	c.File.PollInterval = 0
	require.Equal(t, timeconv.Duration(time.Minute), o.File.PollInterval)
}

// The shared 'http' block is foundation for the HTTP-polling providers.
// Until the first of them registers in providers.httpProviders, the block
// is rejected everywhere -- which is the correct behavior for config naming
// a capability the binary does not have, and is asserted here so that a
// provider landing without registering itself fails loudly.

func TestHTTPBlockRejectedOnNonHTTPProviders(t *testing.T) {
	for _, p := range []string{
		providers.Kubernetes, providers.DNSSRV, providers.DNSA, providers.File,
	} {
		t.Run(p, func(t *testing.T) {
			o := &Options{
				Name:     "test",
				Provider: p,
				HTTP:     &HTTPOptions{Endpoint: "http://example.com"},
			}
			_, err := o.Validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), "http")
		})
	}
}

// httpTestOptions builds a discoverer carrying the http block. The block's
// provider-placement rule is covered separately by
// TestHTTPBlockRejectedOnNonHTTPProviders; these exercise the contents.
func httpTestOptions(h *HTTPOptions) *Options {
	return &Options{Name: "test", HTTP: h}
}

func TestHTTPOptionsValidation(t *testing.T) {
	tests := map[string]struct {
		http    *HTTPOptions
		wantErr string
	}{
		"valid minimal": {
			&HTTPOptions{Endpoint: "http://example.com:8500"}, "",
		},
		"valid https with everything": {
			&HTTPOptions{
				Endpoint: "https://example.com",
				Interval: timeconv.Duration(5 * time.Second),
				Timeout:  timeconv.Duration(time.Second),
				Headers:  types.EnvStringMap{"X-Consul-Token": "abc"},
				Username: "u", Password: "p",
			}, "",
		},
		"missing endpoint": {&HTTPOptions{}, "'endpoint' is required"},
		"unparsable endpoint": {
			&HTTPOptions{Endpoint: "://nope"}, "not a valid url",
		},
		"non-http scheme": {
			&HTTPOptions{Endpoint: "ftp://example.com"}, "must be an http or https url",
		},
		"no host": {
			&HTTPOptions{Endpoint: "http:///just/a/path"}, "must include a host",
		},
		"interval below minimum": {
			&HTTPOptions{
				Endpoint: "http://example.com",
				Interval: timeconv.Duration(time.Millisecond),
			}, "'interval' must be at least 1s",
		},
		"timeout below minimum": {
			&HTTPOptions{
				Endpoint: "http://example.com",
				Timeout:  timeconv.Duration(time.Millisecond),
			}, "'timeout' must be at least 100ms",
		},
		"basic and bearer together": {
			&HTTPOptions{
				Endpoint: "http://example.com",
				Username: "u", BearerToken: "t",
			}, "mutually exclusive",
		},
		"both bearer forms": {
			&HTTPOptions{
				Endpoint:    "http://example.com",
				BearerToken: "t", BearerTokenFile: "/tmp/t",
			}, "mutually exclusive",
		},
		"password without username": {
			&HTTPOptions{Endpoint: "http://example.com", Password: "p"},
			"'password' requires 'username'",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := httpTestOptions(test.http).validateHTTP()
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestHTTPOptionsDefaults(t *testing.T) {
	o := httpTestOptions(&HTTPOptions{Endpoint: "http://example.com"})
	require.NoError(t, o.Initialize("test"))
	require.Equal(t, timeconv.Duration(DefaultHTTPInterval), o.HTTP.Interval)
	require.Equal(t, timeconv.Duration(DefaultHTTPTimeout), o.HTTP.Timeout)

	// explicit values survive Initialize
	o = httpTestOptions(&HTTPOptions{
		Endpoint: "http://example.com",
		Interval: timeconv.Duration(90 * time.Second),
		Timeout:  timeconv.Duration(45 * time.Second),
	})
	require.NoError(t, o.Initialize("test"))
	require.Equal(t, timeconv.Duration(90*time.Second), o.HTTP.Interval)
	require.Equal(t, timeconv.Duration(45*time.Second), o.HTTP.Timeout)
}

// Clone must be deep: a shallow HTTP pointer would let one discoverer's
// credential rotation or header edit reach another's config.
func TestHTTPOptionsCloneIsDeep(t *testing.T) {
	o := &Options{
		Name:     "test",
		Provider: providers.File,
		HTTP: &HTTPOptions{
			Endpoint: "http://example.com",
			Headers:  types.EnvStringMap{"X-Token": "original"},
			TLS:      &to.Options{InsecureSkipVerify: true},
		},
	}
	c := o.Clone()
	require.NotSame(t, o.HTTP, c.HTTP)
	require.NotSame(t, o.HTTP.TLS, c.HTTP.TLS)

	c.HTTP.Endpoint = "http://elsewhere.com"
	c.HTTP.Headers["X-Token"] = "mutated"
	c.HTTP.TLS.InsecureSkipVerify = false
	require.Equal(t, "http://example.com", o.HTTP.Endpoint)
	require.Equal(t, "original", o.HTTP.Headers["X-Token"])
	require.True(t, o.HTTP.TLS.InsecureSkipVerify)
}

func TestHTTPOptionsYAMLRoundTrip(t *testing.T) {
	const src = `
provider: file
http:
  endpoint: https://consul.example.com:8501
  interval: 15s
  timeout: 5s
  follow_redirects: true
  headers:
    X-Consul-Token: secret
  bearer_token_file: /var/run/token
  tls:
    insecure_skip_verify: true
`
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte(src), &o))
	require.Equal(t, "https://consul.example.com:8501", o.HTTP.Endpoint)
	require.Equal(t, timeconv.Duration(15*time.Second), o.HTTP.Interval)
	require.Equal(t, timeconv.Duration(5*time.Second), o.HTTP.Timeout)
	require.True(t, o.HTTP.FollowRedirects)
	require.Equal(t, "secret", o.HTTP.Headers["X-Consul-Token"])
	require.Equal(t, "/var/run/token", o.HTTP.BearerTokenFile)
	require.NotNil(t, o.HTTP.TLS)
	require.True(t, o.HTTP.TLS.InsecureSkipVerify)
}

func TestHTTPSDOptions(t *testing.T) {
	base := func() *Options {
		return &Options{
			Name:     "test",
			Provider: providers.HTTPSD,
			HTTP:     &HTTPOptions{Endpoint: "http://sd.example.com"},
			HTTPSD:   &httpsdopts.Options{},
		}
	}
	t.Run("format defaults to trickster", func(t *testing.T) {
		o := base()
		require.NoError(t, o.Initialize("test"))
		require.Equal(t, httpsdopts.FormatTrickster, o.HTTPSD.Format)
		ok, err := o.Validate()
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("prometheus format accepted", func(t *testing.T) {
		o := base()
		o.HTTPSD.Format = httpsdopts.FormatPrometheus
		ok, err := o.Validate()
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("unknown format rejected", func(t *testing.T) {
		o := base()
		o.HTTPSD.Format = "yaml"
		_, err := o.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "trickster or prometheus")
	})
	// the endpoint lives in the shared block, so http_sd without it has
	// nowhere to poll; that must fail startup rather than at first poll
	t.Run("http block required", func(t *testing.T) {
		o := base()
		o.HTTP = nil
		_, err := o.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "'http' block is required")
	})
	t.Run("block rejected on other providers", func(t *testing.T) {
		for _, p := range []string{
			providers.Kubernetes, providers.DNSSRV, providers.DNSA, providers.File,
		} {
			o := &Options{Name: "test", Provider: p, HTTPSD: &httpsdopts.Options{}}
			_, err := o.Validate()
			require.Error(t, err, "http_sd block should be rejected on %s", p)
		}
	})
	t.Run("initialize defaults the block itself", func(t *testing.T) {
		o := &Options{
			Name:     "test",
			Provider: providers.HTTPSD,
			HTTP:     &HTTPOptions{Endpoint: "http://sd.example.com"},
		}
		require.NoError(t, o.Initialize("test"))
		require.NotNil(t, o.HTTPSD, "an omitted http_sd block should still get defaults")
		require.Equal(t, httpsdopts.FormatTrickster, o.HTTPSD.Format)
	})
	t.Run("GetFormat tolerates a nil receiver", func(t *testing.T) {
		var o *httpsdopts.Options
		require.Equal(t, httpsdopts.FormatTrickster, o.GetFormat())
		require.Equal(t, httpsdopts.FormatTrickster, (&httpsdopts.Options{}).GetFormat())
		require.Equal(t, httpsdopts.FormatPrometheus,
			(&httpsdopts.Options{Format: httpsdopts.FormatPrometheus}).GetFormat())
	})
	t.Run("clone is deep", func(t *testing.T) {
		o := base()
		o.HTTPSD.Format = httpsdopts.FormatPrometheus
		c := o.Clone()
		require.NotSame(t, o.HTTPSD, c.HTTPSD)
		c.HTTPSD.Format = httpsdopts.FormatTrickster
		require.Equal(t, httpsdopts.FormatPrometheus, o.HTTPSD.Format)
	})
}

// The http block is live now that http_sd registers as an HTTP provider;
// this pins that registration, since a provider polling HTTP without it
// would have its connection config rejected at startup.
func TestHTTPSDIsRegisteredAsAnHTTPProvider(t *testing.T) {
	require.True(t, providers.IsHTTPProvider(providers.HTTPSD))
	require.True(t, providers.IsValidProvider(providers.HTTPSD))
}

func TestConsulOptions(t *testing.T) {
	base := func() *Options {
		return &Options{
			Name:     "test",
			Provider: providers.Consul,
			HTTP:     &HTTPOptions{Endpoint: "http://127.0.0.1:8500"},
			Consul:   &consulopts.Options{},
		}
	}
	// The timeout must outlast the blocking wait, so consul cannot share the
	// generic 10s default: it would abort every long poll. Initialize derives
	// one from the wait instead.
	t.Run("timeout default is derived from the wait", func(t *testing.T) {
		o := base()
		require.NoError(t, o.Initialize("test"))
		require.Equal(t, consulopts.DefaultWait, o.Consul.GetWait())
		require.Equal(t,
			timeconv.Duration(consulopts.PollTimeout(consulopts.DefaultWait)), o.HTTP.Timeout)
		require.Greater(t, time.Duration(o.HTTP.Timeout), consulopts.DefaultWait)
		ok, err := o.Validate()
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("derived timeout covers consul's own jitter", func(t *testing.T) {
		// Consul adds up to wait/16 before returning; a margin smaller than
		// that would abort healthy long polls
		wait := 5 * time.Minute
		require.Greater(t, consulopts.PollTimeout(wait), wait+wait/16)
	})
	t.Run("explicit short timeout is rejected", func(t *testing.T) {
		o := base()
		o.Consul.Wait = timeconv.Duration(time.Minute)
		o.HTTP.Timeout = timeconv.Duration(30 * time.Second)
		_, err := o.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "must be greater than 'consul.wait'")
	})
	t.Run("wait bounds", func(t *testing.T) {
		o := base()
		o.Consul.Wait = timeconv.Duration(time.Millisecond)
		_, err := o.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "at least 1s")

		o = base()
		o.Consul.Wait = timeconv.Duration(11 * time.Minute)
		_, err = o.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "at most 10m")
	})
	t.Run("http block required", func(t *testing.T) {
		o := base()
		o.HTTP = nil
		_, err := o.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "'http' block is required")
	})
	t.Run("block rejected on other providers", func(t *testing.T) {
		for _, p := range []string{providers.Kubernetes, providers.File, providers.HTTPSD} {
			o := &Options{Name: "test", Provider: p, Consul: &consulopts.Options{}}
			_, err := o.Validate()
			require.Error(t, err, "consul block should be rejected on %s", p)
		}
	})
	t.Run("warning_is_ready defaults true", func(t *testing.T) {
		var nilOpts *consulopts.Options
		require.True(t, nilOpts.GetWarningIsReady())
		require.True(t, (&consulopts.Options{}).GetWarningIsReady())
		f := false
		require.False(t, (&consulopts.Options{WarningIsReady: &f}).GetWarningIsReady())
	})
	t.Run("clone is deep", func(t *testing.T) {
		o := base()
		f := false
		o.Consul.WarningIsReady = &f
		o.Consul.Datacenter = "dc1"
		c := o.Clone()
		require.NotSame(t, o.Consul, c.Consul)
		require.NotSame(t, o.Consul.WarningIsReady, c.Consul.WarningIsReady)
		*c.Consul.WarningIsReady = true
		c.Consul.Datacenter = "dc2"
		require.False(t, *o.Consul.WarningIsReady)
		require.Equal(t, "dc1", o.Consul.Datacenter)
	})
	t.Run("initialize defaults the block itself", func(t *testing.T) {
		o := &Options{
			Name:     "test",
			Provider: providers.Consul,
			HTTP:     &HTTPOptions{Endpoint: "http://127.0.0.1:8500"},
		}
		require.NoError(t, o.Initialize("test"))
		require.NotNil(t, o.Consul)
	})
}

func TestNomadOptions(t *testing.T) {
	base := func() *Options {
		return &Options{
			Name:     "test",
			Provider: providers.Nomad,
			HTTP:     &HTTPOptions{Endpoint: "http://127.0.0.1:4646"},
			Nomad:    &nomadopts.Options{},
		}
	}
	// Nomad shares HashiCorp's blocking-query protocol with Consul, so its
	// timeout default is derived from the wait for the same reason.
	t.Run("timeout default is derived from the wait", func(t *testing.T) {
		o := base()
		require.NoError(t, o.Initialize("test"))
		require.Equal(t, nomadopts.DefaultWait, o.Nomad.GetWait())
		require.Greater(t, time.Duration(o.HTTP.Timeout), nomadopts.DefaultWait)
		ok, err := o.Validate()
		require.NoError(t, err)
		require.True(t, ok)
	})
	t.Run("explicit short timeout is rejected", func(t *testing.T) {
		o := base()
		o.Nomad.Wait = timeconv.Duration(time.Minute)
		o.HTTP.Timeout = timeconv.Duration(30 * time.Second)
		_, err := o.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "must be greater than 'nomad.wait'")
	})
	t.Run("wait bounds", func(t *testing.T) {
		o := base()
		o.Nomad.Wait = timeconv.Duration(time.Millisecond)
		_, err := o.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "at least 1s")

		o = base()
		o.Nomad.Wait = timeconv.Duration(11 * time.Minute)
		_, err = o.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "at most 10m")
	})
	t.Run("http block required", func(t *testing.T) {
		o := base()
		o.HTTP = nil
		_, err := o.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "'http' block is required")
	})
	t.Run("block rejected on other providers", func(t *testing.T) {
		for _, p := range []string{providers.Consul, providers.File, providers.HTTPSD} {
			o := &Options{Name: "test", Provider: p, Nomad: &nomadopts.Options{}}
			_, err := o.Validate()
			require.Error(t, err, "nomad block should be rejected on %s", p)
		}
	})
	t.Run("clone is deep", func(t *testing.T) {
		o := base()
		o.Nomad.Namespace = "team-a"
		c := o.Clone()
		require.NotSame(t, o.Nomad, c.Nomad)
		c.Nomad.Namespace = "team-b"
		require.Equal(t, "team-a", o.Nomad.Namespace)
	})
	t.Run("initialize defaults the block itself", func(t *testing.T) {
		o := &Options{
			Name:     "test",
			Provider: providers.Nomad,
			HTTP:     &HTTPOptions{Endpoint: "http://127.0.0.1:4646"},
		}
		require.NoError(t, o.Initialize("test"))
		require.NotNil(t, o.Nomad)
	})
}

func TestAWSOptions(t *testing.T) {
	base := func() *Options {
		return &Options{
			Name:     "test",
			Provider: providers.AWS,
			AWS:      &awsopts.Options{Region: "us-east-1"},
		}
	}
	t.Run("service defaults to ec2", func(t *testing.T) {
		o := base()
		require.NoError(t, o.Initialize("test"))
		require.Equal(t, awsopts.ServiceEC2, o.AWS.Service)
		ok, err := o.Validate()
		require.NoError(t, err)
		require.True(t, ok)
	})
	// aws derives its endpoint from region and service, so the shared http
	// block is optional here where every other http provider requires it
	t.Run("http endpoint is optional", func(t *testing.T) {
		o := base()
		require.NoError(t, o.Initialize("test"))
		require.NotNil(t, o.HTTP, "an http block is created for the shared defaults")
		require.Empty(t, o.HTTP.Endpoint)
		_, err := o.Validate()
		require.NoError(t, err)
		require.True(t, providers.DerivesEndpoint(providers.AWS))
		require.False(t, providers.DerivesEndpoint(providers.HTTPSD))
	})
	t.Run("http timings still validated without an endpoint", func(t *testing.T) {
		o := base()
		require.NoError(t, o.Initialize("test"))
		o.HTTP.Interval = timeconv.Duration(time.Millisecond)
		_, err := o.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "'interval' must be at least 1s")
	})
	t.Run("unknown service rejected", func(t *testing.T) {
		o := base()
		o.AWS.Service = "lambda"
		_, err := o.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "must be one of")
	})
	t.Run("half a static credential rejected", func(t *testing.T) {
		o := base()
		o.AWS.AccessKey = "AKIA"
		_, err := o.Validate()
		require.Error(t, err)
	})
	// the discovery service doubles as the signing service
	t.Run("signer options derive the signing service", func(t *testing.T) {
		o := base()
		o.AWS.Service = awsopts.ServiceEC2
		so := o.AWS.SignerOptions()
		require.Equal(t, "ec2", so.GetService())
		require.Equal(t, "us-east-1", so.Region)
		var nilOpts *awsopts.Options
		require.Nil(t, nilOpts.SignerOptions())
	})
	t.Run("block rejected on other providers", func(t *testing.T) {
		for _, p := range []string{providers.Consul, providers.File, providers.HTTPSD} {
			o := &Options{Name: "test", Provider: p, AWS: &awsopts.Options{}}
			_, err := o.Validate()
			require.Error(t, err, "aws block should be rejected on %s", p)
		}
	})
	t.Run("clone is deep", func(t *testing.T) {
		o := base()
		c := o.Clone()
		require.NotSame(t, o.AWS, c.AWS)
		c.AWS.Region = "eu-west-1"
		require.Equal(t, "us-east-1", o.AWS.Region)
	})
	t.Run("secret key redacted in dumps", func(t *testing.T) {
		o := base()
		o.AWS.AccessKey = "AKIA"
		o.AWS.SecretKey = "super-secret"
		b, err := yaml.Marshal(o)
		require.NoError(t, err)
		require.NotContains(t, string(b), "super-secret")
	})
}
