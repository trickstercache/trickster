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

package pgwire

import (
	"bytes"
	"errors"
	"testing"
)

const framesTestMaxBody = 1024

type recordedFrame struct {
	typ     byte
	bodyLen int
}

type recordingObserver struct {
	frames  []recordedFrame
	firsts  []byte
	capture byte
	bodies  []string
}

func (r *recordingObserver) message(typ byte, bodyLen int) bool {
	r.frames = append(r.frames, recordedFrame{typ, bodyLen})
	return typ == r.capture
}

func (r *recordingObserver) firstByte(_, b byte) { r.firsts = append(r.firsts, b) }

func (r *recordingObserver) body(_ byte, body []byte) { r.bodies = append(r.bodies, string(body)) }

func framesTestStream() ([]byte, []recordedFrame) {
	var stream []byte
	stream = appendFrame(stream, msgQuery, []byte("select 1\x00"))
	stream = appendFrame(stream, msgSync, nil)
	stream = appendFrame(stream, msgReadyForQuery, []byte{'T'})
	stream = appendFrame(stream, msgDataRow, bytes.Repeat([]byte{7}, 300))
	stream = appendFrame(stream, msgReadyForQuery, []byte{'I'})
	return stream, []recordedFrame{{msgQuery, 9}, {msgSync, 0}, {msgReadyForQuery, 1}, {msgDataRow, 300}, {msgReadyForQuery, 1}}
}

func TestFrameScannerFindsBoundariesAtEveryChunkSize(t *testing.T) {
	stream, want := framesTestStream()
	for chunk := 1; chunk <= len(stream); chunk++ {
		observer := &recordingObserver{}
		scanner := newFrameScanner(observer, framesTestMaxBody)
		for offset := 0; offset < len(stream); offset += chunk {
			if err := scanner.scan(stream[offset:min(offset+chunk, len(stream))]); err != nil {
				t.Fatalf("chunk %d: %v", chunk, err)
			}
		}
		if !scanner.atBoundary() {
			t.Fatalf("chunk %d: expected to end on a boundary", chunk)
		}
		if len(observer.frames) != len(want) {
			t.Fatalf("chunk %d: frames %v", chunk, observer.frames)
		}
		for i := range want {
			if observer.frames[i] != want[i] {
				t.Fatalf("chunk %d: frame %d = %v, want %v", chunk, i, observer.frames[i], want[i])
			}
		}
		if string(observer.firsts) != "TI" {
			t.Fatalf("chunk %d: transaction statuses %q", chunk, observer.firsts)
		}
	}
}

func TestFrameScannerRejectsInvalidLengths(t *testing.T) {
	for name, header := range map[string][]byte{
		"shorter than the length field": {msgQuery, 0, 0, 0, 3},
		"larger than the limit":         {msgQuery, 0, 0, 8, 0},
		"negative as int32":             {msgQuery, 0xff, 0xff, 0xff, 0xff},
	} {
		scanner := newFrameScanner(&recordingObserver{}, framesTestMaxBody)
		if err := scanner.scan(header); !errors.Is(err, errFrameLength) {
			t.Fatalf("%s: expected errFrameLength, got %v", name, err)
		}
		if _, _, err := readFrame(bytes.NewReader(header), framesTestMaxBody); !errors.Is(err, errFrameLength) {
			t.Fatalf("%s: readFrame expected errFrameLength, got %v", name, err)
		}
	}
	scanner := newFrameScanner(&recordingObserver{}, framesTestMaxBody)
	if err := scanner.scan([]byte{msgQuery, 0, 0}); err != nil || scanner.atBoundary() {
		t.Fatalf("a partial header is not an error and not a boundary: %v", err)
	}
}

func TestReadFrame(t *testing.T) {
	stream, _ := framesTestStream()
	reader := bytes.NewReader(stream)
	typ, body, err := readFrame(reader, framesTestMaxBody)
	if err != nil || typ != msgQuery || string(body) != "select 1\x00" {
		t.Fatalf("unexpected frame %q %q %v", typ, body, err)
	}
	if typ, body, err = readFrame(reader, framesTestMaxBody); err != nil || typ != msgSync || len(body) != 0 {
		t.Fatalf("unexpected frame %q %q %v", typ, body, err)
	}
	if _, _, err = readFrame(bytes.NewReader(stream[:3]), framesTestMaxBody); err == nil {
		t.Fatal("expected a short header to fail")
	}
	if _, _, err = readFrame(bytes.NewReader(stream[:8]), framesTestMaxBody); err == nil {
		t.Fatal("expected a short body to fail")
	}
}

func FuzzFrameScanner(f *testing.F) {
	stream, _ := framesTestStream()
	f.Add(stream, uint8(7))
	f.Add([]byte{msgQuery, 0, 0, 0, 3}, uint8(1))
	f.Add([]byte{}, uint8(0))
	f.Fuzz(func(t *testing.T, data []byte, chunkSize uint8) {
		// however the bytes are chunked, the scanner must agree with itself
		whole := &recordingObserver{}
		wholeErr := newFrameScanner(whole, framesTestMaxBody).scan(data)
		chunked := &recordingObserver{}
		scanner := newFrameScanner(chunked, framesTestMaxBody)
		step := int(chunkSize)%16 + 1
		var chunkedErr error
		for offset := 0; offset < len(data) && chunkedErr == nil; offset += step {
			chunkedErr = scanner.scan(data[offset:min(offset+step, len(data))])
		}
		if (wholeErr == nil) != (chunkedErr == nil) || len(whole.frames) != len(chunked.frames) ||
			!bytes.Equal(whole.firsts, chunked.firsts) {
			t.Fatalf("chunking changed the result: %v/%v %v/%v", wholeErr, chunkedErr, whole.frames, chunked.frames)
		}
	})
}

func FuzzStartupPacket(f *testing.F) {
	f.Add(startupPacket(protocolMajor<<16, paramUser, testClientUser))
	f.Add(startupPacket(codeSSLRequest))
	f.Add([]byte{0, 0, 0, 8, 4, 210, 22, 46})
	f.Fuzz(func(t *testing.T, data []byte) {
		code, packet, err := readStartupPacket(bytes.NewReader(data))
		if err != nil {
			return
		}
		if len(packet) < minStartupPacketLen || len(packet) > maxStartupPacketLen {
			t.Fatalf("accepted a %d-byte packet", len(packet))
		}
		if code>>16 == protocolMajor {
			_, _ = parseStartupParams(packet[minStartupPacketLen:])
		}
	})
}

func FuzzSCRAMMessages(f *testing.F) {
	f.Add(gs2NoBinding+"n=,r="+scramTestClientNonce, "c=biws,r=x,p=AAAA")
	f.Add(scramPlusHeader+"n=,r=x", "")
	f.Fuzz(func(t *testing.T, clientFirst, clientFinal string) {
		server := scramTestServer(t, scramTestBinding)
		for _, mechanism := range []string{mechSCRAMSHA256, mechSCRAMSHA256Plus} {
			if _, err := server.first(mechanism, []byte(clientFirst)); err != nil {
				continue
			}
			// no input may authenticate without the password
			if _, err := server.final([]byte(clientFinal)); err == nil {
				t.Fatalf("authenticated with %q / %q", clientFirst, clientFinal)
			}
		}
	})
}
