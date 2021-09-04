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

package engines

import (
	"bytes"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
)

var testString = "Hey, I'm an http response body string."

func TestPCFReadWriteSingle(t *testing.T) {
	w := bytes.NewBuffer(make([]byte, 0, len(testString)))
	r := strings.NewReader(testString)
	l := len(testString)
	resp := &http.Response{}

	pcf := NewPCF(resp, int64(l))
	var n int64
	go func() {
		n, _ = io.Copy(pcf, r)
		pcf.Close()
	}()
	pcf.AddClient(w)

	if n != int64(l) {
		t.Errorf("PCF could not copy full length of reader")
	}

	if w.String() != testString {
		t.Errorf("PCF result was not correct, expected: \"%s\" (Len: %d), got: \"%s\" (Len: %d)",
			testString, len(testString), w.String(), len(w.String()))
	}
}

func TestPCFReadWriteMultiple(t *testing.T) {
	w := bytes.NewBuffer(make([]byte, 0, len(testString)))
	w1 := bytes.NewBuffer(make([]byte, 0, len(testString)))

	r := strings.NewReader(testString)
	l := len(testString)
	resp := &http.Response{}

	pcf := NewPCF(resp, int64(l))
	var n int64
	go func() {
		n, _ = io.Copy(pcf, r)
		pcf.Close()
	}()
	pcf.AddClient(w)
	pcf.AddClient(w1)

	if n != int64(l) {
		t.Errorf("PCF could not copy full length of reader")
	}

	if w.String() != testString {
		t.Errorf("PCF result was not correct, expected: \"%s\" (Len: %d), got: \"%s\" (Len: %d)",
			testString, len(testString), w.String(), len(w.String()))
	}

	if w1.String() != testString {
		t.Errorf("PCF second client result was not correct, expected: \"%s\" (Len: %d), got: \"%s\" (Len: %d)",
			testString, len(testString), w1.String(), len(w1.String()))
	}
}

func TestPCFReadWriteGetBody(t *testing.T) {
	w := bytes.NewBuffer(make([]byte, 0, len(testString)))
	r := strings.NewReader(testString)
	l := len(testString)
	resp := &http.Response{}

	pcf := NewPCF(resp, int64(l))
	var n int64

	_, err := pcf.GetBody()
	if err == nil {
		t.Errorf("PCF expected an error on an unwritten body")
	}

	go func() {
		n, _ = io.Copy(pcf, r)
		pcf.Close()
	}()
	pcf.AddClient(w)

	if n != int64(l) {
		t.Errorf("PCF could not copy full length of reader")
	}

	if w.String() != testString {
		t.Errorf("PCF result was not correct, expected: \"%s\" (Len: %d), got: \"%s\" (Len: %d)",
			testString, len(testString), w.String(), len(w.String()))
	}

	body, err := pcf.GetBody()
	if err != nil {
		t.Error(err)
	}

	if string(body) != testString {
		t.Errorf("PCF result was not correct, expected: \"%s\" (Len: %d), got: \"%s\" (Len: %d)",
			testString, len(testString), string(body), len(body))
	}
}

func TestPCFWaits(t *testing.T) {
	testStringLong := ""
	for i := 0; i < 32000; i++ {
		testStringLong += "DEADBEEF"
	}
	w := bytes.NewBuffer(make([]byte, 0, len(testStringLong)))
	r := strings.NewReader(testStringLong)
	l := len(testStringLong)
	resp := &http.Response{}
	allComplete := uint64(0)
	serverComplete := uint64(0)

	pcf := NewPCF(resp, int64(l))

	go func() {
		buf := make([]byte, HTTPBlockSize)
		var n int
		var err error
		for {
			n, err = r.Read(buf)
			if err != nil && n != 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
			n, err = pcf.Write(buf)
			if err != nil && n == 0 {
				break
			}
		}
		pcf.Close()
	}()
	go pcf.AddClient(w)

	go func() {
		pcf.WaitAllComplete()
		atomic.StoreUint64(&allComplete, 1)
	}()

	go func() {
		pcf.WaitServerComplete()
		atomic.StoreUint64(&serverComplete, 1)
	}()

	if a := atomic.LoadUint64(&serverComplete); a != 0 {
		t.Errorf("WaitServerComplete returned too quickly, expected wait got finished")
	}

	if a := atomic.LoadUint64(&allComplete); a != 0 {
		t.Errorf("WaitAllComplete returned too quickly, expected wait got finished")
	}

	// Wait for pcf to finish in goroutine
	sleepDur := time.Duration(65*(l/HTTPBlockSize) + 1)
	time.Sleep(sleepDur * time.Millisecond)

	if a := atomic.LoadUint64(&serverComplete); a != 1 {
		t.Errorf("Expected WaitServerComplete to have finished with pcf finish")
	}

	if a := atomic.LoadUint64(&allComplete); a != 1 {
		t.Errorf("Expected WaitAllComplete to have finished with pcf finish")
	}

	go func() {
		pcf.WaitAllComplete()
		atomic.StoreUint64(&allComplete, 2)
	}()

	go func() {
		pcf.WaitServerComplete()
		atomic.StoreUint64(&serverComplete, 2)
	}()

	// Give time for goroutines to  initialize and try to wait
	time.Sleep(20 * time.Millisecond)

	if a := atomic.LoadUint64(&serverComplete); a != 2 {
		t.Errorf("Expected WaitServerComplete to not block after pcf completion")
	}

	if a := atomic.LoadUint64(&allComplete); a != 2 {
		t.Errorf("Expected WaitAllComplete to not block after pcf completion")
	}

}

