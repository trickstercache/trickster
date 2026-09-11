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

package client

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientUDPSize(t *testing.T) {
	require.Equal(t, uint16(DefaultUDPSize), (&Client{}).udpSize())
	require.Equal(t, uint16(0), (&Client{UDPSize: -1}).udpSize(),
		"a negative size disables EDNS0")
	require.Equal(t, uint16(4096), (&Client{UDPSize: 4096}).udpSize())
	require.Equal(t, uint16(maxTCPSize), (&Client{UDPSize: 1 << 20}).udpSize(),
		"the advertised size cannot exceed what the field can hold")
}

func TestExchangeTCPOversizeQuery(t *testing.T) {
	_, err := exchangeTCP(nil, &Msg{}, make([]byte, maxTCPSize+1))
	require.ErrorIs(t, err, ErrLongMessage,
		"the two-byte length prefix cannot frame a larger query")
}

func TestMessageIDVaries(t *testing.T) {
	seen := make(map[uint16]struct{}, 32)
	for range 32 {
		seen[messageID()] = struct{}{}
	}
	require.Greater(t, len(seen), 1, "query IDs must not be constant")
}
