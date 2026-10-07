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

// Package resolver resolves SRV and A/AAAA records directly against one DNS server, which makes
// record TTLs visible, or through the stdlib resolver, which conveys none.
package resolver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	dnsclient "github.com/trickstercache/trickster/v2/pkg/dns/client"
)

// ErrNotFound is wrapped by lookup errors for a name that does not exist (NXDOMAIN)
var ErrNotFound = errors.New("dns name not found")

// Resolver resolves SRV and address records
type Resolver interface {
	// LookupSRV returns the SRV answer at fqdn
	LookupSRV(ctx context.Context, fqdn string) (*SRVAnswer, error)
	// LookupIP returns the A+AAAA answer at fqdn
	LookupIP(ctx context.Context, fqdn string) (IPAnswer, error)
}

// SRVAnswer is the result of an SRV lookup
type SRVAnswer struct {
	// Records is the answer section's SRV records
	Records []*dnsclient.SRV
	// TTL is the shortest record TTL in the answer; zero when the resolver conveys none
	TTL time.Duration
	// Additional maps a lowercase target FQDN to the addresses the server sent for it
	// in the additional section; it is nil when the server sent none
	Additional map[string]IPAnswer
}

// IPAnswer is the result of an address lookup
type IPAnswer struct {
	// Addrs is the A and AAAA addresses, as strings
	Addrs []string
	// TTL is the shortest record TTL in the answer; zero when the resolver conveys none
	TTL time.Duration
}

// New returns a direct resolver against server, else the first resolv.conf nameserver, else (e.g.
// on non-unix hosts) the stdlib resolver
func New(server string) Resolver {
	if server != "" {
		return NewDirect(server)
	}
	if rc, err := dnsclient.LoadResolvConf(
		dnsclient.DefaultResolvConfPath); err == nil {
		return NewDirect(rc.Servers[0])
	}
	return NewStd(nil)
}

// directResolver queries one DNS server directly, which is what makes record
// TTLs visible; the client retries over TCP when a UDP answer is truncated
type directResolver struct {
	server string
	client *dnsclient.Client
}

// NewDirect returns a Resolver that queries server (host:port) directly
func NewDirect(server string) Resolver {
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
	if r.RCode == dnsclient.RCodeNameError {
		return nil, fmt.Errorf("dns query for %s returned %s: %w", fqdn, r.RCode, ErrNotFound)
	}
	if r.RCode != dnsclient.RCodeSuccess {
		return nil, fmt.Errorf("dns query for %s returned %s", fqdn, r.RCode)
	}
	return r, nil
}

func (d *directResolver) LookupSRV(ctx context.Context, fqdn string) (*SRVAnswer, error) {
	r, err := d.query(ctx, fqdn, dnsclient.TypeSRV)
	if err != nil {
		return nil, err
	}
	out := &SRVAnswer{Records: make([]*dnsclient.SRV, 0, len(r.Answers))}
	for _, rr := range r.Answers {
		srv, ok := rr.(*dnsclient.SRV)
		if !ok {
			continue
		}
		out.Records = append(out.Records, srv)
		out.TTL = MinTTL(out.TTL, srv.Hdr.TTL)
	}
	for _, rr := range r.Additional {
		var addr string
		switch a := rr.(type) {
		case *dnsclient.A:
			addr = a.Addr.String()
		case *dnsclient.AAAA:
			addr = a.Addr.String()
		default:
			continue
		}
		if out.Additional == nil {
			out.Additional = make(map[string]IPAnswer, len(out.Records))
		}
		h := rr.Header()
		name := strings.ToLower(h.Name)
		ia := out.Additional[name]
		ia.Addrs = append(ia.Addrs, addr)
		ia.TTL = MinTTL(ia.TTL, h.TTL)
		out.Additional[name] = ia
	}
	return out, nil
}

var addrTypes = [2]dnsclient.Type{dnsclient.TypeA, dnsclient.TypeAAAA}

func (d *directResolver) LookupIP(ctx context.Context, fqdn string) (IPAnswer, error) {
	var out IPAnswer
	var lastErr error
	for _, qtype := range addrTypes {
		r, err := d.query(ctx, fqdn, qtype)
		if err != nil {
			lastErr = err
			continue
		}
		for _, rr := range r.Answers {
			switch a := rr.(type) {
			case *dnsclient.A:
				out.Addrs = append(out.Addrs, a.Addr.String())
				out.TTL = MinTTL(out.TTL, a.Hdr.TTL)
			case *dnsclient.AAAA:
				out.Addrs = append(out.Addrs, a.Addr.String())
				out.TTL = MinTTL(out.TTL, a.Hdr.TTL)
			}
		}
	}
	if len(out.Addrs) == 0 && lastErr != nil {
		return IPAnswer{}, lastErr
	}
	return out, nil
}

// MinTTL folds a record TTL into the running shortest-TTL value (0 means
// no TTL observed yet)
func MinTTL(current time.Duration, ttlSeconds uint32) time.Duration {
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

// NewStd returns a Resolver backed by r, or by net.DefaultResolver when r is nil
func NewStd(r *net.Resolver) Resolver {
	if r == nil {
		r = net.DefaultResolver
	}
	return &stdResolver{r: r}
}

func (s *stdResolver) LookupSRV(ctx context.Context, fqdn string) (*SRVAnswer, error) {
	_, addrs, err := s.r.LookupSRV(ctx, "", "", fqdn)
	if err != nil {
		return nil, stdError(err)
	}
	out := &SRVAnswer{Records: make([]*dnsclient.SRV, len(addrs))}
	for i, a := range addrs {
		out.Records[i] = &dnsclient.SRV{
			Target:   a.Target,
			Port:     a.Port,
			Priority: a.Priority,
			Weight:   a.Weight,
		}
	}
	return out, nil
}

func (s *stdResolver) LookupIP(ctx context.Context, fqdn string) (IPAnswer, error) {
	ips, err := s.r.LookupIP(ctx, "ip", fqdn)
	if err != nil {
		return IPAnswer{}, stdError(err)
	}
	out := IPAnswer{Addrs: make([]string, len(ips))}
	for i, ip := range ips {
		out.Addrs[i] = ip.String()
	}
	return out, nil
}

// stdError marks a stdlib not-found error with ErrNotFound; the stdlib reports
// an empty NOERROR answer the same way, so both read as not found
func stdError(err error) error {
	var de *net.DNSError
	if errors.As(err, &de) && de.IsNotFound {
		return fmt.Errorf("%w: %w", err, ErrNotFound)
	}
	return err
}