func TestPCFReadWriteClose(t *testing.T) {
	w := bytes.NewBuffer(make([]byte, 0, len(testString)))
	r := strings.NewReader(testString)
	l := len(testString)
	resp := &http.Response{}

	pcf := NewPCF(resp, int64(l))
	buf := make([]byte, 2)
	n, _ := r.Read(buf)
	pcf.Write(buf)
	pcf.Close()
	err := pcf.AddClient(w)

	if err != io.EOF {
		t.Errorf("PCF Close call did not return io.EOF")

	}

	if n != 2 {
		t.Errorf("PCF Close read length incorrect, expected 2, got %d", n)
	}
}

func TestPCFIndexReadTooLarge(t *testing.T) {
	r := strings.NewReader(testString)
	l := len(testString)
	resp := &http.Response{}

	pcf := NewPCF(resp, int64(l))
	buf := make([]byte, 2)
	r.Read(buf)
	pcf.Write(buf)
	pcf.Close()

	_, err := pcf.IndexRead(12412, buf)

	if err != errors.ErrReadIndexTooLarge {
		t.Errorf("PCF did not return ErrReadIndexTooLarge, got %e", err)
	}
}

func TestPCFReadLarge(t *testing.T) {
	r := bytes.NewBuffer(make([]byte, 64000))
	w := bytes.NewBuffer(make([]byte, 64000))
	l := r.Len()
	resp := &http.Response{}

	pcf := NewPCF(resp, int64(l))
	var n int64
	go func() {
		n, _ = io.Copy(pcf, r)
		pcf.Close()
	}()
	pcf.AddClient(w)

	if n != int64(l) {
		t.Errorf("PCF could not copy full length of reader")
	}

	if bytes.Equal(r.Bytes(), w.Bytes()) {
		t.Errorf("PCF result was not correct, expected: \"%s\" (Len: %d), got: \"%s\" (Len: %d)",
			testString, len(testString), w.String(), len(w.String()))
	}
}

func TestPCFResp(t *testing.T) {
	resp := &http.Response{}

	pcf := NewPCF(resp, 10)

	if !reflect.DeepEqual(resp, pcf.GetResp()) {
		t.Errorf("PCF GetResp failed to reproduce the original http response.")
	}
}

func BenchmarkPCFWrite(b *testing.B) {

	bufSize := 32

	testBytes := make([]byte, bufSize*1024)
	l := b.N * bufSize * 1024
	resp := &http.Response{}

	pcf := NewPCF(resp, int64(l))
	b.SetBytes(int64(bufSize) * 1024)

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		pcf.Write(testBytes)
	}
}

func BenchmarkPCFRead(b *testing.B) {

	bufSize := 32

	testBytes := make([]byte, bufSize*1024)
	readBuf := make([]byte, bufSize*1024)

	l := b.N * bufSize * 1024
	resp := &http.Response{}

	var readIndex uint64
	var err error

	pcf := NewPCF(resp, int64(l))
	b.SetBytes(int64(bufSize) * 1024)
	for i := 0; i < b.N; i++ {
		pcf.Write(testBytes)
	}

	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		_, err = pcf.IndexRead(readIndex, readBuf)
		readIndex++
		if err != nil {
			break
		}
	}
}

func BenchmarkPCFWriteRead(b *testing.B) {

	bufSize := 32

	testBytes := make([]byte, bufSize*1024)
	readBuf := make([]byte, bufSize*1024)

	l := b.N * bufSize * 1024
	resp := &http.Response{}

	var readIndex uint64
	var err error

	pcf := NewPCF(resp, int64(l))
	b.SetBytes(int64(bufSize) * 1024)

	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		pcf.Write(testBytes)
		_, err = pcf.IndexRead(readIndex, readBuf)
		readIndex++
		if err != nil {
			break
		}
	}
}
