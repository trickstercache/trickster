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

package server

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// FuzzReadClientHello feeds arbitrary bytes into readClientHello and verifies
// it never panics. The parser must gracefully handle truncated, oversized, or
// malformed input.
func FuzzReadClientHello(f *testing.F) {
	// Seed with a valid ClientHello payload (without the leading packet-type byte).
	var seed bytes.Buffer
	w := newProtoWriter(&seed)
	w.putStr("clickhouse-go")
	w.putUvarint(2)
	w.putUvarint(0)
	w.putUvarint(ServerRevision)
	w.putStr("default")
	w.putStr("user")
	w.putStr("pass")
	f.Add(seed.Bytes())

	// Minimal valid payload.
	var minimal bytes.Buffer
	wm := newProtoWriter(&minimal)
	wm.putStr("")
	wm.putUvarint(0)
	wm.putUvarint(0)
	wm.putUvarint(0)
	wm.putStr("")
	wm.putStr("")
	wm.putStr("")
	f.Add(minimal.Bytes())

	// Empty and tiny inputs.
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})

	f.Fuzz(func(t *testing.T, data []byte) {
		r := newProtoReader(bytes.NewReader(data))
		readClientHello(r) // must not panic
	})
}

// FuzzReadClientQuery feeds arbitrary bytes into readClientQuery and verifies
// it never panics. This is the most complex parser — it reads client info,
// settings, interserver secret, compression flag, SQL, and parameters.
func FuzzReadClientQuery(f *testing.F) {
	// Seed with a valid ClientQuery payload built by sendTestQuery.
	var seed bytes.Buffer
	w := newProtoWriter(&seed)
	w.putStr("")          // query ID
	w.putByte(1)          // query kind
	w.putStr("")          // initial user
	w.putStr("")          // initial query ID
	w.putStr("127.0.0.1") // initial address
	w.putInt64(0)         // initial query start time
	w.putByte(1)          // interface = TCP
	w.putStr("")          // os user
	w.putStr("")          // os hostname
	w.putStr("test")      // client name
	w.putUvarint(1)       // major
	w.putUvarint(0)       // minor
	w.putUvarint(ServerRevision)
	w.putStr("")     // quota key
	w.putUvarint(0)  // distributed depth
	w.putUvarint(0)  // version patch
	w.putByte(0)     // no otel span
	w.putUvarint(0)  // parallel replicas fields
	w.putUvarint(0)
	w.putUvarint(0)
	w.putStr("")     // end settings
	w.putStr("")     // interserver secret
	w.putUvarint(2)  // state complete
	w.putBool(false) // no compression
	w.putStr("SELECT 1")
	w.putStr("") // end parameters
	f.Add(seed.Bytes(), uint64(ServerRevision))

	// Seed with an older revision.
	f.Add(seed.Bytes(), uint64(54060))

	// Empty and tiny inputs at various revisions.
	f.Add([]byte{}, uint64(ServerRevision))
	f.Add([]byte{0}, uint64(ServerRevision))
	f.Add([]byte{0xff, 0xff, 0xff}, uint64(54060))

	f.Fuzz(func(t *testing.T, data []byte, revision uint64) {
		// Cap revision to a reasonable range to avoid nonsensical code paths.
		if revision > 100000 {
			revision = 100000
		}
		r := newProtoReader(bytes.NewReader(data))
		readClientQuery(r, revision) // must not panic
	})
}

// FuzzWriteJSONAsNativeBlocks feeds arbitrary bytes (treated as a JSON body)
// into writeJSONAsNativeBlocks and verifies it never panics.
func FuzzWriteJSONAsNativeBlocks(f *testing.F) {
	f.Add([]byte(`{"meta":[{"name":"x","type":"UInt32"}],"data":[{"x":1}],"rows":1}`))
	f.Add([]byte(`{"meta":[],"data":[]}`))
	f.Add([]byte(`not json at all`))
	f.Add([]byte(`{}`))
	f.Add([]byte{})
	f.Add([]byte(`{"meta":[{"name":"s","type":"String"}],"data":[{"s":"hello"}],"rows":1}`))
	f.Add([]byte(`{"meta":[{"name":"d","type":"DateTime"}],"data":[{"d":1700000000}],"rows":1}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var buf bytes.Buffer
		w := newProtoWriter(&buf)
		writeJSONAsNativeBlocks(w, data, false) // must not panic
	})
}

// FuzzSkipClientData feeds arbitrary bytes into skipClientData and verifies
// it never panics.
func FuzzSkipClientData(f *testing.F) {
	// Seed with a valid empty data block (no leading packet type byte).
	var seed bytes.Buffer
	w := newProtoWriter(&seed)
	w.putStr("")     // block name
	w.putUvarint(0)  // is_overflows
	w.putBool(false) // bucket_num
	w.putUvarint(2)  // bucket_size
	w.putInt32(-1)   // reserved
	w.putUvarint(0)  // reserved
	w.putUvarint(0)  // num columns
	w.putUvarint(0)  // num rows
	f.Add(seed.Bytes(), uint64(ServerRevision))

	f.Add([]byte{}, uint64(ServerRevision))
	f.Add([]byte{0, 0}, uint64(0))

	f.Fuzz(func(t *testing.T, data []byte, revision uint64) {
		if revision > 100000 {
			revision = 100000
		}
		r := newProtoReader(bytes.NewReader(data))
		skipClientData(r, revision) // must not panic
	})
}

// FuzzProtoReaderStr feeds arbitrary bytes into the string reader to verify
// it handles oversized length prefixes and truncated data without panicking.
func FuzzProtoReaderStr(f *testing.F) {
	// Valid string.
	var valid bytes.Buffer
	w := newProtoWriter(&valid)
	w.putStr("hello")
	f.Add(valid.Bytes())

	// Length prefix claiming huge size with no data.
	var huge bytes.Buffer
	buf := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(buf, 1<<30)
	huge.Write(buf[:n])
	f.Add(huge.Bytes())

	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0x0f})

	f.Fuzz(func(t *testing.T, data []byte) {
		r := newProtoReader(bytes.NewReader(data))
		r.str() // must not panic
	})
}
