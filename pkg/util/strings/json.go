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

package strings

import (
	"encoding/json"
	"math"
	"strconv"
	"unicode/utf8"
)

const hexDigits = "0123456789abcdef"

// the ASCII bytes a JSON string holds as they are, which excludes those that are HTML-sensitive
var jsonSafe = func() (t [utf8.RuneSelf]bool) {
	for c := 0x20; c < utf8.RuneSelf; c++ {
		switch c {
		case '"', '\\', '<', '>', '&':
		default:
			t[c] = true
		}
	}
	return t
}()

// AppendJSON appends s as a quoted JSON string escaped as encoding/json's Marshal does, including
// HTML-sensitive characters and U+2028/U+2029, with invalid UTF-8 as U+FFFD
func AppendJSON(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if jsonSafe[c] {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch c {
			case '"', '\\':
				dst = append(dst, '\\', c)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			dst = append(dst, s[start:i]...)
			dst = utf8.AppendRune(dst, utf8.RuneError)
		case r == ' ' || r == ' ':
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[r&0xf])
		default:
			i += size
			continue
		}
		i += size
		start = i
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// AppendJSONFloat appends f as encoding/json writes a float of bit size 32 or 64; ok is false, and
// dst unchanged, for NaN and the infinities, which JSON can't hold
func AppendJSONFloat(dst []byte, f float64, bits int) ([]byte, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return dst, false
	}
	format := byte('f')
	if abs := math.Abs(f); abs != 0 {
		if bits == 64 && (abs < 1e-6 || abs >= 1e21) ||
			bits == 32 && (float32(abs) < 1e-6 || float32(abs) >= 1e21) {
			format = 'e'
		}
	}
	dst = strconv.AppendFloat(dst, f, format, -1, bits)
	if format == 'e' {
		// a one-digit negative exponent is written without its leading zero, as e-9
		if n := len(dst); n >= 4 && dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
			dst[n-2] = dst[n-1]
			dst = dst[:n-1]
		}
	}
	return dst, true
}

// AppendJSONValue appends v to dst as encoding/json's Marshal writes it, and fails where it fails.
// Scalars are appended directly, and anything else is marshaled.
func AppendJSONValue(dst []byte, v any) ([]byte, error) {
	switch t := v.(type) {
	case nil:
		return append(dst, "null"...), nil
	case string:
		return AppendJSON(dst, t), nil
	case bool:
		return strconv.AppendBool(dst, t), nil
	case float64:
		if out, ok := AppendJSONFloat(dst, t, 64); ok {
			return out, nil
		}
	case float32:
		if out, ok := AppendJSONFloat(dst, float64(t), 32); ok {
			return out, nil
		}
	case int64:
		return strconv.AppendInt(dst, t, 10), nil
	case int:
		return strconv.AppendInt(dst, int64(t), 10), nil
	case int32:
		return strconv.AppendInt(dst, int64(t), 10), nil
	case uint64:
		return strconv.AppendUint(dst, t, 10), nil
	case uint:
		return strconv.AppendUint(dst, uint64(t), 10), nil
	case uint32:
		return strconv.AppendUint(dst, uint64(t), 10), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return dst, err
	}
	return append(dst, b...), nil
}

// CheckJSONValue reports the error AppendJSONValue would return for v, without writing it, so a
// caller can check every value before writing any
func CheckJSONValue(v any) error {
	switch t := v.(type) {
	case nil, string, bool, int64, int, int32, uint64, uint, uint32:
		return nil
	case float64:
		if !math.IsNaN(t) && !math.IsInf(t, 0) {
			return nil
		}
	case float32:
		if f := float64(t); !math.IsNaN(f) && !math.IsInf(f, 0) {
			return nil
		}
	}
	_, err := json.Marshal(v)
	return err
}
