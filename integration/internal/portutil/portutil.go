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

// Reserve binds n ephemeral TCP ports and returns the port numbers plus a
// release function. The listeners stay open until the caller invokes release,
// so the caller can write the ports into a config template and only release
// immediately before whatever consumer (e.g. trickster) needs to bind them.
// This minimizes the close-to-bind window where another process may grab the
// freed port out from under the consumer.
//
// The caller MUST call release() exactly once, just before passing the ports
// to the consumer. The returned function is idempotent and safe to call from
// t.Cleanup as a backstop.
func Reserve(t testing.TB, n int) (ports []int, release func()) {
	t.Helper()
	ls := make([]*net.TCPListener, n)
	ports = make([]int, n)
	for i := range n {
		l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			for _, x := range ls[:i] {
				x.Close()
			}
			t.Fatalf("listen :0: %v", err)
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
