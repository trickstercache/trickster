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

package mysql

import (
	"slices"
	"testing"

	"vitess.io/vitess/go/sqltypes"
	querypb "vitess.io/vitess/go/vt/proto/query"
)

func FuzzNativeAuthentication(f *testing.F) {
	f.Add([]byte("12345678901234567890"), "client", []byte{1, 2, 3})
	f.Add([]byte{}, "", []byte{})
	auth := newCredentialAuth(map[string]string{"client": "password"}, "", nil)
	f.Fuzz(func(t *testing.T, salt []byte, user string, response []byte) {
		if len(salt) != 20 {
			return
		}
		_, _ = auth.UserEntryWithHash(nil, salt, user, response, nil)
	})
}

func FuzzHandshakeCapabilityMask(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0, 10})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, packet []byte) {
		original := slices.Clone(packet)
		masked := maskUnsupportedCapabilities(packet)
		if len(masked) != len(packet) {
			t.Fatal("mask changed packet length")
		}
		if !slices.Equal(packet, original) {
			t.Fatal("mask mutated input")
		}
	})
}

func FuzzCommandDispatchClassification(f *testing.F) {
	f.Add("SELECT 1")
	f.Add("PREPARE s FROM 'SELECT ?'")
	f.Add("SELECT 1; SELECT 2")
	f.Add("/*!80000 SET @tenant = 1 */")
	f.Add("\"\\;0")
	f.Fuzz(func(t *testing.T, query string) {
		_ = unsupportedTextFeature(query, false)
		_ = unsupportedTextFeature(query, true)
		_, _ = hasMultipleStatements(query)
	})
}

func FuzzResultRowAccounting(f *testing.F) {
	f.Add([]byte("value"), int64(5))
	f.Add([]byte{}, int64(0))
	f.Fuzz(func(t *testing.T, value []byte, limit int64) {
		if limit < 0 {
			limit = 0
		}
		row := []sqltypes.Value{sqltypes.MakeTrusted(querypb.Type_VARBINARY, value)}
		size, overflow := addRowSize(0, row, limit)
		if !overflow && (size < 0 || size > limit) {
			t.Fatalf("invalid accounted size %d for limit %d", size, limit)
		}
	})
}

func FuzzCachedResultDecode(f *testing.F) {
	valid, err := marshalCachedQueryResult(&cachedQueryResult{result: &sqltypes.Result{}})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add(valid[:min(5, len(valid))])
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = unmarshalCachedQueryResult(data)
	})
}
