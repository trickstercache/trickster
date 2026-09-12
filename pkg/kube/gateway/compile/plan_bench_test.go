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

package compile

import (
	"net/http"
	"strconv"
	"testing"

	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
)

// plannerModel builds hosts hostnames, each with a plain root prefix, paths
// header-predicated paths and a method-specific path leaving methods to fill
func plannerModel(hosts, paths int) *ir.IR {
	var routes []ir.Route
	var groups []ir.BackendGroup
	rank := 0
	for h := range hosts {
		host := "h" + strconv.Itoa(h) + ".example.com"
		rootRule, root := serviceRule("root"+strconv.Itoa(h), 0, "root", prefix("/"))
		routes = append(routes, hostRoute("root"+strconv.Itoa(h), host, rank, rootRule))
		groups = append(groups, root)
		rank++
		for p := range paths {
			name := "p" + strconv.Itoa(h) + "x" + strconv.Itoa(p)
			rule, g := serviceRule(name, 0, "svc"+strconv.Itoa(p),
				withHeaders(exact("/api/"+strconv.Itoa(p)), "X-Tenant", strconv.Itoa(p)),
				withMethods(exact("/only/"+strconv.Itoa(p)), http.MethodGet))
			routes = append(routes, hostRoute(name, host, rank, rule))
			groups = append(groups, g)
			rank++
		}
	}
	return model(routes, groups...)
}

func benchmarkPlanner(b *testing.B, hosts, paths int) {
	m := plannerModel(hosts, paths)
	opts := kubecfg.New()
	opts.Defaults.RoutingMode = kubecfg.RoutingModeService
	if _, err := Compile(m, opts); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := Compile(m, opts); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPlanner_Hosts10_Paths10(b *testing.B)  { benchmarkPlanner(b, 10, 10) }
func BenchmarkPlanner_Hosts50_Paths20(b *testing.B)  { benchmarkPlanner(b, 50, 20) }
func BenchmarkPlanner_Hosts200_Paths10(b *testing.B) { benchmarkPlanner(b, 200, 10) }
