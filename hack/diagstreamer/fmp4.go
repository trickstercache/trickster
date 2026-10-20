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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ISO BMFF box types the track reader handles
const (
	boxFtyp = "ftyp"
	boxMoov = "moov"
	boxStyp = "styp"
	boxMoof = "moof"
	boxMdat = "mdat"
	boxTrak = "trak"
	boxMdia = "mdia"
	boxMdhd = "mdhd"
	boxMinf = "minf"
	boxStbl = "stbl"
	boxStsd = "stsd"
	boxTraf = "traf"
	boxTfhd = "tfhd"
	boxTfdt = "tfdt"
	boxTrun = "trun"
	boxAvcC = "avcC"

	boxHeader      = 8
	boxLargeHeader = 16
	maxBoxSize     = 64 << 20

	tfhdDurationPresent    = 0x000008
	tfhdBaseOffsetPresent  = 0x000001
	tfhdDescriptionPresent = 0x000002
	tfhdSizePresent        = 0x000010
	tfhdFlagsPresent       = 0x000020
	trunDataOffsetPresent  = 0x000001
	trunFirstFlagsPresent  = 0x000004
	trunDurationPresent    = 0x000100
	trunSizePresent        = 0x000200
	trunFlagsPresent       = 0x000400
	trunCTSPresent         = 0x000800
	sampleIsNonSync        = 0x00010000

	codecAVC1 = "avc1"
	codecMP4A = "mp4a"
	codecAAC  = "mp4a.40.2"
)

var errBoxFormat = errors.New("malformed ISO BMFF box")

// readBox reads one whole box, header included, from r
func readBox(r *bufio.Reader) (string, []byte, error) {
	hdr, err := r.Peek(boxHeader)
	if err != nil {
		if errors.Is(err, io.EOF) && len(hdr) == 0 {
			return "", nil, io.EOF
		}
		return "", nil, io.ErrUnexpectedEOF
	}
	size := uint64(binary.BigEndian.Uint32(hdr))
	typ := string(hdr[4:8])
	if size == 1 {
		large, err := r.Peek(boxLargeHeader)
		if err != nil {
			return "", nil, io.ErrUnexpectedEOF
		}
		size = binary.BigEndian.Uint64(large[8:])
	}
	if size < boxHeader || size > maxBoxSize {
		return "", nil, fmt.Errorf("%w: %s of %d bytes", errBoxFormat, typ, size)
	}
	b := make([]byte, size)
	if _, err = io.ReadFull(r, b); err != nil {
		return "", nil, io.ErrUnexpectedEOF
	}
	return typ, b, nil
}

// children walks the boxes inside a container box's payload, calling fn with each child's type and whole bytes
func children(payload []byte, fn func(typ string, b []byte) error) error {
	for len(payload) > 0 {
		if len(payload) < boxHeader {
			return errBoxFormat
		}
		size := uint64(binary.BigEndian.Uint32(payload))
		hdr := uint64(boxHeader)
		if size == 1 {
			if len(payload) < boxLargeHeader {
				return errBoxFormat
			}
			size, hdr = binary.BigEndian.Uint64(payload[8:]), boxLargeHeader
		}
		if size < hdr || size > uint64(len(payload)) {
			return errBoxFormat
		}
		if err := fn(string(payload[4:8]), payload[:size]); err != nil {
			return err
		}
		payload = payload[size:]
	}
	return nil
}

// find returns the first box at the path below a container's payload
func find(payload []byte, path ...string) []byte {
	var out []byte
	_ = children(payload, func(typ string, b []byte) error {
		if typ != path[0] {
			return nil
		}
		if len(path) == 1 {
			out = b
		} else {
			out = find(b[boxHeader:], path[1:]...)
		}
		return errStop
	})
	return out
}

var errStop = errors.New("stop")

