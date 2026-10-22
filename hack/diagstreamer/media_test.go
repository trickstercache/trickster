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

package diagstreamer

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

type recorder struct {
	info   TrackInfo
	chunks []Chunk
}

func (r *recorder) Init(info TrackInfo) { r.info = info }
func (r *recorder) Chunk(c Chunk)       { r.chunks = append(r.chunks, c) }

func testMoov(mdhd, entry []byte) []byte {
	stbl := mkbox(boxStbl, fullbox(boxStsd, 0, 0, u32(1), entry))
	return mkbox(boxMoov, mkbox(boxTrak, mkbox(boxMdia, mdhd, mkbox(boxMinf, stbl))))
}

func avcMoov() []byte {
	mdhd := fullbox(boxMdhd, 0, 0, u32(0), u32(0), u32(90000), u32(0), u16(languageUnd), u16(0))
	return testMoov(mdhd, mkbox(codecAVC1, zeros(78), mkbox(boxAvcC, []byte{1, 0x64, 0x00, 0x1f, 0xff})))
}

func keyMoof(decode uint64) []byte {
	// has two samples of 3000 under tfhd defaults that mark samples non-sync, the first overridden to sync
	tfhd := fullbox(boxTfhd, 0, tfhdBaseOffsetPresent|tfhdDurationPresent|tfhdFlagsPresent, u32(1), u64(0),
		u32(3000), u32(sampleIsNonSync))
	trun := fullbox(boxTrun, 0, trunFirstFlagsPresent, u32(2), u32(0))
	return mkbox(boxMoof, fullbox("mfhd", 0, 0, u32(1)), mkbox(boxTraf, tfhd, fullbox(boxTfdt, 1, 0, u64(decode)), trun))
}

func deltaMoof(decode uint32) []byte {
	// carries per-sample durations, sizes and flags, the first sample non-sync
	samples := append(append(u32(1000), u32(10)...), u32(sampleIsNonSync)...)
	samples = append(samples, append(append(u32(2000), u32(10)...), u32(0)...)...)
	trun := fullbox(boxTrun, 0, trunDurationPresent|trunSizePresent|trunFlagsPresent, u32(2), samples)
	return mkbox(boxMoof, mkbox(boxTraf, fullbox(boxTfhd, 0, 0, u32(1)), fullbox(boxTfdt, 0, 0, u32(decode)), trun))
}

