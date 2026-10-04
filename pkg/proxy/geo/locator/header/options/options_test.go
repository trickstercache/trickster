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

	"github.com/stretchr/testify/require"
)

func TestOptions(t *testing.T) {
	o := New()
	require.ErrorIs(t, o.Validate(), ErrNoCountryHeader)
	o.Initialize()
	require.Equal(t, DefaultUnknownValues, o.UnknownValues)
	o.Country = "CF-IPCountry"
	require.NoError(t, o.Validate())
	o.Subdivision = "Bad Header"
	require.ErrorContains(t, o.Validate(), "'subdivision'")
	o.Subdivision = "CloudFront-Viewer-Country-Region"
	o.Continent = "bad:header"
	require.ErrorContains(t, o.Validate(), "'continent'")

	c := o.Clone()
	require.Equal(t, o, c)
	c.UnknownValues[0] = "changed"
	require.Equal(t, DefaultUnknownValues[0], o.UnknownValues[0])
	require.Nil(t, (*Options)(nil).Clone())

	none := &Options{UnknownValues: []string{}}
	none.Initialize()
	require.Empty(t, none.UnknownValues)
}
