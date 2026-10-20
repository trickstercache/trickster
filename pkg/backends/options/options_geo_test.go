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

package options

import (
	"errors"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	geoaclopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

func TestValidateGeoACLNames(t *testing.T) {
	const aclName, pathACLName = "north-america", "europe"
	acls := geoaclopts.Lookup{aclName: {Name: aclName}, pathACLName: {Name: pathACLName}}
	o := New()
	o.Name = "web"
	o.GeoACLName = aclName
	o.Paths = po.List{
		{Path: "/eu/", GeoACLName: pathACLName},
		{Path: "/open/", GeoACLName: reserved.ReferenceNone},
		{Path: "/inherits/"},
	}
	l := Lookup{o.Name: o, "nil": nil}
	if err := l.ValidateGeoACLNames(acls); err != nil {
		t.Fatal(err)
	}
	if o.GeoACLOptions != acls[aclName] || o.Paths[0].GeoACLOptions != acls[pathACLName] ||
		o.Paths[1].GeoACLOptions != nil || o.Paths[2].GeoACLOptions != nil {
		t.Fatal("geo ACL references were not resolved")
	}
	// clones share the geo ACL, so what is compiled into it reaches each copy
	if c := o.Clone(); c.GeoACLOptions != o.GeoACLOptions || c.Paths[0].GeoACLOptions != o.Paths[0].GeoACLOptions {
		t.Fatal("a clone does not share its geo ACL")
	}

	var target *ErrInvalidGeoACLName
	o.Paths[0].GeoACLName = "asia"
	if err := l.ValidateGeoACLNames(acls); !errors.As(err, &target) {
		t.Fatalf("an undefined path geo ACL: %v", err)
	}
	o.GeoACLName = reserved.ReferenceNone
	if err := l.ValidateGeoACLNames(acls); !errors.As(err, &target) {
		t.Fatalf("none on a backend: %v", err)
	}
}

func TestClearACLNames(t *testing.T) {
	o := New()
	o.GeoACLName, o.IPACLName, o.RateLimiterName = "north-america", "office", "per-client"
	o.Paths = po.List{{Path: "/eu/", GeoACLName: "europe", IPACLName: "none", RateLimiterName: "login"}, nil}
	o.ClearACLNames()
	if o.GeoACLName != "" || o.IPACLName != "" || o.RateLimiterName != "" ||
		o.Paths[0].GeoACLName != "" || o.Paths[0].IPACLName != "" || o.Paths[0].RateLimiterName != "" {
		t.Fatal("ACL names were not cleared")
	}
}
