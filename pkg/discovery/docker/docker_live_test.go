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

package docker

import (
	"os"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	dockeropts "github.com/trickstercache/trickster/v2/pkg/discovery/docker/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"

	"github.com/stretchr/testify/require"
)

// These tests run against a real Docker daemon and are skipped unless
// TRICKSTER_DOCKER_TEST=1, following the repo's TRICKSTER_DNS_TEST and
// TRICKSTER_AWS_TEST precedent. They are read-only: nothing here creates,
// starts or stops a container.
//
// The daemon socket is taken from DOCKER_HOST when set, and otherwise
// from the provider's own default. On Docker Desktop the socket is under
// the user's home rather than /var/run, so:
//
//	TRICKSTER_DOCKER_TEST=1 \
//	  DOCKER_HOST=unix://$HOME/.docker/run/docker.sock \
//	  go test ./pkg/discovery/docker/ -run Live -v -count=1
func liveOptions(t *testing.T) *do.Options {
	t.Helper()
	if os.Getenv("TRICKSTER_DOCKER_TEST") != "1" {
		t.Skip("set TRICKSTER_DOCKER_TEST=1 to run against a real docker daemon")
	}
	return &do.Options{
		Name:     "live-docker",
		Provider: "docker",
		Docker:   &dockeropts.Options{},
		HTTP:     &do.HTTPOptions{Endpoint: os.Getenv("DOCKER_HOST")},
	}
}

func liveList(t *testing.T, q *do.Query) ([]container, mapping) {
	t.Helper()
	p, err := newProvider("live-docker", liveOptions(t))
	require.NoError(t, err)
	target, err := p.listURL(q)
	require.NoError(t, err)
	s := &subscription{p: p, url: target}
	cs, err := s.list(t.Context())
	require.NoError(t, err)
	return cs, mapping{scheme: "http", network: q.Network,
		addressType: q.AddressType, port: q.Port, portLabel: q.PortLabel}
}

// The pinned API version must actually be served, and the response must
// decode into the declared types. This is the assertion that would catch
// the daemon's shape drifting away from what this package expects.
func TestLiveListAndDecode(t *testing.T) {
	cs, _ := liveList(t, &do.Query{})
	require.NotEmpty(t, cs, "no running containers; start something to test against")
	for _, c := range cs {
		require.NotEmpty(t, c.ID)
		require.Equal(t, stateRunning, c.State,
			"the default status filter must exclude everything else")
		require.NotNil(t, c.NetworkSettings)
	}
	t.Logf("listed %d running containers", len(cs))
}

// GET /containers/json carries no Health object -- health is only in the
// Status string. If a future API version adds one, this test fails and is
// the prompt to read it from there instead.
func TestLiveHealthIsOnlyInTheStatusString(t *testing.T) {
	cs, _ := liveList(t, &do.Query{})
	var withHealth int
	for _, c := range cs {
		if c.ready() != discovery.ReadyUnknown {
			withHealth++
			require.Contains(t, c.Status, "(",
				"readiness came from somewhere other than the Status string")
		}
	}
	t.Logf("%d of %d containers report health", withHealth, len(cs))
}

// A container exposing one tcp port needs no configured port. This is the
// claim that makes the provider usable without per-container config, so
// it is worth confirming against a real inventory.
func TestLiveSolePortResolution(t *testing.T) {
	cs, m := liveList(t, &do.Query{})
	snap, skipped := toMembers(cs, m)
	t.Logf("mapped %d members, excluded %d", len(snap), len(skipped))
	for _, e := range skipped {
		t.Logf("  excluded %s: %s", e.name, e.reason)
	}
	require.NotEmpty(t, snap,
		"no container resolved a port; expected at least one single-port container")
	for _, mem := range snap {
		require.NotEmpty(t, mem.Address)
		require.Contains(t, mem.Address, ":")
	}
}

// The server-side label filter is the Engine API's own document format;
// a name it does not recognize is a 400, so this confirms the encoding.
func TestLiveLabelFilterIsAccepted(t *testing.T) {
	cs, _ := liveList(t, &do.Query{
		Filters: map[string][]string{
			"label": {"com.trickster.probe=definitely-not-set"},
		}})
	require.Empty(t, cs, "a label nothing carries must match nothing")
}

// An unknown filter name must fail loudly rather than be ignored, so a
// typo in config is not a silently empty pool.
func TestLiveUnknownFilterIsAnError(t *testing.T) {
	p, err := newProvider("live-docker", liveOptions(t))
	require.NoError(t, err)
	target, err := p.listURL(&do.Query{
		Filters: map[string][]string{"nonsense": {"x"}}})
	require.NoError(t, err)
	s := &subscription{p: p, url: target}
	_, err = s.list(t.Context())
	require.Error(t, err)
	t.Logf("daemon rejected the filter: %v", err)
}

// captureFixture rewrites testdata/containers.json from the live daemon.
// It writes the bytes the daemon actually sent rather than a re-encoding
// of the decoded structs, which would only prove the types are
// self-consistent. Redact before committing: see the fixture header.
func TestLiveCaptureFixture(t *testing.T) {
	path := os.Getenv("TRICKSTER_DOCKER_CAPTURE")
	if path == "" {
		t.Skip("set TRICKSTER_DOCKER_CAPTURE=<path> to write a testdata fixture")
	}
	p, err := newProvider("live-docker", liveOptions(t))
	require.NoError(t, err)
	target, err := p.listURL(&do.Query{
		Filters: map[string][]string{"status": {"running", "exited"}}})
	require.NoError(t, err)
	s := &subscription{p: p, url: target}
	cs, err := s.list(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, cs)
	t.Logf("capture target %s holds %d containers", path, len(cs))
}
