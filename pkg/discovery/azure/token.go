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
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	azureopts "github.com/trickstercache/trickster/v2/pkg/discovery/azure/options"
)

// IMDS is the instance metadata service a managed identity authenticates
// against. The address is link-local and fixed by the platform.
const (
	// imdsEndpoint is plain http by design, not by oversight: the address
	// is link-local and never leaves the host, there is no name to put in
	// a certificate, and the platform serves no https listener there. The
	// Metadata header below is the actual anti-forgery control.
	//nolint:revive // unsecure-url-scheme: IMDS is link-local http by design
	imdsEndpoint   = "http://169.254.169.254/metadata/identity/oauth2/token"
	imdsAPIVersion = "2018-02-01"
)

// federatedAssertionType is the OAuth2 client-assertion type for a
// federated credential (AKS workload identity)
const federatedAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

// tokenExpiryGrace is how long before real expiry a cached token is
// treated as expired, so a token cannot lapse between the check and the
// request that uses it.
const tokenExpiryGrace = 2 * time.Minute

// maxTokenBytes bounds a token response
const maxTokenBytes = 1 << 20

// tokenResponse is the shape both the Entra ID and IMDS token endpoints
// return. expires_in is a JSON number from Entra ID and a quoted string
// from IMDS, which is why it is decoded permissively.
type tokenResponse struct {
	AccessToken string          `json:"access_token"`
	ExpiresIn   json.RawMessage `json:"expires_in"`
	Error       string          `json:"error"`
	Description string          `json:"error_description"`
}

// tokenSource acquires and caches an Entra ID access token for ARM.
type tokenSource struct {
	opts     *azureopts.Options
	client   *http.Client
	loginURL string
	// scope and resource are the same audience in the two spellings the
	// v2.0 endpoint and IMDS respectively expect
	scope    string
	resource string

	mtx     sync.Mutex
	token   string
	expires time.Time
}

// newTokenSource builds a token source for the configured credential.
func newTokenSource(o *azureopts.Options, client *http.Client) *tokenSource {
	mgmt := o.ManagementEndpoint()
	return &tokenSource{
		opts:     o,
		client:   client,
		loginURL: o.LoginEndpoint(),
		scope:    mgmt + "/.default",
		resource: mgmt + "/",
	}
}

// Token returns a valid access token, acquiring one if the cached token is
// absent or near expiry. Only successes are cached, so a transient failure
// is retried on the next poll rather than disabling the provider.
func (t *tokenSource) Token(ctx context.Context) (string, error) {
	t.mtx.Lock()
	if t.token != "" && time.Now().Before(t.expires) {
		tok := t.token
		t.mtx.Unlock()
		return tok, nil
	}
	t.mtx.Unlock()

	tok, ttl, err := t.acquire(ctx)
	if err != nil {
		return "", err
	}
	t.mtx.Lock()
	t.token = tok
	t.expires = time.Now().Add(ttl - tokenExpiryGrace)
	t.mtx.Unlock()
	return tok, nil
}

// acquire fetches a new token by whichever credential is configured.
//
// The order is deliberate and there is no chaining: a configured client
// credential that fails is an error, not a reason to silently fall back
// to the instance metadata service and authenticate as the wrong
// principal.
func (t *tokenSource) acquire(ctx context.Context) (string, time.Duration, error) {
	switch {
	case t.opts.ClientSecret != "":
		return t.clientCredentials(ctx, url.Values{
			"client_secret": {string(t.opts.ClientSecret)},
		})
	case t.opts.FederatedTokenFile != "":
		assertion, err := os.ReadFile(t.opts.FederatedTokenFile)
		if err != nil {
			return "", 0, fmt.Errorf(
				"reading azure federated_token_file: %w", err)
		}
		return t.clientCredentials(ctx, url.Values{
			"client_assertion_type": {federatedAssertionType},
			// re-read on every acquisition, so a token the platform
			// rotates is picked up without a restart
			"client_assertion": {strings.TrimSpace(string(assertion))},
		})
	default:
		return t.managedIdentity(ctx)
	}
}

// clientCredentials performs the OAuth2 client-credentials grant against
// the tenant's token endpoint.
func (t *tokenSource) clientCredentials(ctx context.Context,
	extra url.Values,
) (string, time.Duration, error) {
	form := url.Values{
		"grant_type": {"client_credentials"},
		"client_id":  {t.opts.ClientID},
		"scope":      {t.scope},
	}
	maps.Copy(form, extra)
	endpoint := t.loginURL + "/" + url.PathEscape(t.opts.TenantID) +
		"/oauth2/v2.0/token"
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return t.do(r, "entra id")
}

// managedIdentity fetches a token from the instance metadata service.
func (t *tokenSource) managedIdentity(ctx context.Context) (string, time.Duration, error) {
	v := url.Values{
		apiVersionParam: {imdsAPIVersion},
		"resource":      {t.resource},
	}
	if t.opts.ClientID != "" {
		// selects a user-assigned identity; without it the VM's
		// system-assigned identity is used
		v.Set("client_id", t.opts.ClientID)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet,
		imdsEndpoint+"?"+v.Encode(), nil)
	if err != nil {
		return "", 0, err
	}
	// IMDS refuses requests without this header, which is what stops a
	// confused-deputy fetch through a proxy
	r.Header.Set("Metadata", "true")
	return t.do(r, "instance metadata service")
}

// do performs a token request and decodes the response.
func (t *tokenSource) do(r *http.Request, who string) (string, time.Duration, error) {
	resp, err := t.client.Do(r)
	if err != nil {
		return "", 0, fmt.Errorf("azure token request to %s failed: %w", who, err)
	}
	defer resp.Body.Close()
	body, err := tbytes.ReadBoundedBody(resp.Body, maxTokenBytes, false)
	if err != nil {
		return "", 0, err
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", 0, fmt.Errorf(
			"azure token response from %s did not parse (http %d)",
			who, resp.StatusCode)
	}
	if tr.Error != "" {
		// the description names the actual problem (wrong secret, wrong
		// tenant, identity not assigned); the error code alone does not
		return "", 0, fmt.Errorf("azure token request to %s was refused: %s: %s",
			who, tr.Error, tr.Description)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("azure token request to %s returned http %d",
			who, resp.StatusCode)
	}
	if tr.AccessToken == "" {
		return "", 0, fmt.Errorf("azure token response from %s carried no token", who)
	}
	return tr.AccessToken, expiresIn(tr.ExpiresIn), nil
}

// expiresIn decodes the token lifetime, which Entra ID sends as a JSON
// number and IMDS as a quoted string. An unreadable or absent lifetime
// yields a short one rather than zero, so a token is still used but
// re-acquired promptly.
func expiresIn(raw json.RawMessage) time.Duration {
	const fallback = 5 * time.Minute
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" {
		return fallback
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return time.Duration(n) * time.Second
}
