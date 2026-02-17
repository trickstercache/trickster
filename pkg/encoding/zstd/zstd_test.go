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

package zstd

import (
	"bytes"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestDecodeEncode(t *testing.T) {
	const expected = "trickster"
	b, err := Encode([]byte(expected))
	if err != nil {
		t.Error(err)
	}
	b, err = Decode(b)
	if err != nil {
		t.Error(err)
	}
	if string(b) != expected {
		t.Errorf("expected %s got %s", expected, string(b))
	}
}

func TestNewDecoder(t *testing.T) {
	const expected = "trickster"
	b, err := Encode([]byte(expected))
	if err != nil {
		t.Error(err)
	}
	r := bytes.NewReader(b)
	dec := NewDecoder(r)
	if dec == nil {
		t.Error("expected non-nil decoder")
	}
}

func TestNewEncoder(t *testing.T) {
	w := httptest.NewRecorder()
	enc := NewEncoder(w, 0)
	if enc == nil {
		t.Error("expected non-nil encoder")
	}

	w = httptest.NewRecorder()
	enc = NewEncoder(w, 1)
	if enc == nil {
		t.Error("expected non-nil encoder")
	}

	w = httptest.NewRecorder()
	enc = NewEncoder(w, 4)
	if enc == nil {
		t.Error("expected non-nil encoder")
	}

	w = httptest.NewRecorder()
	enc = NewEncoder(w, 9)
	if enc == nil {
		t.Error("expected non-nil encoder")
	}
}

// TestConcurrentEncodeDecode verifies that the shared commonEncoder and
// commonDecoder are safe for concurrent use via EncodeAll/DecodeAll.
// If this test fails or produces data corruption, the shared instances
// must be replaced with sync.Pool.
func TestConcurrentEncodeDecode(t *testing.T) {
	// Create test data with varying patterns to detect corruption
	testData := [][]byte{
		bytes.Repeat([]byte("test data 1"), 100),
		bytes.Repeat([]byte("different pattern 2"), 150),
		bytes.Repeat([]byte("yet another test 3"), 200),
		bytes.Repeat([]byte("final test pattern 4"), 250),
	}

	const goroutines = 100
	const iterations = 10

	var wg sync.WaitGroup
	errors := make(chan error, goroutines*iterations*2)

	// Launch concurrent encode/decode operations
	for i := 0; i < goroutines; i++ {
		dataIndex := i % len(testData)
		testBytes := testData[dataIndex]

		wg.Add(2)

		// Concurrent encode
		go func(data []byte, idx int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				encoded, err := Encode(data)
				if err != nil {
					errors <- err
					return
				}
				// Verify we can decode what we just encoded
				decoded, err := Decode(encoded)
				if err != nil {
					errors <- err
					return
				}
				if !bytes.Equal(decoded, data) {
					t.Errorf("data corruption: goroutine %d iteration %d", idx, j)
					return
				}
			}
		}(testBytes, i)

		// Concurrent decode (of pre-encoded data)
		go func(data []byte, idx int) {
			defer wg.Done()
			// Pre-encode the data
			encoded, err := Encode(data)
			if err != nil {
				errors <- err
				return
			}
			for j := 0; j < iterations; j++ {
				decoded, err := Decode(encoded)
				if err != nil {
					errors <- err
					return
				}
				if !bytes.Equal(decoded, data) {
					t.Errorf("data corruption: goroutine %d iteration %d", idx, j)
					return
				}
			}
		}(testBytes, i)
	}

	wg.Wait()
	close(errors)

	// Check for any errors
	for err := range errors {
		t.Error(err)
	}
}
