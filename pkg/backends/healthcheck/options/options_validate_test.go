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

package options

import (
	"testing"
	"time"

	ct "github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func TestValidateSuccess(t *testing.T) {
	t.Parallel()

	o := &Options{
		Verb:              "GET",
		Scheme:            "https",
		Host:              "hc.example.com",
		Path:              "/health",
		Timeout:           timeconv.Duration(2 * time.Second),
		ExpectedCodes:     []int{200, 204},
		FailureThreshold:  1,
		RecoveryThreshold: 2,
	}
	ok, err := o.Validate()
	if !ok || err != nil {
		t.Fatalf("Validate() = (%v, %v)", ok, err)
	}
}

func TestValidateErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		o    *Options
	}{
		{
			name: "invalid verb",
			o:    &Options{Verb: "NOT_A_METHOD"},
		},
		{
			name: "invalid scheme",
			o:    &Options{Scheme: "ftp"},
		},
		{
			name: "timeout too low",
			o:    &Options{Timeout: timeconv.Duration(MinProbeWait - 1)},
		},
		{
			name: "timeout too high",
			o:    &Options{Timeout: timeconv.Duration(MaxProbeWait + time.Second)},
		},
		{
			name: "invalid expected code",
			o:    &Options{ExpectedCodes: []int{99}},
		},
		{
			name: "host without scheme",
			o:    &Options{Host: "hc.example.com"},
		},
		{
			name: "negative failure threshold",
			o:    &Options{FailureThreshold: -1},
		},
		{
			name: "negative recovery threshold",
			o:    &Options{RecoveryThreshold: -1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ok, err := tt.o.Validate()
			if ok || err == nil {
				t.Fatalf("Validate() = (%v, %v), want error", ok, err)
			}
		})
	}
}

func TestCloneCopiesSlicesAndMaps(t *testing.T) {
	t.Parallel()

	o := &Options{
		Verb:            "GET",
		ExpectedCodes:   []int{200},
		Headers:         ct.EnvStringMap{"X-Test": "1"},
		ExpectedHeaders: map[string]string{headers.NameContentType: "text/plain"},
	}
	cl := o.Clone()
	if cl == o {
		t.Fatal("Clone returned same pointer")
	}
	cl.ExpectedCodes[0] = 500
	if o.ExpectedCodes[0] != 200 {
		t.Fatal("ExpectedCodes should be copied")
	}
	cl.Headers["X-Test"] = "2"
	if o.Headers["X-Test"] != "1" {
		t.Fatal("Headers should be copied")
	}
	cl.ExpectedHeaders[headers.NameContentType] = "application/json"
	if o.ExpectedHeaders[headers.NameContentType] != "text/plain" {
		t.Fatal("ExpectedHeaders should be copied")
	}
}
