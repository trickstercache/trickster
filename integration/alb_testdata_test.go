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

package integration

import (
	"embed"
	"testing"
)

//go:embed testdata/alb_cache testdata/alb_tsm_correctness testdata/alb_response_headers testdata/alb_nested testdata/alb_per_path testdata/alb_unavail testdata/alb_request_headers testdata/alb_compose
var albTestdataFS embed.FS

func albTestdata(t testing.TB, name string) string {
	t.Helper()
	b, err := albTestdataFS.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read embedded testdata %q: %v", name, err)
	}
	return string(b)
}
