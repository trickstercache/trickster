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

package integration

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"

	"github.com/stretchr/testify/require"
)

const (
	moqExportBackend = "moqpull"
	moqExportRun     = 30 * time.Second
	moqExportRead    = 12 * time.Second
	moqDiscoveryRoot = "discovery"
)

type namespaceEvents struct {
	got chan message.Message
}

func (w *namespaceEvents) HandleMessage(_ *session.Request, m message.Message) { w.got <- m }
func (w *namespaceEvents) HandleClose(*session.Request, error)                 {}

func (w *namespaceEvents) next(t *testing.T, what string) message.Message {
	t.Helper()
	select {
	case m := <-w.got:
		return m
	case <-time.After(moqWait):
		t.Fatalf("no %s through Trickster", what)
		return nil
	}
}

func TestMoQNamespaceDiscovery(t *testing.T) {
	// a broadcast published into moq-relay is reported to a SUBSCRIBE_NAMESPACE sent to Trickster in front of it,
	// and withdrawn when its publisher leaves: the discovery a moq-dev player depends on
	requireMoQRelay(t)
	drafts := codec.Drafts()
	_, addr := moqInteropHarness(t, drafts)
	run := strconv.FormatInt(time.Now().UnixNano(), 36)

	for _, d := range drafts {
		t.Run(strconv.Itoa(d), func(t *testing.T) {
			prefix := wire.Namespace{[]byte(moqDiscoveryRoot), []byte(run)}
			sub, err := dialMoQWith(t, addr, fmt.Sprintf(moqInteropHost, d), codec.ALPNFor(d))
			require.NoError(t, err)
			w := &namespaceEvents{got: make(chan message.Message, 16)}
			req, err := sub.Open(context.Background(), &message.SubscribeNamespace{
				Prefix: prefix, Options: message.SubscribeOptionNamespace,
			}, w, nil)
			require.NoError(t, err)
			require.Equal(t, message.KindRequestOK, moqResponse(t, req).Kind(), "SUBSCRIBE_NAMESPACE is accepted")

			// the publisher leaves when its subtest ends, which withdraws its namespace
			name := []byte(strconv.Itoa(d))
			t.Run("announced", func(t *testing.T) {
				publishTo(t, moqRelayAddr, d, append(prefix, name))
				n, ok := w.next(t, "NAMESPACE").(*message.Namespace)
				require.True(t, ok, "a NAMESPACE reports the broadcast")
				require.Equal(t, wire.Namespace{name}, n.Suffix)
			})
			done, ok := w.next(t, "NAMESPACE_DONE").(*message.NamespaceDone)
			require.True(t, ok, "a NAMESPACE_DONE withdraws the broadcast")
			require.Equal(t, wire.Namespace{name}, done.Suffix)
		})
	}
}

func moqPullCLIHarness(t *testing.T) int {
	t.Helper()
	// one pull-through backend in front of moq-relay, on a listener the CLI's container reaches through the
	// Docker host gateway; as the listener's only backend, it serves every session
	serve, _ := moqServingTLS(t)
	serve.InsecureSkipVerify = true
	udp, releaseUDP := portutil.ReserveUDP(t, 1)
	h := configHarness(t, func(c *tkconfig.Config) {
		c.Listeners[moqListener] = &listenerconfig.Options{
			Protocol: listenerconfig.ProtocolMoQ, ListenAddress: "0.0.0.0", ListenPort: udp[0],
		}
		b := bo.New()
		b.Name, b.Provider, b.CacheName = moqExportBackend, providers.MoQ, "default"
		b.OriginURL = "moqt://" + moqRelayAddr + "/"
		b.ListenerNames, b.TLS, b.MoQ = []string{moqListener}, serve, mo.New()
		c.Backends[b.Name] = b
	})
	releaseUDP()
	h.start(t)
	return udp[0]
}

