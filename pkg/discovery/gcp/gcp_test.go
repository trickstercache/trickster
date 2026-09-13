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

package gcp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	gcpopts "github.com/trickstercache/trickster/v2/pkg/discovery/gcp/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// isolate clears the ambient Google credential environment so a developer's
// own gcloud login cannot make a test pass or fail.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GCLOUD_PROJECT", "")
	// point gcloud's config at an empty directory so well-known user
	// credentials are not found
	t.Setenv("CLOUDSDK_CONFIG", t.TempDir())
	// keep a metadata-server probe from stalling the resolve
	t.Setenv("GCE_METADATA_HOST", "127.0.0.1:1")
}

// fakeCompute serves instances.aggregatedList pages and records requests.
type fakeCompute struct {
	*httptest.Server
	mtx      sync.Mutex
	pages    []string
	status   int
	errBody  string
	requests []*http.Request
	hits     atomic.Int64
}

func newFakeCompute(t *testing.T, pages ...string) *fakeCompute {
	t.Helper()
	f := &fakeCompute{pages: pages, status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.mtx.Lock()
		f.requests = append(f.requests, r.Clone(r.Context()))
		status, errBody, pages := f.status, f.errBody, f.pages
		f.mtx.Unlock()

		// an unauthorized request is what a real API would reject
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("request carried no bearer token")
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			w.Write([]byte(errBody))
			return
		}
		idx := 0
		if tok := r.URL.Query().Get("pageToken"); tok != "" {
			fmt.Sscanf(tok, "page-%d", &idx)
		}
		if idx >= len(pages) {
			w.Write([]byte(`{"items":{}}`))
			return
		}
		w.Write([]byte(pages[idx]))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeCompute) setError(status int, body string) {
	f.mtx.Lock()
	f.status, f.errBody = status, body
	f.mtx.Unlock()
}

func (f *fakeCompute) lastRequest(t *testing.T) *http.Request {
	t.Helper()
	f.mtx.Lock()
	defer f.mtx.Unlock()
	require.NotEmpty(t, f.requests)
	return f.requests[len(f.requests)-1]
}

// pageWith renders an aggregatedList page.
func pageWith(nextToken string, instances ...gceInstance) string {
	page := aggregatedList{
		Items:         map[string]zoneInstances{"zones/us-central1-a": {Instances: instances}},
		NextPageToken: nextToken,
	}
	b, _ := json.Marshal(page)
	return string(b)
}

func fakeOptions(endpoint string) *do.Options {
	return &do.Options{
		Name:     "test-gcp",
		Provider: "gcp",
		GCP:      &gcpopts.Options{Service: gcpopts.ServiceGCE, Project: "test-project"},
		HTTP: &do.HTTPOptions{
			Endpoint: endpoint,
			Interval: timeconv.Duration(25 * time.Millisecond),
			Timeout:  timeconv.Duration(5 * time.Second),
		},
	}
}

// fakeProvider builds a provider with a static token source, so the request
// path is exercised without resolving real credentials.
func fakeProvider(t *testing.T, endpoint string) *provider {
	t.Helper()
	p, err := newProvider("test-gcp", fakeOptions(endpoint))
	require.NoError(t, err)
	p.tokens = oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: "test-token", TokenType: "Bearer",
	})
	return p
}

func fakeSubscription(t *testing.T, endpoint string, q *do.Query) *subscription {
	t.Helper()
	runner, err := fakeProvider(t, endpoint).newSubscription(q,
		func(discovery.Snapshot) {})
	require.NoError(t, err)
	return runner.(*subscription)
}