// trackInfo reads the timescale and codec of the single track an init segment's moov describes
func trackInfo(moov []byte) (timescale uint32, codec string, err error) {
	mdia := find(moov[boxHeader:], boxTrak, boxMdia)
	if mdia == nil {
		return 0, "", fmt.Errorf("%w: no trak/mdia", errBoxFormat)
	}
	mdhd := find(mdia[boxHeader:], boxMdhd)
	if len(mdhd) < boxHeader+24 {
		return 0, "", fmt.Errorf("%w: no mdhd", errBoxFormat)
	}
	if mdhd[boxHeader] == 1 {
		if len(mdhd) < boxHeader+32 {
			return 0, "", errBoxFormat
		}
		timescale = binary.BigEndian.Uint32(mdhd[boxHeader+20:])
	} else {
		timescale = binary.BigEndian.Uint32(mdhd[boxHeader+12:])
	}
	stsd := find(mdia[boxHeader:], boxMinf, boxStbl, boxStsd)
	// stsd is a full box with an entry count, then the sample entries
	if len(stsd) < boxHeader+8+boxHeader {
		return 0, "", fmt.Errorf("%w: no stsd", errBoxFormat)
	}
	entry := stsd[boxHeader+8:]
	switch string(entry[4:8]) {
	case codecAVC1:
		// a visual sample entry has 78 bytes of fields before its child boxes
		const visualFields = 78
		if len(entry) < boxHeader+visualFields {
			return 0, "", errBoxFormat
		}
		avcC := find(entry[boxHeader+visualFields:], boxAvcC)
		if len(avcC) < boxHeader+4 {
			return 0, "", fmt.Errorf("%w: no avcC", errBoxFormat)
		}
		p := avcC[boxHeader:]
		codec = fmt.Sprintf("%s.%02x%02x%02x", codecAVC1, p[1], p[2], p[3])
	case codecMP4A:
		codec = codecAAC
	default:
		return 0, "", fmt.Errorf("unsupported sample entry %q", entry[4:8])
	}
	return timescale, codec, nil
}

// fragmentTiming reads a moof's base decode time, total duration and whether its first sample is a sync sample
func fragmentTiming(moof []byte) (decodeTime, duration uint64, key bool, err error) {
	traf := find(moof[boxHeader:], boxTraf)
	if traf == nil {
		return 0, 0, false, fmt.Errorf("%w: no traf", errBoxFormat)
	}
	var defaultDuration, defaultFlags uint32
	if tfhd := find(traf[boxHeader:], boxTfhd); len(tfhd) >= boxHeader+8 {
		flags := binary.BigEndian.Uint32(tfhd[boxHeader:]) & 0xffffff
		p := tfhd[boxHeader+8:]
		skip := func(present uint32, n int) {
			if flags&present != 0 && len(p) >= n {
				p = p[n:]
			}
		}
		skip(tfhdBaseOffsetPresent, 8)
		skip(tfhdDescriptionPresent, 4)
		if flags&tfhdDurationPresent != 0 && len(p) >= 4 {
			defaultDuration, p = binary.BigEndian.Uint32(p), p[4:]
		}
		skip(tfhdSizePresent, 4)
		if flags&tfhdFlagsPresent != 0 && len(p) >= 4 {
			defaultFlags = binary.BigEndian.Uint32(p)
		}
	}
	if tfdt := find(traf[boxHeader:], boxTfdt); len(tfdt) >= boxHeader+8 {
		if tfdt[boxHeader] == 1 && len(tfdt) >= boxHeader+12 {
			decodeTime = binary.BigEndian.Uint64(tfdt[boxHeader+4:])
		} else {
			decodeTime = uint64(binary.BigEndian.Uint32(tfdt[boxHeader+4:]))
		}
	}
	trun := find(traf[boxHeader:], boxTrun)
	if len(trun) < boxHeader+8 {
		return 0, 0, false, fmt.Errorf("%w: no trun", errBoxFormat)
	}
	flags := binary.BigEndian.Uint32(trun[boxHeader:]) & 0xffffff
	count := binary.BigEndian.Uint32(trun[boxHeader+4:])
	p := trun[boxHeader+8:]
	if flags&trunDataOffsetPresent != 0 {
		p = p[min(4, len(p)):]
	}
	firstFlags := defaultFlags
	if flags&trunFirstFlagsPresent != 0 && len(p) >= 4 {
		firstFlags, p = binary.BigEndian.Uint32(p), p[4:]
	}
	per := 0
	for _, f := range []uint32{trunDurationPresent, trunSizePresent, trunFlagsPresent, trunCTSPresent} {
		if flags&f != 0 {
			per += 4
		}
	}
	for i := range count {
		if len(p) < per {
			return 0, 0, false, fmt.Errorf("%w: short trun", errBoxFormat)
		}
		d := defaultDuration
		q := p
		if flags&trunDurationPresent != 0 {
			d, q = binary.BigEndian.Uint32(q), q[4:]
		}
		if flags&trunSizePresent != 0 {
			q = q[4:]
		}
		if i == 0 && flags&trunFlagsPresent != 0 {
			firstFlags = binary.BigEndian.Uint32(q)
		}
		duration += uint64(d)
		p = p[per:]
	}
	return decodeTime, duration, firstFlags&sampleIsNonSync == 0, nil
}
