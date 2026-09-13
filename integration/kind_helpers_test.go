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

package integration

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// the Gateway API controller scenarios' NodePorts on the host
const (
	kindContext        = "kind-trickster-it"
	kindNamespace      = "trickster-it"
	gatewayHTTPAddr    = "127.0.0.1:30085"
	gatewayHTTPSAddr   = "127.0.0.1:30086"
	gatewayMetricsAddr = "127.0.0.1:30087"
	gatewayIngressAddr = "127.0.0.1:30088"
)

func skipUnlessKind(t *testing.T) {
	t.Helper()
	if os.Getenv("TRICKSTER_KIND_TEST") != "1" {
		t.Skip("kind scenario runs only with TRICKSTER_KIND_TEST=1")
	}
}

// safe to call from a goroutine, since it fails nothing
func kubectlTry(stdin string, args ...string) (string, error) {
	cmd := exec.Command("kubectl", append([]string{
		"--context", kindContext, "-n", kindNamespace,
	}, args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("kubectl %v: %w: %s", args, err, out)
	}
	return string(out), nil
}

func kubectlKind(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	out, err := kubectlTry(stdin, args...)
	require.NoError(t, err)
	return out
}

func applyKind(t *testing.T, manifest string) {
	t.Helper()
	kubectlKind(t, manifest, "apply", "-f", "-")
}

func deleteKind(t *testing.T, manifest string) {
	t.Helper()
	kubectlKind(t, manifest, "delete", "--ignore-not-found", "--wait=false", "-f", "-")
}

// hostClient never follows a redirect, so a redirecting route is observed rather than chased
var hostClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func hostRequest(addr, host, path string, header http.Header) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		return nil, err
	}
	req.Host = host
	for k, v := range header {
		req.Header[k] = v
	}
	return req, nil
}

// hostGet returns the response with its body read and closed
func hostGet(t *testing.T, addr, host, path string, header ...http.Header) (*http.Response, string) {
	t.Helper()
	var h http.Header
	if len(header) > 0 {
		h = header[0]
	}
	req, err := hostRequest(addr, host, path, h)
	require.NoError(t, err)
	resp, err := hostClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(body)
}

func waitRoute(t *testing.T, addr, host, path string, status int, timeout time.Duration) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		req, err := hostRequest(addr, host, path, nil)
		if !assert.NoError(collect, err) {
			return
		}
		resp, err := hostClient.Do(req)
		if !assert.NoError(collect, err) {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		assert.Equal(collect, status, resp.StatusCode)
	}, timeout, 250*time.Millisecond, "%s%s on %s never answered %d", host, path, addr, status)
}

// loadLoop drives concurrent requests until ended, classifying failures so a zero-error
// assertion can say what broke
type loadLoop struct {
	stop     chan struct{}
	wg       sync.WaitGroup
	requests atomic.Int64
	errors   atomic.Int64
	failures *requestFailures
}

// a transport error, a status outside 2xx, or a body that ends early is an error
func startLoad(workers int, get func() (*http.Response, error)) *loadLoop {
	l := &loadLoop{stop: make(chan struct{}), failures: newRequestFailures()}
	for range workers {
		l.wg.Go(func() {
			for {
				select {
				case <-l.stop:
					return
				default:
				}
				resp, err := get()
				l.requests.Add(1)
				if err != nil {
					l.errors.Add(1)
					l.failures.transport(err)
					continue
				}
				good := resp.StatusCode >= 200 && resp.StatusCode <= 299
				if !good {
					l.errors.Add(1)
					l.failures.response(resp)
				}
				_, err = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if err != nil && good {
					l.errors.Add(1)
					l.failures.transport(fmt.Errorf("body: %w", err))
				}
			}
		})
	}
	return l
}

func (l *loadLoop) end() (requests, errors int64, failures *requestFailures) {
	close(l.stop)
	l.wg.Wait()
	return l.requests.Load(), l.errors.Load(), l.failures
}

