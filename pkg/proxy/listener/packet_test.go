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

package listener

import (
	"net"
	"testing"
)

func TestNewPacketListener(t *testing.T) {
	conn, err := NewPacketListener("127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, ok := conn.LocalAddr().(*net.UDPAddr); !ok {
		t.Errorf("expected a UDP socket, got %T", conn.LocalAddr())
	}

	if _, err = NewPacketListener("240.0.0.1", 1); err == nil {
		t.Error("expected an error binding an unusable address")
	}
}
