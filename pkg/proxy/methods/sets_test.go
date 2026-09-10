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

package methods

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExpand(t *testing.T) {
	all := AllHTTPMethods()
	require.Equal(t, all, Expand(nil))
	require.Equal(t, all, Expand([]string{http.MethodGet, Wildcard}))
	require.Equal(t, []string{http.MethodGet, http.MethodPost}, Expand([]string{"get", "Post"}))
	// an already concrete, upper-cased list is returned as is
	in := []string{http.MethodGet, http.MethodPost}
	out := Expand(in)
	require.Equal(t, in, out)
	require.Same(t, &in[0], &out[0])
}

func TestCompact(t *testing.T) {
	require.Equal(t, []string{Wildcard}, Compact(AllHTTPMethods()))
	require.Equal(t, []string{Wildcard}, Compact(append(AllHTTPMethods(), http.MethodGet)))
	require.Equal(t, []string{http.MethodGet, http.MethodPost},
		Compact([]string{http.MethodPost, http.MethodGet}))
	require.Empty(t, Compact(nil))
	in := []string{http.MethodPost, http.MethodGet}
	Compact(in)
	require.Equal(t, []string{http.MethodPost, http.MethodGet}, in, "input is not sorted in place")
}

func TestPartition(t *testing.T) {
	cacheable, rest := Partition(
		[]string{http.MethodGet, http.MethodPost, http.MethodHead}, IsCacheable)
	require.Equal(t, []string{http.MethodGet, http.MethodHead}, cacheable)
	require.Equal(t, []string{http.MethodPost}, rest)

	// a method the proxy cannot classify must not be assumed cacheable
	cacheable, rest = Partition([]string{"FROBNICATE"}, IsCacheable)
	require.Nil(t, cacheable)
	require.Equal(t, []string{"FROBNICATE"}, rest)

	cacheable, rest = Partition(nil, IsCacheable)
	require.Nil(t, cacheable)
	require.Nil(t, rest)
}
