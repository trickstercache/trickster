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
	"math"
)

// The tone track is silence with a beep at the start of every second, a higher one each minute, so audio and the
// picture's flash box can be checked for sync
const (
	SampleRate      = 48000
	audioChannels   = 2
	bytesPerSample  = 2
	beepHz          = 1000.0
	minuteBeepHz    = 2000.0
	beepAmplitude   = 0.25 * math.MaxInt16 // -12 dBFS
	beepRampSamples = SampleRate / 1000    // 1 ms fades, so the beep does not click
	secondsPerMin   = 60
)

// Tone renders the tone track by absolute sample number, counted from the Unix epoch at SampleRate
type Tone struct {
	buf []byte
}

// Samples returns n interleaved stereo s16le samples starting at sample from; the slice is reused by the next call
func (t *Tone) Samples(from int64, n int) []byte {
	size := n * audioChannels * bytesPerSample
	if cap(t.buf) < size {
		t.buf = make([]byte, size)
	}
	b := t.buf[:size]
	beepLen := int64(SampleRate / flashShare)
	for i := range n {
		s := from + int64(i)
		var v int16
		if in := s % SampleRate; in < beepLen {
			hz := beepHz
			if (s/SampleRate)%secondsPerMin == 0 {
				hz = minuteBeepHz
			}
			gain := math.Min(1, math.Min(float64(in), float64(beepLen-1-in))/beepRampSamples)
			v = int16(beepAmplitude * gain * math.Sin(2*math.Pi*hz*float64(in)/SampleRate))
		}
		o := i * audioChannels * bytesPerSample
		binary.LittleEndian.PutUint16(b[o:], uint16(v))
		binary.LittleEndian.PutUint16(b[o+bytesPerSample:], uint16(v))
	}
	return b
}

func (t timeline) sampleAt(n int64) int64 {
	// the number of the first audio sample of frame n
	return n/t.fps*SampleRate + n%t.fps*SampleRate/t.fps
}
