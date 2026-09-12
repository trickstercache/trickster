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

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/daemon/setup"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
)

// generatedOverlay renders n controller-shaped backends: one reverse proxy per
// service with host routing, a prefix path, and a shared request rewriter.
func generatedOverlay(n int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "request_rewriters:\n  %sheaders:\n    instructions:\n", overlayTestPrefix)
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
`, overlayTestPrefix, i)
	}
	return sb.String()
}

func BenchmarkReloadOverlay(b *testing.B) {
	for _, n := range []int{100, 500, 2000} {
		b.Run("backends="+strconv.Itoa(n), func(b *testing.B) {
			dir := b.TempDir()
			path := filepath.Join(dir, "trickster.yaml")
			if err := os.WriteFile(path, []byte(reloadableConfig(0)), 0o600); err != nil {
				b.Fatal(err)
			}
			conf, clients, err := setup.BootstrapConfig("-config", path)
			if err != nil {
				b.Fatal(err)
			}
			group := listener.NewGroup()
			b.Cleanup(func() { _ = group.Shutdown(0) })
			si := &instance.ServerInstance{Listeners: group}
			if err := setup.ApplyConfig(si, conf, clients, nil, nil, group); err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if si.HealthChecker != nil {
					si.HealthChecker.Shutdown()
				}
			})
			stub := &overlayStub{}
			si.OverlayProvider = stub
			overlay := generatedOverlay(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				stub.set(overlay, strconv.Itoa(i))
				ok, err := Reload(si, "bench", "-config", path)
				if err != nil || !ok {
					b.Fatalf("reload %d = (%v, %v)", i, ok, err)
				}
			}
		})
	}
}
