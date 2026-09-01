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

package azure

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	azureopts "github.com/trickstercache/trickster/v2/pkg/discovery/azure/options"

	"github.com/stretchr/testify/require"
)

// fakeLogin records the form a token request carried
type fakeLogin struct {
	*httptest.Server
	forms  chan url.Values
	body   string
	status int
	calls  int
}

func newFakeLogin(t *testing.T, body string) *fakeLogin {
	f := &fakeLogin{forms: make(chan url.Values, 8), body: body, status: 200}
	f.Server = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			form := r.Form
			// IMDS carries its parameters in the query string
			for k, v := range r.URL.Query() {
				form[k] = v
			}
			f.calls++
			select {
			case f.forms <- form:
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(f.body))
		}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeLogin) nextForm(t *testing.T) url.Values {
	t.Helper()
	select {
	case v := <-f.forms:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a token request")
		return nil
	}
}

func goodToken(expiresIn string) string {
	return `{"access_token":"tok-abc","expires_in":` + expiresIn + `}`
}

// tokenSourceFor builds a source whose login endpoint is the fake
func tokenSourceFor(o *azureopts.Options, loginURL string) *tokenSource {
	ts := newTokenSource(o, http.DefaultClient)
	ts.loginURL = loginURL
	return ts
}

func TestClientSecretGrant(t *testing.T) {
	f := newFakeLogin(t, goodToken("3599"))
	ts := tokenSourceFor(&azureopts.Options{
		TenantID: "tenant-1", ClientID: "client-1", ClientSecret: "shh",
	}, f.URL)

	tok, err := ts.Token(t.Context())
	require.NoError(t, err)
	require.Equal(t, "tok-abc", tok)

	form := f.nextForm(t)
	require.Equal(t, "client_credentials", form.Get("grant_type"))
	require.Equal(t, "client-1", form.Get("client_id"))
	require.Equal(t, "shh", form.Get("client_secret"))
	require.Equal(t, "https://management.azure.com/.default", form.Get("scope"))
}

// AKS workload identity: the platform writes and rotates a token file, so
// Trickster holds no secret of its own.
func TestFederatedTokenFileGrant(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(path, []byte("  assertion-1\n"), 0o600))

	f := newFakeLogin(t, goodToken("3599"))
	ts := tokenSourceFor(&azureopts.Options{
		TenantID: "tenant-1", ClientID: "client-1", FederatedTokenFile: path,
	}, f.URL)

	_, err := ts.Token(t.Context())
	require.NoError(t, err)
	form := f.nextForm(t)
	require.Equal(t, federatedAssertionType, form.Get("client_assertion_type"))
	require.Equal(t, "assertion-1", form.Get("client_assertion"),
		"the file's surrounding whitespace must not be sent")
	require.Empty(t, form.Get("client_secret"))
}

// The platform rotates the file, so it must be re-read on every
// acquisition rather than cached at construction.
func TestFederatedTokenFileIsRereadOnEachAcquisition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(path, []byte("first"), 0o600))

	f := newFakeLogin(t, goodToken("3599"))
	ts := tokenSourceFor(&azureopts.Options{
		TenantID: "t", ClientID: "c", FederatedTokenFile: path,
	}, f.URL)

	_, err := ts.Token(t.Context())
	require.NoError(t, err)
	require.Equal(t, "first", f.nextForm(t).Get("client_assertion"))

	require.NoError(t, os.WriteFile(path, []byte("second"), 0o600))
	// force re-acquisition rather than waiting out the cached lifetime
	ts.expires = time.Now().Add(-time.Second)
	_, err = ts.Token(t.Context())
	require.NoError(t, err)
	require.Equal(t, "second", f.nextForm(t).Get("client_assertion"))
}

func TestMissingFederatedTokenFileIsAnError(t *testing.T) {
	f := newFakeLogin(t, goodToken("3599"))
	ts := tokenSourceFor(&azureopts.Options{
		TenantID: "t", ClientID: "c",
		FederatedTokenFile: "/nonexistent/token",
	}, f.URL)
	_, err := ts.Token(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "federated_token_file")
}

