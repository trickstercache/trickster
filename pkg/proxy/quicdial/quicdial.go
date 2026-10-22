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

// Package quicdial dials QUIC from a UDP socket of the destination's address family, since macOS can give the
// dual-stack socket of quic.DialAddr a port another process holds for IPv4, which then receives every reply
package quicdial

import (
	"context"
	"crypto/tls"
	"net"
	"net/netip"

	"github.com/quic-go/quic-go"
)

// DialAddr dials addr as quic.DialAddr does, from a socket of addr's address family
func DialAddr(ctx context.Context, addr string, tlsConf *tls.Config, conf *quic.Config) (*quic.Conn, error) {
	return dial(ctx, addr, tlsConf, conf, false)
}

// DialAddrEarly dials addr as quic.DialAddrEarly does, from a socket of addr's address family. It fits the dial
// hooks of http3.Transport and webtransport.Transport.
func DialAddrEarly(ctx context.Context, addr string, tlsConf *tls.Config, conf *quic.Config) (*quic.Conn, error) {
	return dial(ctx, addr, tlsConf, conf, true)
}

func dial(ctx context.Context, addr string, tlsConf *tls.Config, conf *quic.Config, early bool) (*quic.Conn, error) {
	raddr, host, err := resolve(ctx, addr)
	if err != nil {
		return nil, err
	}
	network := "udp6"
	if raddr.IP.To4() != nil {
		network = "udp4"
	}
	pc, err := net.ListenUDP(network, nil)
	if err != nil {
		return nil, err
	}
	// a Transport infers no server name from the address it is given, so it is set from the host, as quic.DialAddr
	// does
	tc := tlsConf.Clone()
	if tc == nil {
		tc = &tls.Config{}
	}
	if tc.ServerName == "" {
		tc.ServerName = host
	}
	tr := &quic.Transport{Conn: pc}
	var conn *quic.Conn
	if early {
		conn, err = tr.DialEarly(ctx, raddr, tc, conf)
	} else {
		conn, err = tr.Dial(ctx, raddr, tc, conf)
	}
	if err != nil {
		_ = tr.Close()
		_ = pc.Close()
		return nil, err
	}
	// the socket serves this connection alone, as quic.DialAddr's does
	go func() {
		<-conn.Context().Done()
		_ = tr.Close()
		_ = pc.Close()
	}()
	return conn, nil
}

func resolve(ctx context.Context, addr string) (*net.UDPAddr, string, error) {
	// resolves within ctx, preferring an IPv4 address as net.ResolveUDPAddr does
	host, service, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, "", err
	}
	port, err := net.DefaultResolver.LookupPort(ctx, "udp", service)
	if err != nil {
		return nil, "", err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, "", err
	}
	ip := ips[0]
	for _, a := range ips {
		if a.Unmap().Is4() {
			ip = a
			break
		}
	}
	return net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip.Unmap(), uint16(port))), host, nil // #nosec G115 -- a port
}
