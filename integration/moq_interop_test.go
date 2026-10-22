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
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
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
	"github.com/trickstercache/trickster/v2/pkg/proxy/quicdial"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

const (
	moqRelayAddr         = "127.0.0.1:10443"
	moqServerName        = "localhost"
	moqInteropEnv        = "TRICKSTER_MOQ_INTEROP_TEST"
	moqInteropHost       = "d%d.example.com"
	moqInteropBackend    = "interop%d"
	moqInteropKeySize    = 4096
	moqInteropDeltaSize  = 96
	moqInteropDeltaCount = 3
	moqCLIEnv            = "TRICKSTER_MOQ_CLI_TEST"
	moqCLIImage          = "moqdev/moq:0.14.5@sha256:991edc7bc70b96854730ddf0b1019f4e26e4a2ec0e983dd877acf3b1773c51bd"
	moqCLIBackend        = "moqcli"
	moqCLIWait           = time.Minute
	moqCLIPullWait       = 5 * time.Minute
)

func requireMoQRelay(t *testing.T) {
	t.Helper()
	try := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, err := quicdial.DialAddr(ctx, moqRelayAddr, moqPeerTLS(codec.ALPNFor(codec.ClientOffer[0])), nil)
		if err != nil {
			return err
		}
		return conn.CloseWithError(0, "")
	}
	err := try()
	if err != nil && os.Getenv(moqInteropEnv) == "1" {
		// the relay may still be starting in a run that requires it
		for deadline := time.Now().Add(15 * time.Second); err != nil && time.Now().Before(deadline); {
			time.Sleep(500 * time.Millisecond)
			err = try()
		}
		if err != nil {
			t.Fatalf("%s=1 but moq-relay is not answering at %s: %v", moqInteropEnv, moqRelayAddr, err)
		}
	}
	if err != nil {
		t.Skipf("moq-relay integration container not running at %s (run `make integration-start`); skipping",
			moqRelayAddr)
	}
}

func moqPeerTLS(alpns ...string) *tls.Config {
	// moq-relay generates its certificate when it starts and Trickster serves a test one, so neither verifies
	return &tls.Config{InsecureSkipVerify: true, ServerName: moqServerName, // #nosec G402 -- test certificates
		NextProtos: alpns, MinVersion: tls.VersionTLS13}
}

type interopPublisher struct {
	subs chan *session.Request
}

func (p *interopPublisher) HandleGoAway(*session.Session, *message.GoAway) {}

func (p *interopPublisher) HandleRequest(r *session.Request) {
	r.SetHandlers(moqNop{}, nil)
	if _, ok := r.Message().(*message.Subscribe); !ok {
		// moq-relay also asks publishers for their namespaces, and carries on without them when refused
		_ = r.Reject(message.RequestNotSupported, "", 0)
		return
	}
	if r.Send(&message.SubscribeOK{TrackAlias: r.Session().NewTrackAlias()}) == nil {
		p.subs <- r
	}
}

func (p *interopPublisher) next(t *testing.T) *session.Request {
	t.Helper()
	select {
	case r := <-p.subs:
		return r
	case <-time.After(moqWait):
		t.Fatal("the relay forwarded no SUBSCRIBE to the publisher")
		return nil
	}
}

func publishTo(t *testing.T, addr string, draft int, ns wire.Namespace) *interopPublisher {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), moqWait)
	defer cancel()
	conn, err := quicdial.DialAddr(ctx, addr, moqPeerTLS(codec.ALPNFor(draft)),
		&quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true})
	require.NoError(t, err)
	c, ok := codec.ForALPN(conn.ConnectionState().TLS.NegotiatedProtocol)
	require.True(t, ok)
	require.Equal(t, draft, c.Draft())
	p := &interopPublisher{subs: make(chan *session.Request, 4)}
	s, err := session.New(session.FromQUIC(conn), session.Options{
		Role: session.RoleClient, Codec: c, Handler: p,
		Setup: message.Setup{Path: "/", HasPath: true, Authority: moqServerName, HasAuthority: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close(message.CodeNoError, "") })
	require.NoError(t, s.Start(ctx))
	req, err := s.Open(context.Background(), &message.PublishNamespace{Prefix: ns}, moqNop{}, nil)
	require.NoError(t, err)
	require.Equal(t, message.KindRequestOK, moqResponse(t, req).Kind(), "the relay accepts the namespace")
	return p
}