// tlsSecretManifest renders a kubernetes.io/tls Secret with a fresh self-signed certificate
// for the hosts, and returns its serial number
func tlsSecretManifest(t *testing.T, name string, hosts ...string) (string, *big.Int) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hosts[0]},
		DNSNames:     hosts,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
type: kubernetes.io/tls
data:
  tls.crt: %s
  tls.key: %s
`, name, kindNamespace, base64.StdEncoding.EncodeToString(certPEM),
		base64.StdEncoding.EncodeToString(keyPEM)), serial
}

// servedSerial is the serial of the certificate presented for the server name, nil when the
// handshake fails
func servedSerial(addr, serverName string) *big.Int {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr,
		&tls.Config{ServerName: serverName, InsecureSkipVerify: true}) //nolint:gosec // the certificate is self-signed
	if err != nil {
		return nil
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil
	}
	return certs[0].SerialNumber
}

func waitServedSerial(t *testing.T, addr, serverName string, want *big.Int, timeout time.Duration) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		got := servedSerial(addr, serverName)
		if !assert.NotNil(collect, got, "no certificate served yet") {
			return
		}
		assert.Zero(collect, got.Cmp(want), "serving %s, want %s", got, want)
	}, timeout, 250*time.Millisecond, "%s never served certificate %s", serverName, want)
}

// stableMetric returns a counter once two readings a settle period apart agree, so a later
// delta is not from work in flight
func stableMetric(t *testing.T, metricsAddr, name string, settle time.Duration) float64 {
	t.Helper()
	var value float64
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		first, ok := metricValue(t, metricsAddr, name, "")
		if !assert.True(collect, ok, "%s not present", name) {
			return
		}
		time.Sleep(settle)
		second, ok := metricValue(t, metricsAddr, name, "")
		if !assert.True(collect, ok, "%s not present", name) {
			return
		}
		assert.Equal(collect, first, second, "%s still changing", name)
		value = second
	}, time.Minute, settle, "%s never settled", name)
	return value
}

// metricSample is one series of a metric family in a scrape
type metricSample struct {
	labels string
	value  float64
}

// metricSnapshot is one complete scrape, every family with every series it exposed
type metricSnapshot map[string][]metricSample

// scrapeMetrics reads the metrics endpoint once, so every value taken from the result is from
// the same instant; a failed or partial read is an error rather than a zero
func scrapeMetrics(metricsAddr string) (metricSnapshot, error) {
	resp, err := hostClient.Get("http://" + metricsAddr + "/metrics")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics endpoint answered %d", resp.StatusCode)
	}
	return parseMetrics(string(b)), nil
}

func parseMetrics(text string) metricSnapshot {
	out := make(metricSnapshot)
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		space := strings.LastIndexByte(line, ' ')
		if space < 0 {
			continue
		}
		v, err := strconv.ParseFloat(line[space+1:], 64)
		if err != nil {
			continue
		}
		series, name := line[:space], line[:space]
		var labels string
		if brace := strings.IndexByte(series, '{'); brace >= 0 {
			name, labels = series[:brace], series[brace:]
		}
		out[name] = append(out[name], metricSample{labels: labels, value: v})
	}
	return out
}

// sum adds every series of a family; a family the scrape did not expose is absent, which for
// a counter created on first use means it has never been incremented
func (m metricSnapshot) sum(name string) (float64, bool) {
	samples, ok := m[name]
	if !ok {
		return 0, false
	}
	var total float64
	for _, s := range samples {
		total += s.value
	}
	return total, true
}

// value is the first series of a family whose labels contain the fragment
func (m metricSnapshot) value(name, labelFragment string) (float64, bool) {
	for _, s := range m[name] {
		if labelFragment == "" || strings.Contains(s.labels, labelFragment) {
			return s.value, true
		}
	}
	return 0, false
}