func dockerCLI(name, connect string, args ...string) []string {
	return append([]string{
		"run", "--rm", "-i", "--name", name, "--add-host=host.docker.internal:host-gateway", moqCLIImage,
		"--connect", connect, "--connect-tls-insecure", "--connect-websocket-enabled=false",
	}, args...)
}

func TestMoQExportThroughRelayCLI(t *testing.T) {
	// moq-dev's CLI imports a live fMP4 into moq-relay, and its exporter, which finds the broadcast only by
	// SUBSCRIBE_NAMESPACE, reads it back through Trickster
	if os.Getenv(moqCLIEnv) == "" {
		t.Skipf("set %s=1 to run moq-dev's moq CLI against Trickster in Docker", moqCLIEnv)
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	requireMoQRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), moqCLIPullWait)
	defer cancel()
	if exec.CommandContext(ctx, "docker", "image", "inspect", moqCLIImage).Run() != nil {
		out, err := exec.CommandContext(ctx, "docker", "pull", moqCLIImage).CombinedOutput()
		require.NoError(t, err, "pulling the moq CLI: %s", out)
	}
	port := moqPullCLIHarness(t)
	run := strconv.FormatInt(time.Now().UnixNano(), 36)
	broadcast := "export/" + run + ".hang"
	importer, exporter := "trickster-it-import-"+run, "trickster-it-export-"+run
	t.Cleanup(func() {
		// a killed docker client leaves its container running
		_ = exec.Command("docker", "rm", "-f", importer, exporter).Run() // #nosec G204 -- test-built names
	})

	// a live test pattern and tone, in real time, fragmented at every keyframe as the importer takes it
	runCtx, stop := context.WithTimeout(context.Background(), moqExportRun)
	defer stop()
	src := exec.CommandContext(runCtx, ffmpeg, "-hide_banner", "-loglevel", "error", "-re", // #nosec G204 -- fixed
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=15", "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-g", "15", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-f", "mp4", "-movflags", "empty_moov+default_base_moof+frag_keyframe", "pipe:1")
	imp := exec.CommandContext(runCtx, "docker", dockerCLI(importer, // #nosec G204 -- fixed image and test-built arguments
		"moqt://host.docker.internal:"+moqRelayPort()+"/", "--broadcast", broadcast, "import", "fmp4")...)
	pipe, err := src.StdoutPipe()
	require.NoError(t, err)
	imp.Stdin = pipe
	var impErr bytes.Buffer
	imp.Stderr = &impErr
	require.NoError(t, src.Start())
	require.NoError(t, imp.Start())
	t.Cleanup(func() {
		stop()
		_ = src.Wait()
		_ = imp.Wait()
	})

	// the exporter streams until it is stopped; what it wrote by then is a fragmented MP4 of the broadcast
	readCtx, done := context.WithTimeout(context.Background(), moqExportRead)
	defer done()
	exp := exec.CommandContext(readCtx, "docker", dockerCLI(exporter, // #nosec G204 -- fixed image and test-built arguments
		fmt.Sprintf("moqt://host.docker.internal:%d/", port), "--broadcast", broadcast, "export", "fmp4")...)
	var out, expErr bytes.Buffer
	exp.Stdout, exp.Stderr = &out, &expErr
	_ = exp.Run()
	_ = exec.Command("docker", "rm", "-f", exporter).Run() // #nosec G204 -- test-built name
	require.True(t, bytes.Contains(out.Bytes(), []byte("moov")) && bytes.Contains(out.Bytes(), []byte("moof")),
		"the export through Trickster carries the broadcast (%d bytes)\nexport: %s\nimport: %s", out.Len(),
		expErr.String(), impErr.String())
	probe := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", "pipe:0")
	probe.Stdin = bytes.NewReader(out.Bytes())
	streams, err := probe.Output()
	require.NoError(t, err, "ffprobe reads the export")
	require.Contains(t, string(streams), "h264")
	require.Contains(t, string(streams), "aac")
}

func moqRelayPort() string {
	_, port, _ := net.SplitHostPort(moqRelayAddr)
	return port
}