func moqInteropHarness(t *testing.T, drafts []int) (tricksterHarness, string) {
	t.Helper()
	// one backend per upstream draft pulls from moq-relay, chosen by the session's authority
	serve, _ := moqServingTLS(t)
	serve.InsecureSkipVerify = true
	udp, releaseUDP := portutil.ReserveUDP(t, 1)
	h := configHarness(t, func(c *tkconfig.Config) {
		c.Listeners[moqListener] = &listenerconfig.Options{
			Protocol: listenerconfig.ProtocolMoQ, ListenAddress: "127.0.0.1", ListenPort: udp[0],
		}
		for _, d := range drafts {
			b := bo.New()
			b.Name, b.Provider, b.CacheName = fmt.Sprintf(moqInteropBackend, d), providers.MoQ, "default"
			b.OriginURL, b.Hosts = "moqt://"+moqRelayAddr+"/", []string{fmt.Sprintf(moqInteropHost, d)}
			b.ListenerNames, b.TLS = []string{moqListener}, serve
			b.MoQ = mo.New()
			b.MoQ.Upstream.Drafts = []int{d}
			// a group leaving the one-group live edge is dropped, so a fetch for it goes upstream
			b.MoQ.LiveEdge.Groups, b.MoQ.Spill = 1, false
			c.Backends[b.Name] = b
		}
	})
	releaseUDP()
	h.start(t)
	return h, fmt.Sprintf("127.0.0.1:%d", udp[0])
}

func randomGroup(t *testing.T) []string {
	t.Helper()
	out := make([]string, 0, moqInteropDeltaCount+1)
	for i := range moqInteropDeltaCount + 1 {
		b := make([]byte, moqInteropDeltaSize)
		if i == 0 {
			b = make([]byte, moqInteropKeySize)
		}
		_, err := rand.Read(b)
		require.NoError(t, err)
		out = append(out, string(b))
	}
	return out
}

type liveTrack struct {
	t        *testing.T
	r        *moqReceived
	upstream *session.Request
	groups   [][]string
}

func subscribeThrough(t *testing.T, sub *session.Session, pub *interopPublisher, ft message.FullTrackName) *liveTrack {
	t.Helper()
	r := newMoQReceived()
	req, err := sub.Open(context.Background(), &message.Subscribe{Track: ft}, r, r)
	require.NoError(t, err)
	require.Equal(t, message.KindSubscribeOK, moqResponse(t, req).Kind())
	return &liveTrack{t: t, r: r, upstream: pub.next(t)}
}

func (lt *liveTrack) publish() []string {
	lt.t.Helper()
	// a group of random objects reaches the subscriber intact before the next is published
	g := randomGroup(lt.t)
	publishGroup(lt.t, lt.upstream, uint64(len(lt.groups)), g...)
	for i, want := range g {
		require.Equal(lt.t, want, moqNext(lt.t, lt.r.objects, fmt.Sprintf("object %d of group %d", i, len(lt.groups))))
	}
	lt.groups = append(lt.groups, g)
	return g
}

func TestMoQInterop(t *testing.T) {
	requireMoQRelay(t)
	drafts := codec.Drafts()
	h, addr := moqInteropHarness(t, drafts)
	run := strconv.FormatInt(time.Now().UnixNano(), 36)

	for i, up := range drafts {
		// each upstream draft is read by a subscriber on another, so Trickster bridges the two
		down := drafts[(i+1)%len(drafts)]
		t.Run(fmt.Sprintf("upstream-%d-downstream-%d", up, down), func(t *testing.T) {
			ns := wire.Namespace{[]byte("interop"), []byte(run), []byte(strconv.Itoa(up))}
			ft := message.FullTrackName{Namespace: ns, Name: []byte(moqTrack)}
			pub := publishTo(t, moqRelayAddr, up, ns)
			sub, err := dialMoQWith(t, addr, fmt.Sprintf(moqInteropHost, up), codec.ALPNFor(down))
			require.NoError(t, err)
			lt := subscribeThrough(t, sub, pub, ft)
			group0 := lt.publish()
			lt.publish()
			// group 0 has left the live edge and nothing spilled it, so moq-relay answers the fetch
			backend := fmt.Sprintf(`backend_name="%s",source="upstream"`, fmt.Sprintf(moqInteropBackend, up))
			before, _ := metricValue(t, h.MetricsAddr, "trickster_moq_fetch_objects_total", backend)
			f := newMoQReceived()
			var p message.Params
			p.SetLocationFilter(message.LocationFilter{Type: message.FilterAbsoluteStartGroupEnd})
			req, err := sub.Open(context.Background(), &message.Fetch{Track: ft, Params: p}, f, f)
			require.NoError(t, err)
			fok, ok := moqResponse(t, req).(*message.FetchOK)
			require.True(t, ok, "the fetch is answered")
			require.Equal(t, uint64(0), fok.EndLocation.Group)
			for i, want := range group0 {
				require.Equal(t, want, moqNext(t, f.fetched, fmt.Sprintf("fetched object %d", i)))
			}
			after, _ := metricValue(t, h.MetricsAddr, "trickster_moq_fetch_objects_total", backend)
			require.Equal(t, float64(len(group0)), after-before, "the fetched objects came from moq-relay")
		})
	}
}