// A refused grant must carry the description: the error code alone does
// not say whether the secret, the tenant or the assignment is wrong.
func TestRefusedGrantCarriesTheDescription(t *testing.T) {
	f := newFakeLogin(t,
		`{"error":"invalid_client","error_description":"AADSTS7000215: Invalid client secret"}`)
	f.status = http.StatusUnauthorized
	ts := tokenSourceFor(&azureopts.Options{
		TenantID: "t", ClientID: "c", ClientSecret: "wrong",
	}, f.URL)
	_, err := ts.Token(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_client")
	require.Contains(t, err.Error(), "Invalid client secret")
}

// A token is cached until near expiry, so a poll loop does not re-acquire
// on every refresh.
func TestTokenIsCachedUntilNearExpiry(t *testing.T) {
	f := newFakeLogin(t, goodToken("3599"))
	ts := tokenSourceFor(&azureopts.Options{
		TenantID: "t", ClientID: "c", ClientSecret: "s"}, f.URL)

	for range 3 {
		_, err := ts.Token(t.Context())
		require.NoError(t, err)
	}
	require.Equal(t, 1, f.calls, "one acquisition serves every poll until expiry")
}

// Only successes are cached, so a transient failure is retried on the
// next poll rather than disabling the provider.
func TestFailedAcquisitionIsNotCached(t *testing.T) {
	f := newFakeLogin(t, `{"error":"temporarily_unavailable","error_description":"try again"}`)
	f.status = http.StatusServiceUnavailable
	ts := tokenSourceFor(&azureopts.Options{
		TenantID: "t", ClientID: "c", ClientSecret: "s"}, f.URL)

	_, err := ts.Token(t.Context())
	require.Error(t, err)
	require.Empty(t, ts.token)

	f.status = http.StatusOK
	f.body = goodToken("3599")
	tok, err := ts.Token(t.Context())
	require.NoError(t, err)
	require.Equal(t, "tok-abc", tok)
}

// A cached token is treated as expired early, so it cannot lapse between
// the check and the request that uses it.
func TestExpiryHasAGrace(t *testing.T) {
	f := newFakeLogin(t, goodToken("3599"))
	ts := tokenSourceFor(&azureopts.Options{
		TenantID: "t", ClientID: "c", ClientSecret: "s"}, f.URL)
	_, err := ts.Token(t.Context())
	require.NoError(t, err)
	require.True(t, ts.expires.Before(time.Now().Add(3599*time.Second)),
		"the cached lifetime must be shorter than the token's own")
}

// IMDS sends expires_in as a quoted string where Entra ID sends a number;
// both must decode, and an unusable value must not yield a zero lifetime
// that would re-acquire on every single call.
func TestExpiresInAcceptsBothSpellings(t *testing.T) {
	require.Equal(t, 3599*time.Second, expiresIn(json.RawMessage(`3599`)))
	require.Equal(t, 3599*time.Second, expiresIn(json.RawMessage(`"3599"`)))
	for _, bad := range []string{``, `""`, `"abc"`, `0`, `-1`, `null`} {
		require.Equal(t, 5*time.Minute, expiresIn(json.RawMessage(bad)),
			"unusable lifetime %q must fall back, not become zero", bad)
	}
}

// A configured client credential that fails is an error, not a reason to
// fall back to the instance metadata service -- that would silently
// authenticate as the wrong principal.
func TestNoSilentFallbackToManagedIdentity(t *testing.T) {
	f := newFakeLogin(t, `{"error":"invalid_client","error_description":"bad"}`)
	f.status = http.StatusUnauthorized
	ts := tokenSourceFor(&azureopts.Options{
		TenantID: "t", ClientID: "c", ClientSecret: "wrong"}, f.URL)

	_, err := ts.Token(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "entra id",
		"the failure must name the credential that was configured")
	require.NotContains(t, err.Error(), "instance metadata")
}

// Sovereign clouds use different endpoints for both ARM and login;
// selecting the cloud must set both, since keeping two URLs consistent by
// hand is exactly the kind of thing that silently half-works.
func TestCloudSelectsBothEndpoints(t *testing.T) {
	for _, tc := range []struct{ cloud, mgmt, login string }{
		{azureopts.CloudPublic,
			"https://management.azure.com", "https://login.microsoftonline.com"},
		{azureopts.CloudUSGovernment,
			"https://management.usgovcloudapi.net", "https://login.microsoftonline.us"},
		{azureopts.CloudChina,
			"https://management.chinacloudapi.cn", "https://login.chinacloudapi.cn"},
	} {
		t.Run(tc.cloud, func(t *testing.T) {
			o := &azureopts.Options{Cloud: tc.cloud}
			require.Equal(t, tc.mgmt, o.ManagementEndpoint())
			require.Equal(t, tc.login, o.LoginEndpoint())
			ts := newTokenSource(o, http.DefaultClient)
			require.Equal(t, tc.mgmt+"/.default", ts.scope)
			require.Equal(t, tc.mgmt+"/", ts.resource,
				"IMDS wants the resource with a trailing slash, not the .default scope")
		})
	}
}
