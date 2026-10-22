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
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// Captions are one cue per second, the same text as the burned-in caption line. They depend only on time, so a
// caption segment is built when asked for: WebVTT for HLS, WebVTT in fragmented MP4 (ISO/IEC 14496-30) for DASH.
const (
	KindText         = "text"
	textTimescale    = 1000
	vttExt           = ".vtt"
	vttHeader        = "WEBVTT"
	vttTimeLayout    = "15:04:05.000"
	textTrackID      = 1
	languageUnd      = 0x55c4 // "und", packed as ISO 639-2/T in 15 bits
	fixedPointOne    = 0x00010000
	fixedPointVolume = 0x0100
	tkhdEnabled      = 0x000003 // track enabled and in the movie
	tfhdDefaultBase  = 0x020000
	trunTextFlags    = trunDataOffsetPresent | trunDurationPresent | trunSizePresent
	textBandwidth    = 1000
)

// the unity transformation matrix boxes carry
var identity = [9]uint32{fixedPointOne, 0, 0, 0, fixedPointOne, 0, 0, 0, 0x40000000}

func u16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

func mkbox(typ string, parts ...[]byte) []byte {
	size := boxHeader
	for _, p := range parts {
		size += len(p)
	}
	out := make([]byte, 0, size)
	out = binary.BigEndian.AppendUint32(out, uint32(size))
	out = append(out, typ...)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func fullbox(typ string, version uint8, flags uint32, parts ...[]byte) []byte {
	return mkbox(typ, append([][]byte{u32(uint32(version)<<24 | flags&0xffffff)}, parts...)...)
}

func matrix() []byte {
	out := make([]byte, 0, len(identity)*4)
	for _, v := range identity {
		out = binary.BigEndian.AppendUint32(out, v)
	}
	return out
}

func zeros(n int) []byte { return make([]byte, n) }

func wvttInit() []byte {
	// the initialization segment of the DASH caption track
	ftyp := mkbox(boxFtyp, []byte("iso6"), u32(0), []byte("iso6cmfcdash"))
	mvhd := fullbox("mvhd", 0, 0, u32(0), u32(0), u32(textTimescale), u32(0), u32(fixedPointOne),
		u16(fixedPointVolume), zeros(10), matrix(), zeros(24), u32(textTrackID+1))
	tkhd := fullbox("tkhd", 0, tkhdEnabled, u32(0), u32(0), u32(textTrackID), zeros(4), u32(0), zeros(8), u16(0),
		u16(0), u16(0), zeros(2), matrix(), u32(0), u32(0))
	mdhd := fullbox(boxMdhd, 0, 0, u32(0), u32(0), u32(textTimescale), u32(0), u16(languageUnd), u16(0))
	hdlr := fullbox("hdlr", 0, 0, u32(0), []byte(KindText), zeros(12), []byte("WebVTT\x00"))
	dinf := mkbox("dinf", fullbox("dref", 0, 0, u32(1), fullbox("url ", 0, 1)))
	wvtt := mkbox("wvtt", zeros(6), u16(1), mkbox("vttC", []byte(vttHeader)))
	stbl := mkbox(boxStbl, fullbox(boxStsd, 0, 0, u32(1), wvtt), fullbox("stts", 0, 0, u32(0)),
		fullbox("stsc", 0, 0, u32(0)), fullbox("stsz", 0, 0, u32(0), u32(0)), fullbox("stco", 0, 0, u32(0)))
	minf := mkbox(boxMinf, fullbox("nmhd", 0, 0), dinf, stbl)
	trak := mkbox(boxTrak, tkhd, mkbox(boxMdia, mdhd, hdlr, minf))
	mvex := mkbox("mvex", fullbox("trex", 0, 0, u32(textTrackID), u32(1), u32(0), u32(0), u32(0)))
	return append(ftyp, mkbox(boxMoov, mvhd, trak, mvex)...)
}

func (s *Streamer) cueTimes(n int64) []time.Time {
	// lists the starts of the one-second cues of segment number n
	start := s.p.startTime().Add(time.Duration(n-s.video.startSeg) * s.cfg.Segment)
	count := int(s.cfg.Segment / time.Second)
	out := make([]time.Time, count)
	for i := range out {
		out[i] = start.Add(time.Duration(i) * time.Second)
	}
	return out
}

func (s *Streamer) mediaOffset(at time.Time) time.Duration {
	// how far into the stream's media timeline at falls
	return at.Sub(s.p.startTime())
}

func vttTimestamp(d time.Duration) string {
	return time.Unix(0, 0).UTC().Add(d).Format(vttTimeLayout)
}

func (s *Streamer) webVTTSegment(n int64) string {
	// the HLS caption segment for segment number n; cue times are on the media timeline, which
	// starts at zero with the first frame, as the fragments' decode times do
	var b strings.Builder
	b.WriteString(vttHeader + "\nX-TIMESTAMP-MAP=MPEGTS:0,LOCAL:00:00:00.000\n\n")
	for _, at := range s.cueTimes(n) {
		off := s.mediaOffset(at)
		fmt.Fprintf(&b, "%s --> %s\n%s\n\n", vttTimestamp(off), vttTimestamp(off+time.Second),
			captionText(s.cfg.Title, at))
	}
	return b.String()
}

func (s *Streamer) wvttSegment(n int64) []byte {
	// the DASH caption segment for segment number n: one sample per cue, each a vttc box
	cues := s.cueTimes(n)
	samples := make([][]byte, len(cues))
	entries := make([]byte, 0, 8*len(cues))
	for i, at := range cues {
		samples[i] = mkbox("vttc", mkbox("payl", []byte(captionText(s.cfg.Title, at))))
		entries = append(entries, u32(textTimescale)...)
		entries = append(entries, u32(uint32(len(samples[i])))...) //nolint:gosec // one short cue
	}
	decode := uint64(s.mediaOffset(cues[0]).Milliseconds()) //nolint:gosec // cues start with the stream
	count := uint32(len(cues))                              //nolint:gosec // seconds per segment
	sequence := uint32(n)                                   //nolint:gosec // the segment number's low 32 bits
	build := func(offset uint32) []byte {
		tfhd := fullbox(boxTfhd, 0, tfhdDefaultBase, u32(textTrackID))
		tfdt := fullbox(boxTfdt, 1, 0, u64(decode))
		trun := fullbox(boxTrun, 0, trunTextFlags, u32(count), u32(offset), entries)
		return mkbox(boxMoof, fullbox("mfhd", 0, 0, u32(sequence)), mkbox(boxTraf, tfhd, tfdt, trun))
	}
	// the data offset counts from the moof to the first sample, past the mdat header
	moof := build(0)
	moof = build(uint32(len(moof) + boxHeader)) //nolint:gosec // a moof of a few hundred bytes
	return append(moof, mkbox(boxMdat, samples...)...)
}

func (s *Streamer) subtitlesPlaylist() string {
	// lists the HLS caption segments alongside the video's window
	segs := s.video.window(s.cfg.Window)
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:%d\n#EXT-X-TARGETDURATION:%d\n", hlsVersion, s.targetDuration())
	first := s.video.startSeg
	if len(segs) > 0 {
		first = segs[0].number
	}
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", first)
	for _, seg := range segs {
		fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n#EXTINF:%s,\n%s\n", seg.start.UTC().Format(pdtLayout),
			seconds(s.cfg.Segment), s.mediaPath(KindText, fmt.Sprintf("%d%s", seg.number, vttExt)))
	}
	return b.String()
}
