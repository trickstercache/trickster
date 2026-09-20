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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestParseKeySource(t *testing.T) {
	for in, want := range map[string]KeySource{
		"":                  {Kind: KeyClientIP},
		"client_ip":         {Kind: KeyClientIP},
		"  host ":           {Kind: KeyHost},
		"header:x-tenant":   {Kind: KeyHeader, Name: "X-Tenant"},
		"header: X-Tenant ": {Kind: KeyHeader, Name: "X-Tenant"},
		"cookie:session":    {Kind: KeyCookie, Name: "session"},
		"query:tenant":      {Kind: KeyQuery, Name: "tenant"},
		"sni":               {Kind: KeySNI},
	} {
		got, err := ParseKeySource(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{"snI:x", "header:", "cookie: ", "query:a=b", "header:two words", "cookie:a;b", "ip", "header"} {
		_, err := ParseKeySource(in)
		require.ErrorIs(t, err, ErrInvalidKeySource, in)
	}
}

func load(t *testing.T, doc string) *Options {
	t.Helper()
	o := New()
	require.NoError(t, yaml.Unmarshal([]byte(doc), o))
	return o
}

func TestHRWOptions(t *testing.T) {
	o := load(t, "mechanism: hrw\n")
	require.NoError(t, o.Initialize("alb1"))
	require.Equal(t, KeySource{Kind: KeyClientIP}, o.HRW.KeySource)
	require.Equal(t, DefaultIPv6Prefix, o.HRW.IPv6Prefix)
	_, err := o.Validate()
	require.NoError(t, err)

	o = load(t, "mechanism: highest_random_weight\nhrw:\n  key: header:x-tenant\n  ipv6_prefix: 56\n")
	require.NoError(t, o.Initialize("alb1"))
	require.Equal(t, KeySource{Kind: KeyHeader, Name: "X-Tenant"}, o.HRW.KeySource)
	require.Equal(t, 56, o.HRW.IPv6Prefix)
	_, err = o.Validate()
	require.NoError(t, err)

	require.ErrorIs(t, load(t, "mechanism: hrw\nhrw:\n  key: port\n").Initialize("alb1"), ErrInvalidKeySource)
	bad := load(t, "mechanism: hrw\nhrw:\n  ipv6_prefix: 129\n")
	require.NoError(t, bad.Initialize("alb1"))
	_, err = bad.Validate()
	require.ErrorIs(t, err, ErrInvalidIPv6Prefix)
	unparsed := &Options{MechanismName: names.MechanismHRW, HRW: HRWOptions{Key: "nonsense"}}
	_, err = unparsed.Validate()
	require.ErrorIs(t, err, ErrInvalidKeySource)
}

func TestLTOptions(t *testing.T) {
	o := load(t, "mechanism: lt\n")
	require.NoError(t, o.Initialize("alb1"))
	require.Empty(t, o.LT.Signal, "an unset signal is resolved per listener protocol, not at load")
	require.Zero(t, o.LTDecay())
	for _, code := range []int{200, 404, 500, 501, 505} {
		require.True(t, o.LT.GoodCodes.Contains(code), code)
	}
	for _, code := range []int{502, 503, 504} {
		require.False(t, o.LT.GoodCodes.Contains(code), code)
	}
	_, err := o.Validate()
	require.NoError(t, err)

	o = load(t, "mechanism: least_time\nlt:\n  status_codes: [{start: 200, end: 499}]\n  decay: 30s\n  signal: first_write\n")
	require.NoError(t, o.Initialize("alb1"))
	require.Equal(t, 30*time.Second, o.LTDecay())
	require.True(t, o.LT.GoodCodes.Contains(404))
	require.False(t, o.LT.GoodCodes.Contains(500))
	_, err = o.Validate()
	require.NoError(t, err)

	c := o.Clone()
	require.Equal(t, o.LT.StatusCodes, c.LT.StatusCodes)
	require.NotSame(t, o.LT.GoodCodes, c.LT.GoodCodes)
	c.LT.StatusCodes[0].End = 299
	require.Equal(t, 499, o.LT.StatusCodes[0].End, "the clone shares its ranges with the original")

	for doc, want := range map[string]error{
		"mechanism: lt\nlt:\n  signal: last_byte\n":                      ErrInvalidLTSignal,
		"mechanism: lt\nlt:\n  decay: -5s\n":                             ErrInvalidLTDecay,
		"mechanism: lt\nlt:\n  status_codes: [{start: 500, end: 200}]\n": types.ErrInvalidStatusRange,
	} {
		bad := load(t, doc)
		require.NoError(t, bad.Initialize("alb1"))
		_, err := bad.Validate()
		require.ErrorIs(t, err, want, doc)
	}
}

// a strategy's block is refused on any other mechanism, as output_format is outside tsm
func TestStrategyBlocksBelongToTheirMechanism(t *testing.T) {
	for doc, want := range map[string]error{
		"mechanism: rr\nhrw:\n  key: host\n":         ErrHRWOnlyForHRW,
		"mechanism: rr\nlt:\n  decay: 5s\n":          ErrLTOnlyForLT,
		"mechanism: lt\nhrw:\n  ipv6_prefix: 48\n":   ErrHRWOnlyForHRW,
		"mechanism: hrw\nlt:\n  signal: first_write": ErrLTOnlyForLT,
		"mechanism: p2c\nlt:\n  status_codes: [200]": ErrLTOnlyForLT,
	} {
		o := load(t, doc)
		require.NoError(t, o.Initialize("alb1"))
		_, err := o.Validate()
		require.ErrorIs(t, err, want, doc)
	}
	for _, mech := range []string{names.MechanismP2C, names.MechanismLC, names.MechanismRR} {
		o := load(t, "mechanism: "+mech+"\n")
		require.NoError(t, o.Initialize("alb1"))
		_, err := o.Validate()
		require.NoError(t, err, mech)
	}
}

func TestKeySourcePlanes(t *testing.T) {
	for in, want := range map[string][3]bool{
		// readable on: a tcp or udp listener, a tls listener, an http listener
		"client_ip":       {true, true, true},
		"sni":             {false, true, false},
		"host":            {false, false, true},
		"header:X-Tenant": {false, false, true},
		"cookie:session":  {false, false, true},
		"query:tenant":    {false, false, true},
	} {
		ks, err := ParseKeySource(in)
		require.NoError(t, err)
		require.Equal(t, want, [3]bool{ks.OnStream(false), ks.OnStream(true), ks.OnHTTP()}, in)
	}
}

func TestLTSignalFor(t *testing.T) {
	o := &Options{}
	for protocol, want := range map[string]string{
		"http": LTSignalFirstWrite, "tcp": LTSignalConnect, "tls": LTSignalConnect,
		"udp": LTSignalFirstReply, "mysql": LTSignalFirstWrite,
	} {
		got, err := o.LTSignalFor(protocol)
		require.NoError(t, err)
		require.Equal(t, want, got, protocol)
	}
	o.LT.Signal = LTSignalFirstByte
	got, err := o.LTSignalFor("tls")
	require.NoError(t, err)
	require.Equal(t, LTSignalFirstByte, got)
	for _, protocol := range []string{"http", "udp"} {
		_, err := o.LTSignalFor(protocol)
		require.ErrorIs(t, err, ErrInvalidLTSignal, protocol)
	}
}

func TestStreamOptions(t *testing.T) {
	o := load(t, "mechanism: rr\nstream:\n  connect_retries: 2\n  passive_health: {}\n")
	require.NoError(t, o.Initialize("alb1"))
	require.Equal(t, 2, o.Stream.ConnectRetries)
	require.Equal(t, DefaultPassiveFailures, o.Stream.PassiveHealth.Failures, "an empty block turns it on")
	_, err := o.Validate()
	require.NoError(t, err)

	o = load(t, "mechanism: p2c\nstream:\n  passive_health: {failures: 5, eject: 10s, max_ejected_percent: 25}\n")
	require.NoError(t, o.Initialize("alb1"))
	require.Equal(t, PassiveHealthOptions{Failures: 5, Eject: timeconv.Duration(10 * time.Second), MaxEjectedPercent: 25},
		*o.Stream.PassiveHealth)
	c := o.Clone()
	c.Stream.PassiveHealth.Failures = 9
	c.Stream.ConnectRetries = 4
	require.Equal(t, 5, o.Stream.PassiveHealth.Failures, "the clone shares its stream options")
	require.Zero(t, o.Stream.ConnectRetries)
	require.Nil(t, (*StreamOptions)(nil).Clone())

	for doc, want := range map[string]error{
		"stream:\n  connect_retries: -1\n":                        ErrInvalidConnectRetries,
		"stream:\n  connect_retries: 11\n":                        ErrInvalidConnectRetries,
		"stream:\n  passive_health: {failures: -1}\n":             ErrInvalidPassiveHealth,
		"stream:\n  passive_health: {eject: -1s}\n":               ErrInvalidPassiveHealth,
		"stream:\n  passive_health: {max_ejected_percent: 101}\n": ErrInvalidPassiveHealth,
		"stream:\n  passive_health: {max_ejected_percent: -5}\n":  ErrInvalidPassiveHealth,
		"lt:\n  signal: last_byte\n":                              ErrInvalidLTSignal,
	} {
		mech := "mechanism: rr\n"
		if want == ErrInvalidLTSignal {
			mech = "mechanism: lt\n"
		}
		bad := load(t, mech+doc)
		require.NoError(t, bad.Initialize("alb1"))
		_, err := bad.Validate()
		require.ErrorIs(t, err, want, doc)
	}
}
