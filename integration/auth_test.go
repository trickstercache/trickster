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

package integration

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestAuth_HtpasswdBasic(t *testing.T) {
	const (
		user = "test"
		pass = "password"
	)
	htpwPath := "testdata/configs/htpasswd"
	writeHtpasswd(t, htpwPath, user, pass)
	t.Cleanup(func() { _ = os.Remove(htpwPath) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", "testdata/configs/auth.yaml")

	const (
		frontAddr   = "127.0.0.1:8536"
		metricsAddr = "127.0.0.1:8537"
	)
	waitForTrickster(t, metricsAddr)

	promURL := "http://" + frontAddr + "/prom1/api/v1/query?" +
		url.Values{"query": {"up"}}.Encode()

	client := &http.Client{}

	req, err := http.NewRequest(http.MethodGet, promURL, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"expected 401 with no credentials")

	req, err = http.NewRequest(http.MethodGet, promURL, nil)
	require.NoError(t, err)
	req.SetBasicAuth(user, "not-the-password")
	resp, err = client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"expected 401 with wrong password")

	req, err = http.NewRequest(http.MethodGet, promURL, nil)
	require.NoError(t, err)
	token := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	req.Header.Set("Authorization", "Basic "+token)
	resp, err = client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"expected 200 with valid credentials")
}

func writeHtpasswd(t *testing.T, path, user, pass string) {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.MinCost)
	require.NoError(t, err)
	line := user + ":" + string(h) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(line), 0o600))
}
