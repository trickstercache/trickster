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

import "testing"

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
