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

// Command h3-client issues an HTTP/3 request against a Trickster HTTP/3
// endpoint. Most systems ship a curl without HTTP/3 support, so this exists so
// contributors can exercise the QUIC listener without rebuilding curl.
//
// Usage:
//
//	go run ./hack/h3-client -url https://127.0.0.1:8443/ -ca /path/to/ca.pem
//	go run ./hack/h3-client -url https://127.0.0.1:8443/ -insecure
package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/quic-go/quic-go/http3"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	url := flag.String("url", "", "the https:// URL to request over HTTP/3 (required)")
	caPath := flag.String("ca", "", "path to a PEM CA bundle trusted for this request")
	insecure := flag.Bool("insecure", false, "skip certificate verification (development only)")
	method := flag.String("method", http.MethodGet, "HTTP method")
	timeout := flag.Duration("timeout", 30*time.Second, "overall request timeout")
	showBody := flag.Bool("body", true, "print the response body")
	flag.Parse()

	if *url == "" {
		flag.Usage()
		return errors.New("-url is required")
	}

	tlsConf := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: *insecure, // #nosec G402 -- development tool, opt-in via flag
	}
	if *caPath != "" {
		pem, err := os.ReadFile(*caPath)
		if err != nil {
			return fmt.Errorf("reading CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return fmt.Errorf("no certificates found in %s", *caPath)
		}
		tlsConf.RootCAs = pool
	}

	req, err := http.NewRequest(*method, *url, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}

	tr := &http3.Transport{TLSClientConfig: tlsConf}
	defer tr.Close()
	client := &http.Client{Transport: tr, Timeout: *timeout}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	fmt.Printf("%s %s (%s)\n", resp.Proto, resp.Status, time.Since(start).Round(time.Millisecond))
	for _, k := range []string{"Content-Type", "Content-Length", "Alt-Svc", "X-Trickster-Result"} {
		if v := resp.Header.Get(k); v != "" {
			fmt.Printf("%s: %s\n", k, v)
		}
	}
	if !*showBody {
		return nil
	}
	fmt.Println()
	if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
		return fmt.Errorf("reading body: %w", err)
	}
	return nil
}
