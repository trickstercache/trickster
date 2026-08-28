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

// Package sizeconv provides parsing of human-readable byte sizes
// (e.g., "256MB", "1.5GiB", "1024") into byte counts.
package sizeconv

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Size is a byte count that supports YAML (un)marshaling from
// human-readable strings; units are binary (1KB = 1024 bytes)
type Size int64

var ErrInvalidSizeFormat = errors.New("invalid size format")

const (
	kb = 1 << 10
	mb = 1 << 20
	gb = 1 << 30
	tb = 1 << 40
)

var units = map[string]int64{
	"":    1,
	"b":   1,
	"k":   kb,
	"kb":  kb,
	"kib": kb,
	"m":   mb,
	"mb":  mb,
	"mib": mb,
	"g":   gb,
	"gb":  gb,
	"gib": gb,
	"t":   tb,
	"tb":  tb,
	"tib": tb,
}

// ParseSize returns the byte count represented by the provided string,
// which is a number with an optional unit suffix (B, KB, MB, GB, TB)
func ParseSize(input string) (Size, error) {
	s := strings.TrimSpace(strings.ToLower(input))
	i := len(s)
	for i > 0 && (s[i-1] < '0' || s[i-1] > '9') && s[i-1] != '.' {
		i--
	}
	num, unit := s[:i], strings.TrimSpace(s[i:])
	mult, ok := units[unit]
	if !ok || num == "" {
		return 0, ErrInvalidSizeFormat
	}
	if !strings.Contains(num, ".") {
		v, err := strconv.ParseInt(num, 10, 64)
		if err != nil || v < 0 || v > math.MaxInt64/mult {
			return 0, ErrInvalidSizeFormat
		}
		return Size(v * mult), nil
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 {
		return 0, ErrInvalidSizeFormat
	}
	result := f * float64(mult)
	if math.IsInf(result, 0) || result >= float64(math.MaxInt64) {
		return 0, ErrInvalidSizeFormat
	}
	return Size(result), nil
}

func (s Size) String() string {
	v := int64(s)
	for _, u := range []struct {
		mult int64
		name string
	}{{tb, "TB"}, {gb, "GB"}, {mb, "MB"}, {kb, "KB"}} {
		if v != 0 && v%u.mult == 0 {
			return strconv.FormatInt(v/u.mult, 10) + u.name
		}
	}
	return strconv.FormatInt(v, 10)
}

// UnmarshalYAML unmarshals a number or size string into a Size
func (s *Size) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	parsed, err := ParseSize(raw)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}

// MarshalYAML marshals a Size to its most compact string format
func (s Size) MarshalYAML() (any, error) {
	return s.String(), nil
}
