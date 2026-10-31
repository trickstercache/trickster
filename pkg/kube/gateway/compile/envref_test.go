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

package compile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	"github.com/stretchr/testify/require"
)

func TestCompiledHeadersDoNotExpandEnv(t *testing.T) {
	// header values come from Kubernetes objects, so a ${NAME} in one must reach the
	// loaded configuration literally rather than as the controller's environment
	const ref = "${TRK_TEST_KUBE_SECRET}"
	t.Setenv("TRK_TEST_KUBE_SECRET", "leaked")
	policy := policyShape()
	policy.Policies[0].RequestHeaders = map[string]string{"X-Policy": ref}
	policy.Policies[0].ResponseHeaders = map[string]string{"X-Policy-Response": ref}
	policy.Policies[0].CORSHeaders = map[string]string{"Access-Control-Allow-Origin": ref}
	filters := filterShape()
	filters.Routes[0].Rules[0].Filters[0].RequestHeaders.Set = []ir.Header{{Name: "X-A", Value: ref}}
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(loadableBaseConfig), 0o600))
	for name, test := range map[string]struct {
		model *ir.IR
		want  int
	}{
		"policy":  {model: policy, want: 3},
		"filters": {model: filters, want: 1},
	} {
		t.Run(name, func(t *testing.T) {
			o, err := Compile(test.model, serviceOpts(t))
			require.NoError(t, err)
			conf, err := config.LoadWithOverlay([]string{"-config", path}, o)
			require.NoError(t, err)
			seen := make(map[string]bool)
			check := func(kind string, m map[string]string) {
				for k, v := range m {
					require.NotContains(t, v, "leaked", k)
					if v == ref {
						seen[kind+k] = true
					}
				}
			}
			for _, b := range conf.Backends {
				for _, p := range b.Paths {
					check("request:", p.RequestHeaders)
					check("response:", p.ResponseHeaders)
					if p.CORS != nil {
						check("cors:", p.CORS.Headers)
					}
				}
			}
			require.Len(t, seen, test.want)
		})
	}
}
