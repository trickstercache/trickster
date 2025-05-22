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

package validate

import (
	"strconv"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config"
)

func TestLoadConfigurationFileFailures(t *testing.T) {

	tests := []struct {
		filename string
		expected string
	}{
		{ // Case 0
			"../../../testdata/test.missing-origin-url.conf",
			`missing origin-url for backend "test"`,
		},
		{ // Case 1
			"../../../testdata/test.bad_origin_url.conf",
			"first path segment in URL cannot contain colon",
		},
		{ // Case 2
			"../../../testdata/test.missing_backend_provider.conf",
			`missing provider for backend "test"`,
		},
		{ // Case 3
			"../../../testdata/test.bad-cache-name.conf",
			`invalid cache_name "test_fail" provided in backend options "test"`,
		},
		{ // Case 4
			"../../../testdata/test.invalid-negative-cache-1.conf",
			`invalid negative_cache config in default: a is not a valid HTTP status code >= 400 and < 600`,
		},
		{ // Case 5
			"../../../testdata/test.invalid-negative-cache-2.conf",
			`invalid negative_cache config in default: 1212 is not a valid HTTP status code >= 400 and < 600`,
		},
		{ // Case 6
			"../../../testdata/test.invalid-negative-cache-3.conf",
			`invalid negative_cache name: foo`,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			c, err := config.Load([]string{"-config", test.filename})
			if err != nil {
				t.Error(err)
			}
			err = Validate(c)
			if err == nil {
				t.Errorf("expected error `%s` got nothing", test.expected)
			} else if !strings.HasSuffix(err.Error(), test.expected) {
				t.Errorf("expected error `%s` got `%s`", test.expected, err.Error())
			}
		})
	}

}
