/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

// Package portutil reserves free TCP ports for integration tests that need
// to write port numbers into config templates before booting trickster.
package portutil

import (
	"net"
	"testing"
)

// Reserve holds n ephemeral loopback TCP ports until release is called.
// Release immediately before the consumer binds the ports.
func Reserve(t testing.TB, n int) (ports []int, release func()) {
	t.Helper()
	return ReserveOn(t, n, "127.0.0.1")
}

// ReserveOn holds n ephemeral TCP ports on address until release is called.
// Use the consumer's bind address so its interface and address family are reserved.
func ReserveOn(t testing.TB, n int, address string) (ports []int, release func()) {
	t.Helper()
	ls := make([]net.Listener, n)
	ports = make([]int, n)
	for i := range n {
		l, err := net.Listen("tcp", net.JoinHostPort(address, "0"))
		if err != nil {
			for _, x := range ls[:i] {
				x.Close()
			}
			t.Fatalf("listen %s:0: %v", address, err)
		}
		ls[i] = l
		ports[i] = l.Addr().(*net.TCPAddr).Port
	}
	closed := false
	release = func() {
		if closed {
			return
		}
		closed = true
		for _, l := range ls {
			l.Close()
		}
	}
	t.Cleanup(release)
	return ports, release
}

// ReserveUDP holds n UDP ports open so a test can hand them to Trickster
// without another test claiming them first. UDP and TCP have separate port
// namespaces, so datagram listeners need their own reservations.
func ReserveUDP(t testing.TB, n int) (ports []int, release func()) {
	t.Helper()
	cs := make([]*net.UDPConn, n)
	ports = make([]int, n)
	for i := range n {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			for _, x := range cs[:i] {
				x.Close()
			}
			t.Fatalf("listen udp :0: %v", err)
		}
		cs[i] = c
		ports[i] = c.LocalAddr().(*net.UDPAddr).Port
	}
	closed := false
	release = func() {
		if closed {
			return
		}
		closed = true
		for _, c := range cs {
			c.Close()
		}
	}
	t.Cleanup(release)
	return ports, release
}
