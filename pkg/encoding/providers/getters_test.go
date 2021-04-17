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

package providers

import (
	"strconv"
	"testing"
)

func TestGetEncoderInitializer(t *testing.T) {

	tests := []struct {
		provider      string
		expectNilFunc bool
		header        string
	}{
		{
			"unsupported",
			true,
			"",
		},
		{
			ZstandardValue,
			false,
			ZstandardValue,
		},
		{
			BrotliValue,
			false,
			BrotliValue,
		},
		{
			GZipValue,
			false,
			GZipValue,
		},
		{
			DeflateValue,
			false,
			DeflateValue,
		},
		{
			SnappyValue,
			false,
			"",
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			f, s := GetEncoderInitializer(test.provider)
			if s != test.header {
				t.Errorf("expected %s got %s", test.header, s)
			}
			if test.expectNilFunc && f != nil {
				t.Error("expected nil")
			}
		})
	}

	// capture invalid case
	f, s := SelectEncoderInitializer(64)
	if f != nil {
		t.Error("expected nil")
	}
	if s != "" {
		t.Errorf("expected empty string got %s", s)
	}
}

func TestGetDecoderInitializer(t *testing.T) {

	tests := []struct {
		provider      string
		expectNilFunc bool
	}{
		{
			"unsupported",
			true,
		},
		{
			ZstandardValue,
			false,
		},
		{
			BrotliValue,
			false,
		},
		{
			GZipValue,
			false,
		},
		{
			DeflateValue,
			false,
		},
		{
			SnappyValue,
			false,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			f := GetDecoderInitializer(test.provider)
			if test.expectNilFunc && f != nil {
				t.Error("expected nil")
			}
		})
	}
	// capture invalid case
	f := SelectDecoderInitializer(64)
	if f != nil {
		t.Error("expected nil")
	}
}
