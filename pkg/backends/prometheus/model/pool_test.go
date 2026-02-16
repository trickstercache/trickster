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

package model

import (
	"bytes"
	"sync"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// TestBufferPool tests the bytes.Buffer pool
func TestBufferPool(t *testing.T) {
	// Test basic get/put
	buf := getBuffer()
	if buf == nil {
		t.Fatal("getBuffer returned nil")
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty buffer, got len=%d", buf.Len())
	}

	buf.WriteString("test data")
	putBuffer(buf)

	// Get again and ensure it's reset
	buf2 := getBuffer()
	if buf2.Len() != 0 {
		t.Errorf("expected reset buffer, got len=%d", buf2.Len())
	}
	putBuffer(buf2)
}

func TestBufferPoolOversized(t *testing.T) {
	buf := getBuffer()
	// Write data larger than maxBufferSize
	largeData := make([]byte, maxBufferSize+1000)
	buf.Write(largeData)

	if buf.Len() <= maxBufferSize {
		t.Fatalf("test setup failed: buffer should be oversized")
	}

	// Put should reject this buffer
	putBuffer(buf)

	// Next get should return a fresh buffer
	buf2 := getBuffer()
	if buf2.Cap() > maxBufferSize {
		t.Errorf("got oversized buffer from pool, cap=%d", buf2.Cap())
	}
	putBuffer(buf2)
}

func TestBufferPoolNil(t *testing.T) {
	// Should not panic
	putBuffer(nil)
}

// TestStringSetPool tests the string set pool
func TestStringSetPool(t *testing.T) {
	m := getStringSet()
	if m == nil {
		t.Fatal("getStringSet returned nil")
	}
	if len(m) != 0 {
		t.Errorf("expected empty set, got len=%d", len(m))
	}

	m.Set("key1")
	m.Set("key2")

	putStringSet(m)

	// Get again and verify cleared
	m2 := getStringSet()
	if len(m2) != 0 {
		t.Errorf("expected cleared set, got len=%d", len(m2))
	}
	putStringSet(m2)
}

func TestStringSetPoolOversized(t *testing.T) {
	m := getStringSet()
	// Add more than maxStringSetSize entries
	for i := 0; i < maxStringSetSize+100; i++ {
		m.Set(string(rune(i)))
	}

	if len(m) <= maxStringSetSize {
		t.Fatalf("test setup failed: set should be oversized")
	}

	// Put should reject this set
	putStringSet(m)

	// Next get should return empty set
	m2 := getStringSet()
	if len(m2) != 0 {
		t.Errorf("expected empty set from pool, got len=%d", len(m2))
	}
	putStringSet(m2)
}

func TestStringSetPoolNil(t *testing.T) {
	// Should not panic
	putStringSet(nil)
}

// TestSeriesDataSetPool tests the series data set pool
func TestSeriesDataSetPool(t *testing.T) {
	m := getSeriesDataSet()
	if m == nil {
		t.Fatal("getSeriesDataSet returned nil")
	}
	if len(m) != 0 {
		t.Errorf("expected empty set, got len=%d", len(m))
	}

	m.Set(WFSeriesData{Name: "test", Instance: "i1", Job: "j1"})
	m.Set(WFSeriesData{Name: "test2", Instance: "i2", Job: "j2"})

	putSeriesDataSet(m)

	// Get again and verify cleared
	m2 := getSeriesDataSet()
	if len(m2) != 0 {
		t.Errorf("expected cleared set, got len=%d", len(m2))
	}
	putSeriesDataSet(m2)
}

func TestSeriesDataSetPoolOversized(t *testing.T) {
	m := getSeriesDataSet()
	// Add more than maxSeriesDataSetSize entries
	for i := 0; i < maxSeriesDataSetSize+100; i++ {
		m.Set(WFSeriesData{Name: string(rune(i)), Instance: "i", Job: "j"})
	}

	if len(m) <= maxSeriesDataSetSize {
		t.Fatalf("test setup failed: set should be oversized")
	}

	// Put should reject this set
	putSeriesDataSet(m)

	// Next get should return empty set
	m2 := getSeriesDataSet()
	if len(m2) != 0 {
		t.Errorf("expected empty set from pool, got len=%d", len(m2))
	}
	putSeriesDataSet(m2)
}

func TestSeriesDataSetPoolNil(t *testing.T) {
	// Should not panic
	putSeriesDataSet(nil)
}

// TestConcurrentBufferPool tests concurrent access to buffer pool
func TestConcurrentBufferPool(t *testing.T) {
	const goroutines = 100
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				buf := getBuffer()
				buf.WriteString("test")
				putBuffer(buf)
			}
		}()
	}

	wg.Wait()
}

// TestConcurrentStringSetPool tests concurrent access to string set pool
func TestConcurrentStringSetPool(t *testing.T) {
	const goroutines = 100
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				m := getStringSet()
				m.Set("test")
				putStringSet(m)
			}
		}()
	}

	wg.Wait()
}

// Benchmarks

func BenchmarkBufferPooled(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf := getBuffer()
		buf.WriteString("benchmark test data")
		putBuffer(buf)
	}
}

func BenchmarkBufferNonPooled(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf := bytes.NewBuffer(nil)
		buf.WriteString("benchmark test data")
	}
}

func BenchmarkStringSetPooled(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m := getStringSet()
		m.Set("key1")
		m.Set("key2")
		putStringSet(m)
	}
}

func BenchmarkStringSetNonPooled(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m := sets.NewStringSet()
		m.Set("key1")
		m.Set("key2")
	}
}

func BenchmarkSeriesDataSetPooled(b *testing.B) {
	b.ReportAllocs()
	data1 := WFSeriesData{Name: "test1", Instance: "i1", Job: "j1"}
	data2 := WFSeriesData{Name: "test2", Instance: "i2", Job: "j2"}
	for i := 0; i < b.N; i++ {
		m := getSeriesDataSet()
		m.Set(data1)
		m.Set(data2)
		putSeriesDataSet(m)
	}
}

func BenchmarkSeriesDataSetNonPooled(b *testing.B) {
	b.ReportAllocs()
	data1 := WFSeriesData{Name: "test1", Instance: "i1", Job: "j1"}
	data2 := WFSeriesData{Name: "test2", Instance: "i2", Job: "j2"}
	for i := 0; i < b.N; i++ {
		m := make(sets.Set[WFSeriesData])
		m.Set(data1)
		m.Set(data2)
	}
}
