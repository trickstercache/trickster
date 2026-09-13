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

	"github.com/stretchr/testify/require"
)

func TestInstructionConstructors(t *testing.T) {
	require.Equal(t, []string{"path", "set", "/v2"}, PathSet("/v2"))
	require.Equal(t, []string{"path", "prefix-replace", "/v1", "/v2"}, PathPrefixReplace("/v1", "/v2"))
	require.Equal(t, []string{"scheme", "set", "https"}, SchemeSet("https"))
	require.Equal(t, []string{"hostname", "set", "h"}, HostnameSet("h"))
	require.Equal(t, []string{"port", "set", "443"}, PortSet("443"))
	require.Equal(t, []string{"header", "set", "X-A", "v"}, HeaderSet("X-A", "v"))
	require.Equal(t, []string{"header", "append", "X-A", "v"}, HeaderAppend("X-A", "v"))
	require.Equal(t, []string{"header", "delete", "X-A"}, HeaderDelete("X-A"))
}