func TestTrackInfo(t *testing.T) {
	ts, codec, err := trackInfo(avcMoov())
	if err != nil || ts != 90000 || codec != "avc1.64001f" {
		t.Errorf("avc: %d %s %v", ts, codec, err)
	}
	mdhd := fullbox(boxMdhd, 1, 0, u64(0), u64(0), u32(48000), u64(0), u16(languageUnd), u16(0))
	ts, codec, err = trackInfo(testMoov(mdhd, mkbox(codecMP4A, zeros(28))))
	if err != nil || ts != 48000 || codec != codecAAC {
		t.Errorf("aac: %d %s %v", ts, codec, err)
	}
	if _, _, err = trackInfo(find(wvttInit(), boxMoov)); err == nil || !strings.Contains(err.Error(), "wvtt") {
		t.Errorf("an unknown sample entry is refused: %v", err)
	}
	for name, moov := range map[string][]byte{
		"no trak":  mkbox(boxMoov),
		"no mdhd":  mkbox(boxMoov, mkbox(boxTrak, mkbox(boxMdia))),
		"no stsd":  mkbox(boxMoov, mkbox(boxTrak, mkbox(boxMdia, fullbox(boxMdhd, 0, 0, zeros(20))))),
		"no avcC":  testMoov(fullbox(boxMdhd, 0, 0, zeros(20)), mkbox(codecAVC1, zeros(78))),
		"short v1": mkbox(boxMoov, mkbox(boxTrak, mkbox(boxMdia, fullbox(boxMdhd, 1, 0, zeros(20))))),
	} {
		if _, _, err = trackInfo(moov); !errors.Is(err, errBoxFormat) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestFragmentTiming(t *testing.T) {
	dt, dur, key, err := fragmentTiming(keyMoof(1 << 40))
	if err != nil || dt != 1<<40 || dur != 6000 || !key {
		t.Errorf("defaults: %d %d %v %v", dt, dur, key, err)
	}
	dt, dur, key, err = fragmentTiming(deltaMoof(500))
	if err != nil || dt != 500 || dur != 3000 || key {
		t.Errorf("per sample: %d %d %v %v", dt, dur, key, err)
	}
	if _, _, _, err = fragmentTiming(mkbox(boxMoof)); !errors.Is(err, errBoxFormat) {
		t.Error("no traf")
	}
	if _, _, _, err = fragmentTiming(mkbox(boxMoof, mkbox(boxTraf))); !errors.Is(err, errBoxFormat) {
		t.Error("no trun")
	}
	short := fullbox(boxTrun, 0, trunDurationPresent, u32(3), u32(1))
	if _, _, _, err = fragmentTiming(mkbox(boxMoof, mkbox(boxTraf, short))); !errors.Is(err, errBoxFormat) {
		t.Error("short trun")
	}
}

func TestReadBox(t *testing.T) {
	large := append(binary.BigEndian.AppendUint32(nil, 1), "free"...)
	large = append(binary.BigEndian.AppendUint64(large, 20), 1, 2, 3, 4)
	typ, b, err := readBox(bufio.NewReader(bytes.NewReader(large)))
	if err != nil || typ != "free" || len(b) != 20 {
		t.Errorf("large box: %s %d %v", typ, len(b), err)
	}
	for name, in := range map[string][]byte{
		"short header": {0, 0, 0},
		"short large":  large[:12],
		"short body":   large[:18],
		"too small":    append(binary.BigEndian.AppendUint32(nil, 4), "free"...),
	} {
		if _, _, err = readBox(bufio.NewReader(bytes.NewReader(in))); err == nil || errors.Is(err, io.EOF) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err = readBox(bufio.NewReader(bytes.NewReader(nil))); !errors.Is(err, io.EOF) {
		t.Error("an empty reader is the end")
	}
	for _, in := range [][]byte{{0, 0}, {0, 0, 0, 1, 'f', 'r', 'e', 'e'}, {0, 0, 0, 9, 'f', 'r', 'e', 'e'},
		append(binary.BigEndian.AppendUint32(nil, 1), "free"...)} {
		if children(in, func(string, []byte) error { return nil }) == nil {
			t.Errorf("children of % x is refused", in)
		}
	}
}

func TestReadTrack(t *testing.T) {
	ftyp := mkbox(boxFtyp, []byte("iso6"), u32(0))
	mdat := mkbox(boxMdat, []byte("frames"))
	var in []byte
	for _, b := range [][]byte{ftyp, avcMoov(), mkbox(boxStyp, []byte("msdh")), keyMoof(0), mdat, deltaMoof(6000),
		mdat} {
		in = append(in, b...)
	}
	var r recorder
	if err := readTrack(bytes.NewReader(in), KindVideo, &r); err != nil {
		t.Fatal(err)
	}
	if r.info.Kind != KindVideo || r.info.Codec != "avc1.64001f" || !bytes.HasPrefix(r.info.Init, ftyp) {
		t.Errorf("info %+v", r.info)
	}
	if len(r.chunks) != 2 || !r.chunks[0].Key || r.chunks[1].Key || r.chunks[1].DecodeTime != 6000 ||
		!bytes.HasPrefix(r.chunks[0].Data, mkbox(boxStyp, []byte("msdh"))) {
		t.Fatalf("chunks %+v", r.chunks)
	}
	for name, in := range map[string][]byte{
		"mdat alone":    mdat,
		"bad moov":      mkbox(boxMoov),
		"bad moof":      append(mkbox(boxMoof), mdat...),
		"truncated box": ftyp[:10],
	} {
		if err := readTrack(bytes.NewReader(in), KindVideo, &r); err == nil {
			t.Errorf("%s is refused", name)
		}
	}
}

func TestCaptionBoxes(t *testing.T) {
	init := wvttInit()
	var types []string
	if err := children(init, func(typ string, _ []byte) error { types = append(types, typ); return nil }); err != nil ||
		strings.Join(types, ",") != "ftyp,moov" {
		t.Fatalf("init boxes %v %v", types, err)
	}
	moov := find(init, boxMoov)
	if find(moov[boxHeader:], boxTrak, boxMdia, boxMinf, boxStbl, boxStsd) == nil ||
		find(moov[boxHeader:], "mvex", "trex") == nil {
		t.Error("init structure")
	}
	if mdhd := find(moov[boxHeader:], boxTrak, boxMdia, boxMdhd); binary.BigEndian.Uint32(mdhd[boxHeader+12:]) != textTimescale {
		t.Error("text timescale")
	}
}
