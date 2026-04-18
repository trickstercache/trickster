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

package server

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"net"
	"strconv"
	"strings"
	"time"
)

// writeValue encodes a single value according to its ClickHouse type.
//
//nolint:gosec // intentional wire protocol integer conversions throughout
func writeValue(w *protoWriter, colType string, v any) error {
	switch {
	case colType == "UInt8" || colType == "Bool":
		return w.putByte(toNumeric[uint8](v))
	case colType == "UInt16":
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], toNumeric[uint16](v))
		_, err := w.Write(b[:])
		return err
	case colType == "UInt32":
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], toNumeric[uint32](v))
		_, err := w.Write(b[:])
		return err
	case colType == "UInt64":
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], toNumeric[uint64](v))
		_, err := w.Write(b[:])
		return err
	case colType == "Int8":
		return w.putByte(byte(toNumeric[int8](v)))
	case colType == "Int16":
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(toNumeric[int16](v))) //nolint:gosec
		_, err := w.Write(b[:])
		return err
	case colType == "Int32":
		return w.putInt32(toNumeric[int32](v))
	case colType == "Int64":
		return w.putInt64(toNumeric[int64](v))
	case colType == "Float32":
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], math.Float32bits(toNumeric[float32](v)))
		_, err := w.Write(b[:])
		return err
	case colType == "Float64":
		return w.putFloat64(toNumeric[float64](v))
	case colType == "DateTime":
		return w.putInt32(int32(toDateTime(v))) //nolint:gosec
	case strings.HasPrefix(colType, "DateTime64"):
		return w.putInt64(toDateTime64(v))
	case colType == "Date":
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], toDate(v))
		_, err := w.Write(b[:])
		return err
	case colType == "Date32":
		return w.putInt32(toDate32(v))
	case colType == "UUID":
		return writeUUID(w, v)
	case colType == "IPv4":
		return writeIPv4(w, v)
	case colType == "IPv6":
		return writeIPv6(w, v)
	case colType == "Enum8" || strings.HasPrefix(colType, "Enum8("):
		return w.putByte(byte(toNumeric[int8](v)))
	case colType == "Enum16" || strings.HasPrefix(colType, "Enum16("):
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(toNumeric[int16](v))) //nolint:gosec
		_, err := w.Write(b[:])
		return err
	case colType == "Int128" || colType == "UInt128":
		return writeBigInt(w, v, 16)
	case colType == "Int256" || colType == "UInt256":
		return writeBigInt(w, v, 32)
	case strings.HasPrefix(colType, "Decimal32"):
		return w.putInt32(toNumeric[int32](v))
	case strings.HasPrefix(colType, "Decimal64"):
		return w.putInt64(toNumeric[int64](v))
	case strings.HasPrefix(colType, "Decimal128"):
		return writeBigInt(w, v, 16)
	case strings.HasPrefix(colType, "Decimal256"):
		return writeBigInt(w, v, 32)
	case strings.HasPrefix(colType, "Decimal("):
		return w.putInt64(toNumeric[int64](v))
	default:
		return w.putStr(toString(v))
	}
}

// toNumeric converts an any value to a numeric type T.
//
//nolint:gosec // intentional wire protocol conversions
func toNumeric[T interface {
	~int8 | ~int16 | ~int32 | ~int64 | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~float32 | ~float64
}](v any) T {
	switch x := v.(type) {
	case float64:
		return T(x)
	case float32:
		return T(x)
	case int64:
		return T(x)
	case int32:
		return T(x)
	case int:
		return T(x)
	case uint64:
		return T(x)
	case uint32:
		return T(x)
	case uint16:
		return T(x)
	case uint8:
		return T(x)
	default:
		var zero T
		return zero
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return fmt.Sprint(v)
	}
}

//nolint:gosec // intentional wire protocol conversions
func toDateTime(v any) uint32 {
	switch x := v.(type) {
	case time.Time:
		return uint32(x.Unix())
	case uint32:
		return x
	case float64:
		return uint32(x)
	default:
		return uint32(toNumeric[int64](v))
	}
}

