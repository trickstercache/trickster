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

package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
)

const benchmarkListeners = `
listeners:
  default:
    port: 0
  mgmt:
    port: 0
  metrics:
    port: 0
`

const benchmarkBaseBackend = `
backends:
  base:
    provider: rp
    origin_url: http://127.0.0.1:1
`

func generatedBackends(prefix string, n int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "request_rewriters:\n  %sheaders:\n    instructions:\n", prefix)
	sb.WriteString("      - [header, set, X-Forwarded-Proto, https]\nbackends:\n")
	for i := range n {
		fmt.Fprintf(&sb, `  %[1]ssvc-%[2]d:
    provider: rp
    origin_url: http://127.0.0.1:1/svc-%[2]d
    path_routing_disabled: true
    req_rewriter_name: %[1]sheaders
    hosts:
      - svc-%[2]d.example.com
    paths:
      - path: /
        match_type: prefix
        handler: proxy
`, prefix, i)
	}
	return sb.String()
}

func BenchmarkBootstrapConfig(b *testing.B) {
	for _, n := range []int{100, 500, 2000} {
		b.Run("backends="+strconv.Itoa(n), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "trickster.yaml")
			body := benchmarkListeners + generatedBackends("gen-", n)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := BootstrapConfig("-config", path); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkBootstrapConfigWithOverlay(b *testing.B) {
	for _, n := range []int{100, 500, 2000} {
		b.Run("backends="+strconv.Itoa(n), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "trickster.yaml")
			if err := os.WriteFile(path, []byte(benchmarkListeners+benchmarkBaseBackend), 0o600); err != nil {
				b.Fatal(err)
			}
			overlay := &config.Overlay{
				Data:    []byte(generatedBackends(reserved.NamePrefixKubeGateway, n)),
				Prefix:  reserved.NamePrefixKubeGateway,
				Version: "bench",
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := BootstrapConfigWithOverlay(overlay, "-config", path); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