func TestNewRequiresGCPOptions(t *testing.T) {
	_, err := New("test", nil)
	require.Error(t, err)
	_, err = New("test", &do.Options{Provider: "gcp"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "'gcp' options block")
}

// service is required even though 'instances' is the only value this build
// supports. Defaulting it now would be permanent: a default cannot be taken
// back once configs rely on it, and every service added later would then be
// reached by opting out of a value the operator never chose.
func TestNewRequiresService(t *testing.T) {
	o := fakeOptions("http://example.com")
	o.GCP.Service = ""
	_, err := New("test", o)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires 'gcp.service'")

	// naming the resource collection rather than the product is the likely
	// mistake, and must fail loudly rather than fall back
	o = fakeOptions("http://example.com")
	o.GCP.Service = "instances"
	_, err = New("test", o)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported")

	d, err := New("test", fakeOptions("http://example.com"))
	require.NoError(t, err)
	require.NotNil(t, d)
}

// The endpoint defaults to the public API and can be overridden for a
// private service endpoint or a test double.
func TestEndpointDefaultAndOverride(t *testing.T) {
	p, err := newProvider("test", &do.Options{
		Provider: "gcp", GCP: &gcpopts.Options{Service: gcpopts.ServiceGCE, Project: "p"}})
	require.NoError(t, err)
	require.Equal(t, DefaultEndpoint, p.endpoint)

	p, err = newProvider("test", fakeOptions("https://compute.example.com/"))
	require.NoError(t, err)
	require.Equal(t, "https://compute.example.com", p.endpoint,
		"a trailing slash would double up when the path is appended")

	_, err = newProvider("test", fakeOptions("not-absolute"))
	require.Error(t, err)
}

// Construction must not reach the network: startup cannot depend on the
// metadata server being up at that instant.
func TestNewProviderDoesNoNetworkIO(t *testing.T) {
	isolate(t)
	p, err := newProvider("test", &do.Options{
		Provider: "gcp", GCP: &gcpopts.Options{Service: gcpopts.ServiceGCE}})
	require.NoError(t, err)
	require.NotNil(t, p)
	require.Nil(t, p.tokens, "credentials must not be resolved at construction")
}

func TestAggregatedListRequestShape(t *testing.T) {
	f := newFakeCompute(t, pageWith("", instance("vm-1", statusRunning, "10.0.0.1", "")))
	s := fakeSubscription(t, f.URL,
		&do.Query{Port: "9090", Filter: `labels.env = "prod"`})
	_, err := s.listInstances(t.Context())
	require.NoError(t, err)

	r := f.lastRequest(t)
	require.Equal(t, "/compute/v1/projects/test-project/aggregated/instances",
		r.URL.Path)
	require.Equal(t, `labels.env = "prod"`, r.URL.Query().Get("filter"))
	require.Equal(t, "true", r.URL.Query().Get("returnPartialSuccess"),
		"one unreachable zone must not fail the whole call")
	require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
}

// A project name with a character needing escaping must not corrupt the path.
func TestProjectIsPathEscaped(t *testing.T) {
	f := newFakeCompute(t, `{"items":{}}`)
	p := fakeProvider(t, f.URL)
	p.gcp.Project = "weird/project"
	runner, err := p.newSubscription(&do.Query{Port: "9090"},
		func(discovery.Snapshot) {})
	require.NoError(t, err)
	_, err = runner.(*subscription).listInstances(t.Context())
	require.NoError(t, err)
	require.Equal(t, "/compute/v1/projects/weird%2Fproject/aggregated/instances",
		f.lastRequest(t).URL.EscapedPath())
}

// Pagination accumulates: a partial page set is a partial membership, and
// emitting one would drain the pool of everything later pages held.
func TestPaginationAccumulates(t *testing.T) {
	f := newFakeCompute(t,
		pageWith("page-1", instance("vm-1", statusRunning, "10.0.0.1", "")),
		pageWith("page-2", instance("vm-2", statusRunning, "10.0.0.2", "")),
		pageWith("", instance("vm-3", statusRunning, "10.0.0.3", "")),
	)
	s := fakeSubscription(t, f.URL, &do.Query{Port: "9090"})
	got, err := s.listInstances(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.EqualValues(t, 3, f.hits.Load())
}

// A pageToken that never clears must not spin forever inside one poll.
func TestPaginationIsBounded(t *testing.T) {
	looping := pageWith("page-0", instance("vm-1", statusRunning, "10.0.0.1", ""))
	f := newFakeCompute(t, looping)
	s := fakeSubscription(t, f.URL, &do.Query{Port: "9090"})
	_, err := s.listInstances(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "did not terminate")
	require.LessOrEqual(t, f.hits.Load(), int64(maxPages))
}

// A failing API keeps the last-good membership and is counted.
func TestFailureKeepsLastGoodAndRecovers(t *testing.T) {
	before := testutil.ToFloat64(
		metrics.DiscoveryRefreshErrors.WithLabelValues("test-gcp", "gcp"))
	f := newFakeCompute(t, pageWith("", instance("vm-1", statusRunning, "10.0.0.1", "")))
	p := fakeProvider(t, f.URL)
	got := make(chan discovery.Snapshot, 8)
	runner, err := p.newSubscription(&do.Query{Port: "9090"},
		func(s discovery.Snapshot) { got <- s })
	require.NoError(t, err)
	runner.Launch(t.Context())
	defer runner.Stop()

	select {
	case snap := <-got:
		require.Len(t, snap, 1)
		require.Equal(t, "10.0.0.1:9090", snap[0].Address)
	case <-time.After(10 * time.Second):
		t.Fatal("no initial snapshot")
	}

	f.setError(http.StatusForbidden, `{"error":{"message":"denied","status":"PERMISSION_DENIED"}}`)
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(
			metrics.DiscoveryRefreshErrors.WithLabelValues("test-gcp", "gcp")) > before
	}, 10*time.Second, 10*time.Millisecond)
	require.Empty(t, got, "a failing API must not replace the membership")
}

