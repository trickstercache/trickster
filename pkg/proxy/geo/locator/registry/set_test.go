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

package registry

import (
	"errors"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator"
	headeropts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/header/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/options"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

const fakeProvider = "fake"

var errFake = errors.New("fake locator failed")

type fakeLocator struct {
	closed atomic.Bool
	built  time.Time
}

func (*fakeLocator) Locate(netip.Addr) (geo.Location, error) { return geo.Location{}, nil }
func (*fakeLocator) Serves() geo.Fields                      { return geo.FieldCountry }
func (f *fakeLocator) BuildTime() time.Time                  { return f.built }
func (f *fakeLocator) Close() error {
	f.closed.Store(true)
	return errFake
}

func init() {
	constructors[fakeProvider] = func(name string, _ *options.Options) (locator.Locator, error) {
		if strings.HasPrefix(name, "fail") {
			return nil, errFake
		}
		return &fakeLocator{built: time.Unix(1700000000, 0)}, nil
	}
}

func fake(s Set, name string) *fakeLocator {
	return s[name].Locator.(*fakeLocator)
}

func TestSetLifecycle(t *testing.T) {
	lookup := options.Lookup{
		"a": {Name: "a", Provider: fakeProvider},
		"b": {Name: "b", Provider: fakeProvider},
	}
	first, err := Build([]string{"a", "b"}, lookup, nil)
	require.NoError(t, err)
	require.NotNil(t, first.Locator("a"))
	require.Nil(t, first.Locator("missing"))

	// an unchanged locator is kept, a changed one is built anew, and a removed one is dropped
	changed := lookup.Clone()
	changed["b"].Header = headeropts.New()
	second, err := Build([]string{"a", "b"}, changed, first)
	require.NoError(t, err)
	require.Same(t, first["a"], second["a"])
	require.NotSame(t, first["b"].Locator, second["b"].Locator)

	// the replaced Set closes what the new one does not keep, once the new one serves
	first.CloseExcept(second)
	require.False(t, fake(first, "a").closed.Load())
	require.True(t, fake(first, "b").closed.Load())

	// a failed build closes what it built and nothing it was handed
	lookup["fail"] = &options.Options{Name: "fail", Provider: fakeProvider}
	lookup["c"] = &options.Options{Name: "c", Provider: fakeProvider}
	_, err = Build([]string{"a", "c", "fail"}, lookup, second)
	require.ErrorIs(t, err, errFake)
	require.False(t, fake(second, "a").closed.Load())
	_, err = Build([]string{"undefined"}, lookup, second)
	require.ErrorContains(t, err, `geo locator "undefined" is not defined`)

	second.Close()
	require.True(t, fake(second, "a").closed.Load())
	require.True(t, fake(second, "b").closed.Load())
	Set(nil).Close()
}

func TestBuildTimeGauge(t *testing.T) {
	published.Store(nil)
	require.Zero(t, testutil.CollectAndCount(buildTimeCollector{}))
	s, err := Build([]string{"gauge"}, options.Lookup{"gauge": {Name: "gauge", Provider: fakeProvider}}, nil)
	require.NoError(t, err)
	Publish(s)
	require.NoError(t, testutil.CollectAndCompare(buildTimeCollector{}, strings.NewReader(`
# HELP trickster_geo_locator_build_timestamp_seconds When the data a geo locator has loaded was built, in seconds since the epoch.
# TYPE trickster_geo_locator_build_timestamp_seconds gauge
trickster_geo_locator_build_timestamp_seconds{geo_locator="gauge"} 1.7e+09
`)))
	fake(s, "gauge").built = time.Time{}
	require.Zero(t, testutil.CollectAndCount(buildTimeCollector{}))
	Publish(nil)
}
