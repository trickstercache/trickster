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

package dataset

import (
	"encoding/json"
	"testing"
)

func TestTagsJSON_EscapesSpecialChars(t *testing.T) {
	tags := Tags{
		"path":  `/api/v1/query?q="a"`,
		"slash": `back\slash`,
		"plain": "ok",
	}
	got := tags.JSON()
	var out map[string]string
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("Tags.JSON produced invalid JSON: %v (out=%s)", err, got)
	}
	for k, want := range tags {
		if out[k] != want {
			t.Errorf("key %q: want %q, got %q", k, want, out[k])
		}
	}
}

func TestTags(t *testing.T) {
	tags := make(Tags)
	if tags.String() != "" {
		t.Error("expected empty string")
	}

	if len(tags.Keys()) > 0 {
		t.Error("expected empty list")
	}

	if tags.Size() > 48 {
		t.Errorf("expected 48 got %d", tags.Size())
	}

	tags["test2"] = "trickster"
	tags["test1"] = "value1"

	expected := `{"test1":"value1","test2":"trickster"}`
	if s := tags.JSON(); s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}

	expected = `"test1"="value1","test2"="trickster"`
	if s := tags.KVP(); s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}

	expected = "test1=value1;test2=trickster"
	if tags.String() != expected {
		t.Errorf("expected %s got %s", expected, tags.String())
	}

	k := tags.Keys()

	if len(k) != 2 {
		t.Errorf("expected %d got %d", 2, len(k))
	}

	if k[0] != "test1" && k[1] != "test2" {
		t.Error("key sort mismatch")
	}

	if tags.Size() != 393 {
		t.Errorf("expected 393 got %d", tags.Size())
	}

	t2 := tags.Clone()
	if t2["test2"] != "trickster" {
		t.Error("tags clone mismatch")
	}

	tags = Tags{}
	if s := tags.StringsWithSep("", ""); s != "" {
		t.Error("expected empty string got", s)
	}

	if s := tags.JSON(); s != "{}" {
		t.Error("expected {} got", s)
	}

	if s := tags.KVP(); s != "" {
		t.Error("expected empty string got", s)
	}

	t2.Merge(Tags{"test3": "value3"})

	if len(t2) != 3 {
		t.Error("expected 3, got", len(t2))
	}
}

func TestInjectTags(t *testing.T) {
	ds := testDataSet2()
	ds.Results[0].SeriesList[0].Header.Tags = nil

	tags := Tags{"trickster": "tag_injection_test"}

	ds.InjectTags(tags)

	if len(ds.Results[0].SeriesList[0].Header.Tags) != 1 {
		t.Errorf("expected %d got %d", 1, len(ds.Results[0].SeriesList[0].Header.Tags))
	}

	if len(ds.Results[1].SeriesList[1].Header.Tags) != 2 {
		t.Errorf("expected %d got %d", 2, len(ds.Results[1].SeriesList[1].Header.Tags))
	}
}

func TestStripTags(t *testing.T) {
	t.Run("strips specified keys and rehashes", func(t *testing.T) {
		s1 := &Series{
			Header: SeriesHeader{
				Name: "cpu",
				Tags: Tags{"region": "us-east-1", "env": "prod"},
			},
			Points: testPoints(),
		}
		s2 := &Series{
			Header: SeriesHeader{
				Name: "cpu",
				Tags: Tags{"region": "us-west-2", "env": "prod"},
			},
			Points: testPoints(),
		}

		ds1 := &DataSet{Results: Results{{SeriesList: SeriesList{s1}}}}
		ds2 := &DataSet{Results: Results{{SeriesList: SeriesList{s2}}}}

		// Before stripping, hashes differ due to different "region" values
		h1 := ds1.Results[0].SeriesList[0].Header.CalculateHash(true)
		h2 := ds2.Results[0].SeriesList[0].Header.CalculateHash(true)
		if h1 == h2 {
			t.Fatal("expected different hashes before stripping")
		}

		ds1.StripTags([]string{"region"})
		ds2.StripTags([]string{"region"})

		// After stripping, "region" is gone
		if _, ok := ds1.Results[0].SeriesList[0].Header.Tags["region"]; ok {
			t.Error("expected region tag to be stripped")
		}

		// "env" is preserved
		if ds1.Results[0].SeriesList[0].Header.Tags["env"] != "prod" {
			t.Error("expected env tag to be preserved")
		}

		// Hashes now match (both have only env=prod)
		h1 = ds1.Results[0].SeriesList[0].Header.CalculateHash()
		h2 = ds2.Results[0].SeriesList[0].Header.CalculateHash()
		if h1 != h2 {
			t.Error("expected identical hashes after stripping")
		}
	})

	t.Run("no-op with empty keys", func(t *testing.T) {
		ds := testDataSet()
		before := ds.Results[0].SeriesList[0].Header.Tags.Clone()
		ds.StripTags(nil)
		ds.StripTags([]string{})
		after := ds.Results[0].SeriesList[0].Header.Tags
		if len(before) != len(after) {
			t.Error("tags should be unchanged")
		}
	})

	t.Run("strip enables merge of previously distinct series", func(t *testing.T) {
		s1 := &Series{
			Header: SeriesHeader{Name: "up", Tags: Tags{"region": "a"}},
			Points: makeStringPoints(ev{100, "10"}),
		}
		s2 := &Series{
			Header: SeriesHeader{Name: "up", Tags: Tags{"region": "b"}},
			Points: makeStringPoints(ev{100, "20"}),
		}
		ds1 := &DataSet{Results: Results{{SeriesList: SeriesList{s1}}}}
		ds2 := &DataSet{Results: Results{{SeriesList: SeriesList{s2}}}}

		ds1.StripTags([]string{"region"})
		ds2.StripTags([]string{"region"})

		// Now merge with sum — should aggregate since hashes match
		ds1.MergeWithStrategy(true, int(MergeStrategySum), ds2)
		if ds1.SeriesCount() != 1 {
			t.Fatalf("expected 1 series after strip+merge, got %d", ds1.SeriesCount())
		}
		if ds1.Results[0].SeriesList[0].Points[0].Values[0] != "30" {
			t.Errorf("expected sum 30, got %v", ds1.Results[0].SeriesList[0].Points[0].Values[0])
		}
	})
}