// A project that genuinely has no instances is a valid membership.
func TestAuthoritativeEmptyIsEmitted(t *testing.T) {
	f := newFakeCompute(t, `{"items":{}}`)
	s := fakeSubscription(t, f.URL, &do.Query{Port: "9090"})
	got, err := s.listInstances(t.Context())
	require.NoError(t, err)
	require.Empty(t, got)
	snap, skipped := toMembers(got, s.mapping)
	require.Empty(t, snap)
	require.Empty(t, skipped)
}

// A credentials file is parsed at use, and a bad one fails the poll rather
// than sending an unauthenticated request.
func TestCredentialsFile(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	good := filepath.Join(dir, "sa.json")
	require.NoError(t, os.WriteFile(good, serviceAccountJSON(t), 0o600))

	p, err := newProvider("test", &do.Options{
		Provider: "gcp",
		GCP:      &gcpopts.Options{Service: gcpopts.ServiceGCE, Project: "p", CredentialsFile: good},
	})
	require.NoError(t, err)
	creds, err := p.findCredentials(t.Context())
	require.NoError(t, err)
	require.NotNil(t, creds.TokenSource)

	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{not json"), 0o600))
	p.gcp.CredentialsFile = bad
	_, err = p.findCredentials(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "parsing gcp credentials_file")

	p.gcp.CredentialsFile = filepath.Join(dir, "absent.json")
	_, err = p.findCredentials(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "reading gcp credentials_file")
}

// The project comes from config, from the credentials, or from the metadata
// server. When none of them yields one, say so clearly rather than
// requesting "projects//instances".
func TestProjectResolution(t *testing.T) {
	isolate(t)
	p, err := newProvider("test", &do.Options{
		Provider: "gcp", GCP: &gcpopts.Options{Service: gcpopts.ServiceGCE, Project: "configured"}})
	require.NoError(t, err)
	got, err := p.projectID(t.Context())
	require.NoError(t, err)
	require.Equal(t, "configured", got)

	// a credentials file that carries a project supplies it
	dir := t.TempDir()
	f := filepath.Join(dir, "sa.json")
	require.NoError(t, os.WriteFile(f, serviceAccountJSON(t), 0o600))
	p, err = newProvider("test", &do.Options{
		Provider: "gcp", GCP: &gcpopts.Options{Service: gcpopts.ServiceGCE, CredentialsFile: f}})
	require.NoError(t, err)
	got, err = p.projectID(t.Context())
	require.NoError(t, err)
	require.Equal(t, "sa-project", got,
		"the project should come from the credentials when config omits it")

	// and with nothing anywhere, the error names the problem
	p, err = newProvider("test", &do.Options{Provider: "gcp", GCP: &gcpopts.Options{Service: gcpopts.ServiceGCE}})
	require.NoError(t, err)
	_, err = p.projectID(t.Context())
	require.Error(t, err)
}

// A momentary credential failure must not permanently disable the provider,
// so only successful resolutions are cached.
func TestFailedCredentialResolutionIsNotCached(t *testing.T) {
	isolate(t)
	p, err := newProvider("test", &do.Options{
		Provider: "gcp",
		GCP:      &gcpopts.Options{Service: gcpopts.ServiceGCE, Project: "p", CredentialsFile: "/nonexistent"},
	})
	require.NoError(t, err)
	_, err = p.tokenSource(t.Context())
	require.Error(t, err)
	p.mtx.Lock()
	cached := p.tokens
	p.mtx.Unlock()
	require.Nil(t, cached, "a failed resolve must not be cached")
}

// End to end through the poll loop.
func TestThroughThePollLoop(t *testing.T) {
	vm := instance("vm-1", statusRunning, "10.128.0.7", "")
	vm.Labels = map[string]string{"port": "8080"}
	f := newFakeCompute(t, pageWith("", vm))
	p := fakeProvider(t, f.URL)

	got := make(chan discovery.Snapshot, 4)
	runner, err := p.newSubscription(&do.Query{PortLabel: "port"},
		func(s discovery.Snapshot) { got <- s })
	require.NoError(t, err)
	runner.Launch(t.Context())
	defer runner.Stop()

	select {
	case snap := <-got:
		require.Len(t, snap, 1)
		require.Equal(t, "10.128.0.7:8080", snap[0].Address)
		require.Equal(t, discovery.Ready, snap[0].Ready)
	case <-time.After(10 * time.Second):
		t.Fatal("no snapshot")
	}
}

// serviceAccountJSON builds a syntactically real service account key, with
// a genuine RSA private key, so google.CredentialsFromJSON accepts it.
func serviceAccountJSON(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	b, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"project_id":   "sa-project",
		"private_key":  string(pemBytes),
		"client_email": "test@sa-project.iam.gserviceaccount.com",
		"token_uri":    "https://oauth2.googleapis.com/token",
	})
	require.NoError(t, err)
	return b
}

// credentials_file must be a service account key. An external_account or
// impersonated_service_account configuration can name an arbitrary token
// URL or local executable, so accepting whichever type the file declares
// would hand credential resolution somewhere unintended.
func TestCredentialsFileRejectsNonServiceAccountTypes(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	for _, credType := range []string{
		"external_account", "impersonated_service_account", "authorized_user",
	} {
		t.Run(credType, func(t *testing.T) {
			f := filepath.Join(dir, credType+".json")
			b, err := json.Marshal(map[string]string{
				"type":               credType,
				"audience":           "//iam.googleapis.com/locations/global/x",
				"token_url":          "https://attacker.example.com/token",
				"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
			})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(f, b, 0o600))

			p, perr := newProvider("test", &do.Options{
				Provider: "gcp",
				GCP:      &gcpopts.Options{Service: gcpopts.ServiceGCE, Project: "p", CredentialsFile: f},
			})
			require.NoError(t, perr)
			_, err = p.findCredentials(t.Context())
			require.Error(t, err, "%s must not be accepted", credType)
			require.Contains(t, err.Error(), "credential type")
		})
	}
}
