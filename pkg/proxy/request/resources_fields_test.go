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

package request

import (
	"reflect"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"go.opentelemetry.io/otel/trace"
)

// every Resources field is either copied by Clone / Merge or listed here with the reason it is not,
// so a new field can't silently go uncloned or unmerged
var (
	notCloned = map[string]string{
		"Response": "each clone makes its own origin exchange",
	}
	notMerged = map[string]string{
		"TS":                    "set by the route's engine after it is entered",
		"TSUnmarshaler":         "set by the route's handler after it is entered",
		"TSMarshaler":           "set by the route's handler after it is entered",
		"TSTransformer":         "set by the route's handler after it is entered",
		"TSReqestOptions":       "set by the route's engine after it is entered",
		"TSMergeStrategy":       "set by the route's handler after it is entered",
		"TSDedupToleranceNanos": "set by the route's handler after it is entered",
		"Response":              "set by the route's engine after it is entered",
		"HiddenResult":          "the outer request's access log state, kept across routes",
		"UpstreamAddr":          "the outer request's access log state, kept across routes",
		"UpstreamStatus":        "the outer request's access log state, kept across routes",
		"UpstreamDuration":      "the outer request's access log state, kept across routes",
		"SpanContext":           "the outer request's access log state, kept across routes",
	}
)

type (
	fakeCache      struct{ cache.Cache }
	fakeBackend    struct{ backends.Backend }
	fakeTimeseries struct{ timeseries.Timeseries }
)

var fakeInterfaceValues = []any{&fakeCache{}, &fakeBackend{}, &fakeTimeseries{}}

func filledResources(t *testing.T) *Resources {
	t.Helper()
	r := &Resources{}
	v := reflect.ValueOf(r).Elem()
	for i := range v.NumField() {
		f := v.Type().Field(i)
		if f.Anonymous {
			continue // the embedded mutex
		}
		setNonZero(t, f.Name, v.Field(i))
	}
	return r
}

func setNonZero(t *testing.T, name string, fv reflect.Value) {
	t.Helper()
	ft := fv.Type()
	switch fv.Kind() {
	case reflect.Pointer:
		fv.Set(reflect.New(ft.Elem()))
	case reflect.Func:
		fv.Set(reflect.MakeFunc(ft, func([]reflect.Value) []reflect.Value {
			out := make([]reflect.Value, ft.NumOut())
			for i := range out {
				out[i] = reflect.Zero(ft.Out(i))
			}
			return out
		}))
	case reflect.Slice:
		s := reflect.MakeSlice(ft, 1, 1)
		setNonZero(t, name, s.Index(0))
		fv.Set(s)
	case reflect.Bool:
		fv.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		fv.SetInt(7)
	case reflect.Uint8:
		fv.SetUint(7)
	case reflect.String:
		fv.SetString(name)
	case reflect.Interface:
		for _, fake := range fakeInterfaceValues {
			if reflect.TypeOf(fake).Implements(ft) {
				fv.Set(reflect.ValueOf(fake))
				return
			}
		}
		t.Fatalf("no fake value implements %s for field %s", ft, name)
	case reflect.Struct:
		if ft != reflect.TypeFor[trace.SpanContext]() {
			t.Fatalf("no filler for struct field %s of type %s", name, ft)
		}
		fv.Set(reflect.ValueOf(trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1},
		})))
	default:
		t.Fatalf("no filler for field %s of kind %s", name, fv.Kind())
	}
}

func sameFieldValue(a, b reflect.Value) bool {
	switch a.Kind() {
	case reflect.Func:
		return !a.IsNil() && !b.IsNil()
	case reflect.Slice, reflect.Struct:
		return reflect.DeepEqual(a.Interface(), b.Interface())
	default:
		return a.Interface() == b.Interface()
	}
}

func checkFieldCoverage(t *testing.T, op string, src, dst *Resources, excluded map[string]string) {
	t.Helper()
	sv, dv := reflect.ValueOf(src).Elem(), reflect.ValueOf(dst).Elem()
	seen := make(map[string]bool, len(excluded))
	for i := range sv.NumField() {
		f := sv.Type().Field(i)
		if f.Anonymous {
			continue
		}
		if _, ok := excluded[f.Name]; ok {
			seen[f.Name] = true
			if !dv.Field(i).IsZero() {
				t.Errorf("%s copied %s, which is listed as not copied", op, f.Name)
			}
			continue
		}
		if !sameFieldValue(sv.Field(i), dv.Field(i)) {
			t.Errorf("%s did not copy %s; copy it or list it as excluded with a reason", op, f.Name)
		}
	}
	for name := range excluded {
		if !seen[name] {
			t.Errorf("%s exclusion %s names no Resources field", op, name)
		}
	}
}

func TestCloneFieldCoverage(t *testing.T) {
	src := filledResources(t)
	checkFieldCoverage(t, "Clone", src, src.Clone(), notCloned)
}

func TestMergeFieldCoverage(t *testing.T) {
	src := filledResources(t)
	dst := &Resources{}
	dst.Merge(src)
	checkFieldCoverage(t, "Merge", src, dst, notMerged)
}
