/*
 * Copyright 2026 The Trickster Authors
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
package sticky

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

func tableOptions(t *testing.T, mutate func(*options.Options)) *options.Options {
	t.Helper()
	o := &options.Options{Mode: options.ModeTable}
	if mutate != nil {
		mutate(o)
	}
	if err := o.Initialize(); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestTableCarriesAcrossReloads(t *testing.T) {
	const name = "carry-test"
	t.Cleanup(func() { ForgetTablesExcept(func(string) bool { return false }) })

	first := TableFor(name, tableOptions(t, nil))
	first.Put(1, leaf, 0)
	if TableFor(name, tableOptions(t, nil)) != first {
		t.Fatal("an unchanged sticky block did not keep its table")
	}
	// options that do not shape a table leave it be
	kept := TableFor(name, tableOptions(t, func(o *options.Options) { o.OnUnavailable = options.OnUnavailableReject }))
	if kept != first {
		t.Error("changing on_unavailable dropped the table")
	}
	if got, ok := kept.Get(1, 0); !ok || got != leaf {
		t.Error("a carried table lost its pins")
	}
	if TableFor("carry-test-other", tableOptions(t, nil)) == first {
		t.Error("another ALB was given this one's table")
	}

	for change, mutate := range map[string]func(*options.Options){
		"key":         func(o *options.Options) { o.Table.Key = "header:X-Tenant" },
		"learn":       func(o *options.Options) { o.Table.Key, o.Table.Learn = "cookie:s", options.LearnResponse },
		"ipv6_prefix": func(o *options.Options) { o.Table.IPv6Prefix = 48 },
		"max_entries": func(o *options.Options) { o.Table.MaxEntries = 10 },
		"ttl":         func(o *options.Options) { ttl := timeconv.Duration(time.Minute); o.TTL = &ttl },
		"idle":        func(o *options.Options) { o.Idle = timeconv.Duration(time.Minute) },
		"mode":        func(o *options.Options) { o.Mode = "" },
	} {
		before := TableFor(name, tableOptions(t, nil))
		changed := TableFor(name, tableOptions(t, mutate))
		if changed == before {
			t.Errorf("changing %s kept the table", change)
		}
		if _, ok := changed.Get(1, 0); ok {
			t.Errorf("the table made after changing %s holds an old pin", change)
		}
	}
}

func TestTableForMakesItUnderTheOptions(t *testing.T) {
	t.Cleanup(func() { ForgetTablesExcept(func(string) bool { return false }) })
	tbl := TableFor("made-test", tableOptions(t, func(o *options.Options) {
		ttl := timeconv.Duration(10 * time.Second)
		o.TTL, o.Idle, o.Table.MaxEntries = &ttl, timeconv.Duration(5*time.Second), 10
	}))
	if tbl.MaxEntries() != 10 {
		t.Errorf("a table of max_entries 10 holds %d", tbl.MaxEntries())
	}
	tbl.Put(1, leaf, 0)
	if _, ok := tbl.Get(1, int64(4*time.Second)); !ok {
		t.Fatal("expired before its idle timeout")
	}
	if _, ok := tbl.Get(1, int64(10*time.Second)); ok {
		t.Error("outlived its ttl")
	}
}

func TestForgetTablesExcept(t *testing.T) {
	t.Cleanup(func() { ForgetTablesExcept(func(string) bool { return false }) })
	a := TableFor("forget-a", tableOptions(t, nil))
	b := TableFor("forget-b", tableOptions(t, nil))
	ForgetTablesExcept(func(name string) bool { return name == "forget-b" })
	if TableFor("forget-a", tableOptions(t, nil)) == a {
		t.Error("a forgotten ALB's table was carried")
	}
	if TableFor("forget-b", tableOptions(t, nil)) != b {
		t.Error("a kept ALB's table was dropped")
	}
}