func moqCLIHarness(t *testing.T) (tricksterHarness, int) {
	t.Helper()
	serve, _ := moqServingTLS(t)
	udp, releaseUDP := portutil.ReserveUDP(t, 1)
	h := configHarness(t, func(c *tkconfig.Config) {
		// the CLI's container reaches the listener through the Docker host gateway
		c.Listeners[moqListener] = &listenerconfig.Options{
			Protocol: listenerconfig.ProtocolMoQ, ListenAddress: "0.0.0.0", ListenPort: udp[0],
		}
		b := bo.New()
		b.Name, b.Provider, b.CacheName = moqCLIBackend, providers.MoQ, "default"
		b.ListenerNames, b.TLS, b.MoQ = []string{moqListener}, serve, mo.New()
		b.MoQ.Publish.Enabled = true
		c.Backends[b.Name] = b
	})
	releaseUDP()
	h.start(t)
	return h, udp[0]
}

func moqCLI(ctx context.Context, port, draft int, ns wire.Namespace, args ...string) *exec.Cmd {
	full := append([]string{
		"run", "--rm", "--add-host=host.docker.internal:host-gateway", moqCLIImage,
		"--connect", fmt.Sprintf("moqt://host.docker.internal:%d/", port),
		"--connect-version", fmt.Sprintf("moq-transport-%d", draft),
		"--connect-tls-insecure", "--connect-once", "--connect-websocket-enabled=false",
		"--broadcast", string(bytes.Join(ns, []byte("/"))),
	}, args...)
	return exec.CommandContext(ctx, "docker", full...) // #nosec G204 -- fixed image and test-built arguments
}

func TestMoQInteropCLI(t *testing.T) {
	if os.Getenv(moqCLIEnv) == "" {
		t.Skipf("set %s=1 to run moq-dev's moq CLI against Trickster in Docker", moqCLIEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), moqCLIPullWait)
	defer cancel()
	if exec.CommandContext(ctx, "docker", "image", "inspect", moqCLIImage).Run() != nil {
		out, err := exec.CommandContext(ctx, "docker", "pull", moqCLIImage).CombinedOutput()
		require.NoError(t, err, "pulling the moq CLI: %s", out)
	}
	h, port := moqCLIHarness(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	subscribes := fmt.Sprintf(`backend_name="%s",result="ok",type="subscribe"`, moqCLIBackend)
	run := strconv.FormatInt(time.Now().UnixNano(), 36)

	for _, d := range codec.Drafts() {
		t.Run(strconv.Itoa(d), func(t *testing.T) {
			ns := wire.Namespace{[]byte("cli"), []byte(run), []byte(strconv.Itoa(d))}
			pub := publishTo(t, addr, d, ns)
			// a subscriber of Trickster's own holds the publisher's subscription open between CLI runs
			sub, err := dialMoQWith(t, addr, moqServerName, codec.ALPNFor(d))
			require.NoError(t, err)
			lt := subscribeThrough(t, sub, pub, message.FullTrackName{Namespace: ns, Name: []byte(moqTrack)})
			group0 := lt.publish()
			group1 := lt.publish()
			ctx, cancel := context.WithTimeout(context.Background(), moqCLIWait)
			defer cancel()
			fetched, err := moqCLI(ctx, port, d, ns, "fetch", "--group", "0", moqTrack).Output()
			require.NoError(t, err, "moq fetch --group 0: %s", stderrOf(err))
			require.Equal(t, strings.Join(group0, ""), string(fetched))

			// without --group the CLI prints the newest group: the current one, or the next once it is published
			before, _ := metricValue(t, h.MetricsAddr, "trickster_moq_requests_total", subscribes)
			var out, errOut bytes.Buffer
			cmd := moqCLI(ctx, port, d, ns, "fetch", moqTrack)
			cmd.Stdout, cmd.Stderr = &out, &errOut
			require.NoError(t, cmd.Start())
			require.Eventually(t, func() bool {
				n, _ := metricValue(t, h.MetricsAddr, "trickster_moq_requests_total", subscribes)
				return n > before
			}, moqCLIWait, 50*time.Millisecond, "the CLI subscribes")
			group2 := lt.publish()
			require.NoError(t, cmd.Wait(), "moq fetch: %s", errOut.String())
			require.Contains(t, []string{strings.Join(group1, ""), strings.Join(group2, "")}, out.String())
		})
	}
}

func stderrOf(err error) string {
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return string(ee.Stderr)
	}
	return fmt.Sprint(err)
}