//nolint:gosec // intentional wire protocol conversions
func toDate(v any) uint16 {
	switch x := v.(type) {
	case time.Time:
		epoch := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
		return uint16(x.Sub(epoch).Hours() / 24)
	case uint16:
		return x
	default:
		return uint16(toNumeric[int64](v))
	}
}

//nolint:gosec // intentional wire protocol conversions
func toDate32(v any) int32 {
	switch x := v.(type) {
	case time.Time:
		epoch := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
		return int32(x.Sub(epoch).Hours() / 24)
	case int32:
		return x
	default:
		return toNumeric[int32](v)
	}
}

//nolint:gosec // intentional wire protocol conversions
func toDateTime64(v any) int64 {
	switch x := v.(type) {
	case time.Time:
		return x.UnixMilli()
	default:
		return toNumeric[int64](x)
	}
}

// writeBigInt writes an integer as a little-endian byte sequence of the given
// size (16 for Int128/UInt128, 32 for Int256/UInt256).
func writeBigInt(w *protoWriter, v any, size int) error {
	bi := toBigInt(v)
	b := make([]byte, size)
	if bi.Sign() >= 0 {
		raw := bi.Bytes()
		for i, j := 0, len(raw)-1; j >= 0 && i < size; i, j = i+1, j-1 {
			b[i] = raw[j]
		}
	} else {
		tc := new(big.Int).Add(bi, new(big.Int).Lsh(big.NewInt(1), uint(size*8))) //nolint:gosec // size is always 16 or 32
		raw := tc.Bytes()
		for i, j := 0, len(raw)-1; j >= 0 && i < size; i, j = i+1, j-1 {
			b[i] = raw[j]
		}
		for i := len(raw); i < size; i++ {
			b[i] = 0xFF
		}
	}
	_, err := w.Write(b)
	return err
}

func toBigInt(v any) *big.Int {
	switch x := v.(type) {
	case *big.Int:
		return x
	case int64:
		return big.NewInt(x)
	case uint64:
		return new(big.Int).SetUint64(x)
	case float64:
		bi, _ := new(big.Int).SetString(strconv.FormatFloat(x, 'f', 0, 64), 10)
		if bi == nil {
			return big.NewInt(0)
		}
		return bi
	case string:
		bi, ok := new(big.Int).SetString(x, 10)
		if !ok {
			return big.NewInt(0)
		}
		return bi
	default:
		return big.NewInt(0)
	}
}

// writeUUID writes a UUID as 16 bytes. ClickHouse stores UUIDs as two
// little-endian uint64s with the halves swapped relative to RFC 4122.
func writeUUID(w *protoWriter, v any) error {
	s := toString(v)
	s = strings.ReplaceAll(s, "-", "")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		b = make([]byte, 16)
	}
	var buf [16]byte
	binary.LittleEndian.PutUint64(buf[0:8], binary.BigEndian.Uint64(b[0:8]))
	binary.LittleEndian.PutUint64(buf[8:16], binary.BigEndian.Uint64(b[8:16]))
	_, err = w.Write(buf[:])
	return err
}

// writeIPv4 writes an IPv4 address as a 4-byte little-endian uint32.
func writeIPv4(w *protoWriter, v any) error {
	s := toString(v)
	ip := net.ParseIP(s)
	if ip == nil {
		ip = net.IPv4zero
	}
	ip4 := ip.To4()
	if ip4 == nil {
		ip4 = net.IPv4zero.To4()
	}
	var b [4]byte
	b[0] = ip4[3]
	b[1] = ip4[2]
	b[2] = ip4[1]
	b[3] = ip4[0]
	_, err := w.Write(b[:])
	return err
}

// writeIPv6 writes an IPv6 address as 16 bytes in network byte order.
func writeIPv6(w *protoWriter, v any) error {
	s := toString(v)
	ip := net.ParseIP(s)
	if ip == nil {
		ip = net.IPv6zero
	}
	ip16 := ip.To16()
	if ip16 == nil {
		ip16 = net.IPv6zero
	}
	_, err := w.Write([]byte(ip16))
	return err
}
