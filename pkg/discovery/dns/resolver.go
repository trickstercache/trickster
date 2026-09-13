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

package dns

import (
	"context"
	"fmt"
	"net"
	"time"

	dnsclient "github.com/trickstercache/trickster/v2/pkg/dns/client"
)

// resolver abstracts record resolution so the provider can use a specific
// DNS server (with TTL access) or fall back to the system resolver
type resolver interface {
	// lookupSRV returns the SRV answer and its shortest TTL
	lookupSRV(ctx context.Context, fqdn string) ([]*dnsclient.SRV, time.Duration, error)
	// lookupIP returns the A+AAAA answer and its shortest TTL
	lookupIP(ctx context.Context, fqdn string) ([]string, time.Duration, error)
}

// newResolver returns a direct resolver against the configured server, or
// the first system nameserver from resolv.conf; when neither is available
// (e.g. non-unix hosts) it falls back to the stdlib resolver, which
// provides no TTLs, so the poll interval alone paces re-resolution
func newResolver(server string) (resolver, error) {
	if server != "" {
		return newDirectResolver(server), nil
	}
	if rc, err := dnsclient.LoadResolvConf(
		dnsclient.DefaultResolvConfPath); err == nil {
		return newDirectResolver(rc.Servers[0]), nil
	}
	return &stdResolver{r: net.DefaultResolver}, nil
}

// directResolver queries one DNS server directly, which is what makes record
// TTLs visible; the client retries over TCP when a UDP answer is truncated
type directResolver struct {
	server string
	client *dnsclient.Client
}

func newDirectResolver(server string) *directResolver {
	return &directResolver{
		server: server,
		client: &dnsclient.Client{Timeout: dnsclient.DefaultTimeout},
	}
}

func (d *directResolver) query(ctx context.Context, fqdn string,
	qtype dnsclient.Type,
) (*dnsclient.Msg, error) {
	r, err := d.client.Query(ctx, d.server, fqdn, qtype)
	if err != nil {
		return nil, err
	}
	if r.RCode != dnsclient.RCodeSuccess {
		return nil, fmt.Errorf("dns query for %s returned %s", fqdn, r.RCode)
	}
	return r, nil
}

func (d *directResolver) lookupSRV(ctx context.Context, fqdn string) ([]*dnsclient.SRV, time.Duration, error) {
	r, err := d.query(ctx, fqdn, dnsclient.TypeSRV)
	if err != nil {
		return nil, 0, err
	}
	out := make([]*dnsclient.SRV, 0, len(r.Answers))
	ttl := time.Duration(0)
	for _, rr := range r.Answers {
		srv, ok := rr.(*dnsclient.SRV)
		if !ok {
			continue
		}
		out = append(out, srv)
		ttl = minTTL(ttl, srv.Hdr.TTL)
	}
	return out, ttl, nil
}

func (d *directResolver) lookupIP(ctx context.Context, fqdn string) ([]string, time.Duration, error) {
	var out []string
	ttl := time.Duration(0)
	var lastErr error
	for _, qtype := range []dnsclient.Type{dnsclient.TypeA, dnsclient.TypeAAAA} {
		r, err := d.query(ctx, fqdn, qtype)
		if err != nil {
			lastErr = err
			continue
		}
		for _, rr := range r.Answers {
			switch a := rr.(type) {
			case *dnsclient.A:
				out = append(out, a.Addr.String())
				ttl = minTTL(ttl, a.Hdr.TTL)
			case *dnsclient.AAAA:
				out = append(out, a.Addr.String())
				ttl = minTTL(ttl, a.Hdr.TTL)
			}
		}
	}
	if len(out) == 0 && lastErr != nil {
		return nil, 0, lastErr
	}
	return out, ttl, nil
}

// minTTL folds a record TTL into the running shortest-TTL value (0 means
// no TTL observed yet)
func minTTL(current time.Duration, ttlSeconds uint32) time.Duration {
	t := time.Duration(ttlSeconds) * time.Second
	if current == 0 || t < current {
		return t
	}
	return current
}

// stdResolver uses the stdlib resolver; it conveys no TTLs
type stdResolver struct {
	r *net.Resolver
}

func (s *stdResolver) lookupSRV(ctx context.Context, fqdn string) ([]*dnsclient.SRV, time.Duration, error) {
	_, addrs, err := s.r.LookupSRV(ctx, "", "", fqdn)
	if err != nil {
		return nil, 0, err
	}
	out := make([]*dnsclient.SRV, len(addrs))
	for i, a := range addrs {
		out[i] = &dnsclient.SRV{
			Target:   a.Target,
			Port:     a.Port,
			Priority: a.Priority,
			Weight:   a.Weight,
		}
	}
	return out, 0, nil
}

func (s *stdResolver) lookupIP(ctx context.Context, fqdn string) ([]string, time.Duration, error) {
	ips, err := s.r.LookupIP(ctx, "ip", fqdn)
	if err != nil {
		return nil, 0, err
	}
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = ip.String()
	}
	return out, 0, nil
}
