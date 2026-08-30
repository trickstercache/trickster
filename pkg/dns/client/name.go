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

package client

import "strings"

const (
	maxNameLen  = 255
	maxLabelLen = 63
	// ptrMask marks a length byte as a compression pointer; the remaining
	// 14 bits are the offset of the name being referenced
	ptrMask = 0xC0
	// maxPtrJumps bounds pointer chasing so a cyclic message cannot spin
	maxPtrJumps = 16
)

// Fqdn returns name with the trailing dot the wire format requires
func Fqdn(name string) string {
	if name == "" {
		return "."
	}
	if strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}

// packName appends name to dst in label form. Names are written uncompressed,
// which every resolver accepts and keeps queries self-contained.
func packName(dst []byte, name string) ([]byte, error) {
	if name == "" || name == "." {
		return append(dst, 0), nil
	}
	name = strings.TrimSuffix(name, ".")
	total := 1
	for label := range strings.SplitSeq(name, ".") {
		n := len(label)
		if n == 0 || n > maxLabelLen {
			return nil, ErrInvalidName
		}
		total += n + 1
		if total > maxNameLen {
			return nil, ErrInvalidName
		}
		dst = append(dst, byte(n))
		dst = append(dst, label...)
	}
	return append(dst, 0), nil
}

// unpackName reads the name at off, following compression pointers, and
// returns it with a trailing dot along with the offset just past the name
func unpackName(msg []byte, off int) (string, int, error) {
	var sb strings.Builder
	next := -1
	var jumps int
	for {
		if off < 0 || off >= len(msg) {
			return "", 0, ErrShortMessage
		}
		l := int(msg[off])
		switch {
		case l == 0:
			off++
			if next < 0 {
				next = off
			}
			if sb.Len() == 0 {
				return ".", next, nil
			}
			return sb.String(), next, nil
		case l&ptrMask == ptrMask:
			if off+1 >= len(msg) {
				return "", 0, ErrShortMessage
			}
			ptr := (l&^ptrMask)<<8 | int(msg[off+1])
			if next < 0 {
				next = off + 2
			}
			if jumps++; jumps > maxPtrJumps {
				return "", 0, ErrNameLoop
			}
			off = ptr
		case l > maxLabelLen:
			// 0b01 and 0b10 length prefixes are reserved and unusable
			return "", 0, ErrInvalidName
		default:
			off++
			if off+l > len(msg) {
				return "", 0, ErrShortMessage
			}
			if sb.Len()+l+1 > maxNameLen {
				return "", 0, ErrInvalidName
			}
			writeLabel(&sb, msg[off:off+l])
			sb.WriteByte('.')
			off += l
		}
	}
}

// writeLabel appends a label to sb in presentation form, escaping the
// characters that would otherwise be read as label separators or be unprintable
func writeLabel(sb *strings.Builder, label []byte) {
	for _, c := range label {
		switch {
		case c == '.' || c == '\\':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case c < '!' || c > '~':
			sb.WriteByte('\\')
			sb.WriteByte('0' + c/100)
			sb.WriteByte('0' + (c/10)%10)
			sb.WriteByte('0' + c%10)
		default:
			sb.WriteByte(c)
		}
	}
}
