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

package registry

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"

	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	_, err := New(nil)
	require.Error(t, err)

	// kubernetes is registered; constructing without connection options
	// surfaces the provider's own error rather than a not-registered error
	_, err = New(&options.Options{Name: "d", Provider: providers.Kubernetes})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "no discoverer implementation registered")

	// dns providers require a dns options block from their constructor
	_, err = New(&options.Options{Name: "d", Provider: providers.DNSSRV})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no dns options")

	// the file provider has no connection options and constructs directly
	d, err := New(&options.Options{Name: "d", Provider: providers.File})
	require.NoError(t, err)
	require.NotNil(t, d)

	// names with no registered implementation return a clear error rather
	// than a nil Discoverer
	_, err = New(&options.Options{Name: "d", Provider: "consul"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no discoverer implementation registered")
}
