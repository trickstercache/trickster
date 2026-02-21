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

package fr

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
)

func TestGetPutCapturesSlice(t *testing.T) {
	tests := []struct {
		name string
		size int
	}{
		{"tiny size (not pooled)", 1},
		{"small size", 4},
		{"medium size", 8},
		{"large size", 16},
		{"max size", 32},
		{"oversized (not pooled)", 64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slice := GetCapturesSlice(tt.size)

			if len(slice) != tt.size {
				t.Errorf("expected length %d, got %d", tt.size, len(slice))
			}

			for i, crw := range slice {
				if crw != nil {
					t.Errorf("expected nil at index %d, got %v", i, crw)
				}
			}

			for i := range slice {
				slice[i] = capture.NewCaptureResponseWriter()
			}

			// Return to pool
			PutCapturesSlice(slice)

			slice2 := GetCapturesSlice(tt.size)
			if len(slice2) != tt.size {
				t.Errorf("expected length %d after pool round-trip, got %d", tt.size, len(slice2))
			}

			for i, crw := range slice2 {
				if crw != nil {
					t.Errorf("expected nil at index %d after pool round-trip, got %v", i, crw)
				}
			}
		})
	}
}

func TestCapturesSliceCapacityHandling(t *testing.T) {
	slice := GetCapturesSlice(8)
	if len(slice) != 8 {
		t.Errorf("expected length 8, got %d", len(slice))
	}

	originalCap := cap(slice)

	// Return to pool
	PutCapturesSlice(slice)

	slice2 := GetCapturesSlice(4)
	if len(slice2) != 4 {
		t.Errorf("expected length 4, got %d", len(slice2))
	}

	if cap(slice2) < 4 {
		t.Errorf("expected capacity >= 4, got %d", cap(slice2))
	}

	// Return to pool
	PutCapturesSlice(slice2)

	slice3 := GetCapturesSlice(16)
	if len(slice3) != 16 {
		t.Errorf("expected length 16, got %d", len(slice3))
	}

	_ = originalCap // Use it
}

func TestResponseChannelPool(t *testing.T) {
	ch := getResponseChannel()
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}

	if cap(ch) != 1 {
		t.Errorf("expected capacity 1, got %d", cap(ch))
	}

	select {
	case <-ch:
		t.Error("expected empty channel")
	default:
		// Good - channel is empty
	}

	ch <- struct{}{}
	putResponseChannel(ch)

	ch2 := getResponseChannel()

	// Verify it's drained
	select {
	case <-ch2:
		t.Error("expected channel to be drained after pool return")
	default:
		// Good - channel is drained
	}

	putResponseChannel(ch2)
}

func TestResponseChannelPoolDrainsOnGet(t *testing.T) {
	ch := getResponseChannel()
	ch <- struct{}{}
	putResponseChannel(ch)
	ch2 := getResponseChannel()

	select {
	case <-ch2:
		t.Error("expected drained channel")
	default:
		// Good
	}

	putResponseChannel(ch2)
}

func TestPoolBoundaries(t *testing.T) {
	slice := GetCapturesSlice(minPoolSize)
	if len(slice) != minPoolSize {
		t.Errorf("expected length %d, got %d", minPoolSize, len(slice))
	}
	PutCapturesSlice(slice)

	slice = GetCapturesSlice(maxPoolSize)
	if len(slice) != maxPoolSize {
		t.Errorf("expected length %d, got %d", maxPoolSize, len(slice))
	}
	PutCapturesSlice(slice)

	slice = GetCapturesSlice(minPoolSize - 1)
	if len(slice) != minPoolSize-1 {
		t.Errorf("expected length %d, got %d", minPoolSize-1, len(slice))
	}
	PutCapturesSlice(slice)

	slice = GetCapturesSlice(maxPoolSize + 1)
	if len(slice) != maxPoolSize+1 {
		t.Errorf("expected length %d, got %d", maxPoolSize+1, len(slice))
	}
	PutCapturesSlice(slice)
}

func TestConcurrentPoolAccess(t *testing.T) {
	const goroutines = 100
	const iterations = 100

	done := make(chan bool, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < iterations; j++ {
				// Alternate between different sizes
				size := 4 + (j % 8)

				slice := GetCapturesSlice(size)
				if len(slice) != size {
					t.Errorf("concurrent access: expected length %d, got %d", size, len(slice))
				}

				// Fill with dummy data
				for k := range slice {
					slice[k] = capture.NewCaptureResponseWriter()
				}

				PutCapturesSlice(slice)

				// Channel pool
				ch := getResponseChannel()
				ch <- struct{}{}
				putResponseChannel(ch)
			}
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < goroutines; i++ {
		<-done
	}
}

func BenchmarkGetCapturesSlice(b *testing.B) {
	b.Run("Pooled_4", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			slice := GetCapturesSlice(4)
			PutCapturesSlice(slice)
		}
	})

	b.Run("Pooled_8", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			slice := GetCapturesSlice(8)
			PutCapturesSlice(slice)
		}
	})

	b.Run("NonPooled_4", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = make([]*capture.CaptureResponseWriter, 4)
		}
	})

	b.Run("NonPooled_8", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = make([]*capture.CaptureResponseWriter, 8)
		}
	})
}

func BenchmarkGetResponseChannel(b *testing.B) {
	b.Run("Pooled", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			ch := getResponseChannel()
			putResponseChannel(ch)
		}
	})

	b.Run("NonPooled", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = make(chan struct{}, 1)
		}
	})
}

func BenchmarkPoolParallel(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			slice := GetCapturesSlice(8)
			for i := range slice {
				slice[i] = capture.NewCaptureResponseWriter()
			}
			PutCapturesSlice(slice)

			ch := getResponseChannel()
			ch <- struct{}{}
			putResponseChannel(ch)
		}
	})
}
