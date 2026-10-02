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

func TestValidateFlavor(t *testing.T) {
	var o *Options
	require.NoError(t, o.Validate())
	require.NoError(t, (&Options{}).Validate())
	for _, f := range Flavors() {
		require.NoError(t, (&Options{Flavor: f}).Validate())
	}
	require.ErrorIs(t, (&Options{Flavor: "CloudWatch"}).Validate(), ErrUnknownFlavor)
}

func TestCloneKeepsFlavor(t *testing.T) {
	require.Equal(t, FlavorAMP, (&Options{Flavor: FlavorAMP}).Clone().Flavor)
}

func TestOriginRegion(t *testing.T) {
	tests := []struct {
		flavor, origin, want string
	}{
		{FlavorCloudWatch, CloudWatchOriginURL("eu-west-1"), "eu-west-1"},
		{FlavorCloudWatch, "https://MONITORING.us-east-2.amazonaws.com:443/", "us-east-2"},
		{FlavorCloudWatch, "https://proxy.example.com", ""},
		{FlavorCloudWatch, "https://monitoring.amazonaws.com", ""},
		{FlavorCloudWatch, "https://monitoring.a.b.amazonaws.com", ""},
		{FlavorAMP, "https://aps-workspaces.ap-south-1.amazonaws.com/workspaces/ws-1", "ap-south-1"},
		{FlavorAMP, CloudWatchOriginURL("eu-west-1"), ""},
		{"", CloudWatchOriginURL("eu-west-1"), ""},
	}
	for _, tc := range tests {
		require.Equal(t, tc.want, OriginRegion(tc.flavor, tc.origin), tc.origin)
	}
}
