/*
 * Copyright 2026 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 */

package portutil

import (
	"net"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReserve(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "0.0.0.0"} {
		t.Run(address, func(t *testing.T) {
			var ports []int
			var release func()
			if address == "127.0.0.1" {
				ports, release = Reserve(t, 2)
			} else {
				ports, release = ReserveOn(t, 2, address)
			}
			require.NotEqual(t, ports[0], ports[1])
			for _, port := range ports {
				listener, err := net.Listen("tcp", net.JoinHostPort(address, strconv.Itoa(port)))
				if listener != nil {
					listener.Close()
				}
				require.Error(t, err, "reserved port must remain unavailable")
			}
			release()
			release()
			for _, port := range ports {
				listener, err := net.Listen("tcp", net.JoinHostPort(address, strconv.Itoa(port)))
				require.NoError(t, err)
				require.NoError(t, listener.Close())
			}
		})
	}
}
