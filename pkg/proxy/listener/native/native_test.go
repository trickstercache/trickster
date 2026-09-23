/*
 * Copyright 2026 The Trickster Authors
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package native

import "testing"

type testAdapter struct {
	Adapter
	protocol string
}

func (a *testAdapter) Protocol() string              { return a.protocol }
func (*testAdapter) ServesProvider(name string) bool { return name == "shared" }

func TestProtocolQualifiedProvider(t *testing.T) {
	pg, my := &testAdapter{protocol: "postgres"}, &testAdapter{protocol: "mysql"}
	r := Registry{"postgres": pg, "mysql": my}
	if r.GetByProvider("shared") != nil {
		t.Fatal("ambiguous provider lookup must not depend on map iteration order")
	}
	if r.GetForProvider("postgres", "shared") != pg || r.GetForProvider("mysql", "shared") != my {
		t.Fatal("protocol-qualified lookup did not preserve both adapters")
	}
	if r.GetForProvider("http", "shared") != nil || r.GetForProvider("mysql", "unknown") != nil {
		t.Fatal("unsupported protocol/provider was accepted")
	}
	all := r.ForProvider("shared")
	if len(all) != 2 || all[0] != my || all[1] != pg || len(r.ForProvider("unknown")) != 0 {
		t.Fatal("provider adapters must be sorted and complete")
	}
	delete(r, "mysql")
	if r.GetByProvider("shared") != pg {
		t.Fatal("unique provider lookup changed")
	}
}
