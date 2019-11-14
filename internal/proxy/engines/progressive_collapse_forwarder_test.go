/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
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
	"testing"
)

var testString = "Hey, I'm an http response body string."

func TestPCFReadWriteSingle(t *testing.T) {
	w := bytes.NewBuffer(make([]byte, 0, len(testString)))
	r := strings.NewReader(testString)
	l := len(testString)
	resp := &http.Response{}

	pcf := NewPCF(resp, l)
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
		t.Errorf("PCF result was not correct, expected: \"%s\" (Len: %d), got: \"%s\" (Len: %d)", testString, len(testString), w.String(), len(w.String()))
	}
}

func TestPCFReadWriteMultiple(t *testing.T) {
	w := bytes.NewBuffer(make([]byte, 0, len(testString)))
	w1 := bytes.NewBuffer(make([]byte, 0, len(testString)))

	r := strings.NewReader(testString)
	l := len(testString)
	resp := &http.Response{}

	pcf := NewPCF(resp, l)
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
		t.Errorf("PCF result was not correct, expected: \"%s\" (Len: %d), got: \"%s\" (Len: %d)", testString, len(testString), w.String(), len(w.String()))
	}

	if w1.String() != testString {
		t.Errorf("PCF second client result was not correct, expected: \"%s\" (Len: %d), got: \"%s\" (Len: %d)", testString, len(testString), w1.String(), len(w1.String()))
	}
}

func TestPCFReadWriteGetBody(t *testing.T) {
	w := bytes.NewBuffer(make([]byte, 0, len(testString)))
	r := strings.NewReader(testString)
	l := len(testString)
	resp := &http.Response{}

	pcf := NewPCF(resp, l)
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
		t.Errorf("PCF result was not correct, expected: \"%s\" (Len: %d), got: \"%s\" (Len: %d)", testString, len(testString), w.String(), len(w.String()))
	}

	body, err := pcf.GetBody()
	if err != nil {
		t.Error(err)
	}

	if string(body) != testString {
		t.Errorf("PCF result was not correct, expected: \"%s\" (Len: %d), got: \"%s\" (Len: %d)", testString, len(testString), string(body), len(body))
	}
}

func TestPCFReadWriteClose(t *testing.T) {
	w := bytes.NewBuffer(make([]byte, 0, len(testString)))
	r := strings.NewReader(testString)
	l := len(testString)
	resp := &http.Response{}

	pcf := NewPCF(resp, l)
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

	pcf := NewPCF(resp, l)
	buf := make([]byte, 2)
	r.Read(buf)
	pcf.Write(buf)
	pcf.Close()

	_, err := pcf.IndexRead(12412, buf)

	if err != ErrReadIndexTooLarge {
		t.Errorf("PCF did not return ErrReadIndexTooLarge, got %e", err)
	}
}

func TestPCFReadLarge(t *testing.T) {
	r := bytes.NewBuffer(make([]byte, 64000))
	w := bytes.NewBuffer(make([]byte, 64000))
	l := r.Len()
	resp := &http.Response{}

	pcf := NewPCF(resp, l)
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
		t.Errorf("PCF result was not correct, expected: \"%s\" (Len: %d), got: \"%s\" (Len: %d)", testString, len(testString), w.String(), len(w.String()))
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
	// 100MB object, simulated actual usecase sizes.
	b.N = 3200

	testBytes := make([]byte, 32*1024)
	l := b.N * 32 * 1024
	resp := &http.Response{}

	pcf := NewPCF(resp, l)
	b.SetBytes(32 * 1024)

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		pcf.Write(testBytes)
	}
}

func BenchmarkPCFRead(b *testing.B) {
	b.N = 3200

	testBytes := make([]byte, 32*1024)
	readBuf := make([]byte, 32*1024)

	l := b.N * 32 * 1024
	resp := &http.Response{}

	var readIndex uint64
	var err error

	pcf := NewPCF(resp, l)
	b.SetBytes(32 * 1024)
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
	b.N = 3200

	testBytes := make([]byte, 32*1024)
	readBuf := make([]byte, 32*1024)

	l := b.N * 32 * 1024
	resp := &http.Response{}

	var readIndex uint64
	var err error

	pcf := NewPCF(resp, l)
	b.SetBytes(32 * 1024)

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
