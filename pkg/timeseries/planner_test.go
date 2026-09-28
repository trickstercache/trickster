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

package timeseries

import (
	"fmt"
	"testing"
	"time"
)

var plannedModes = []StepAlignment{
	StepAlignmentTruncate, StepAlignmentDrop, StepAlignmentPartial,
	StepAlignmentPartialStart, StepAlignmentPartialEnd,
}

func TestPlanRange(t *testing.T) {
	at := func(clock string) time.Time {
		v, err := time.Parse(time.DateTime, "2024-01-01 "+clock)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	now := at("10:30:30")
	ext := func(start, end string) Extent { return Extent{Start: at(start), End: at(end)} }
	startPB := func(label, lower, upper string) PartialBucket {
		return PartialBucket{Label: at(label), Lower: at(lower), Upper: at(upper), Edge: BucketEdgeStart}
	}
	endPB := func(label, upper string) PartialBucket {
		pb := PartialBucket{Label: at(label), Lower: at(label), Edge: BucketEdgeEnd}
		if upper != "" {
			pb.Upper = at(upper)
		}
		return pb
	}
	unaligned := RequestedRange{Start: at("10:00:15"), End: at("10:10:20")}
	tests := []struct {
		name     string
		r        RequestedRange
		model    SampleModel
		mode     StepAlignment
		interior Extent
		partials []PartialBucket
	}{
		{
			"aligned edges plan no partial buckets",
			RequestedRange{Start: at("10:00:00"), End: at("10:10:00")},
			SampleModelBucket, StepAlignmentPartial, ext("10:00:00", "10:09:00"), nil,
		},
		{
			"truncate widens the start and drops the end", unaligned, SampleModelBucket, StepAlignmentTruncate,
			ext("10:00:00", "10:09:00"), nil,
		},
		{
			"drop leaves both partial buckets out", unaligned, SampleModelBucket, StepAlignmentDrop,
			ext("10:01:00", "10:09:00"), nil,
		},
		{
			"partial fetches both edges", unaligned, SampleModelBucket, StepAlignmentPartial,
			ext("10:01:00", "10:09:00"),
			[]PartialBucket{
				startPB("10:00:00", "10:00:15", "10:01:00"), endPB("10:10:00", "10:10:20"),
			},
		},
		{
			"partial_start fetches the start only", unaligned, SampleModelBucket, StepAlignmentPartialStart,
			ext("10:01:00", "10:09:00"),
			[]PartialBucket{startPB("10:00:00", "10:00:15", "10:01:00")},
		},
		{
			"partial_end truncates the start", unaligned, SampleModelBucket, StepAlignmentPartialEnd,
			ext("10:00:00", "10:09:00"),
			[]PartialBucket{endPB("10:10:00", "10:10:20")},
		},
		{
			"open-ended drop ends before the live bucket",
			RequestedRange{Start: at("10:00:15"), End: at("10:30:30"), OpenEnded: true},
			SampleModelBucket, StepAlignmentDrop, ext("10:01:00", "10:29:00"), nil,
		},
		{
			"open-ended partial_end fetches the live bucket unbounded",
			RequestedRange{Start: at("10:00:15"), End: at("10:30:30"), OpenEnded: true},
			SampleModelBucket, StepAlignmentPartialEnd, ext("10:00:00", "10:29:00"),
			[]PartialBucket{endPB("10:30:00", "")},
		},
		{
			"an end after now keeps the client's end on the live bucket",
			RequestedRange{Start: at("10:00:00"), End: at("11:00:00")},
			SampleModelBucket, StepAlignmentPartialEnd, ext("10:00:00", "10:29:00"),
			[]PartialBucket{endPB("10:30:00", "11:00:00")},
		},
		{
			"an inclusive end on the grid starts a partial bucket",
			RequestedRange{Start: at("10:00:00"), End: at("10:10:00"), EndInclusive: true},
			SampleModelBucket, StepAlignmentPartial, ext("10:00:00", "10:09:00"),
			[]PartialBucket{{
				Label: at("10:10:00"), Lower: at("10:10:00"), Upper: at("10:10:00"),
				UpperInclusive: true, Edge: BucketEdgeEnd,
			}},
		},
		{
			"an exclusive start on the grid makes its bucket partial",
			RequestedRange{Start: at("10:00:00"), End: at("10:10:00"), StartExclusive: true},
			SampleModelBucket, StepAlignmentPartial, ext("10:01:00", "10:09:00"),
			[]PartialBucket{{
				Label: at("10:00:00"), Lower: at("10:00:00"), LowerExclusive: true, Upper: at("10:01:00"),
				Edge: BucketEdgeStart,
			}},
		},
		{
			"an exclusive start on the grid truncates to its bucket",
			RequestedRange{Start: at("10:00:00"), End: at("10:10:00"), StartExclusive: true},
			SampleModelBucket, StepAlignmentTruncate, ext("10:00:00", "10:09:00"), nil,
		},
		{
			"stop labels shift complete windows and label partial windows by their end", unaligned,
			SampleModelBucketStop, StepAlignmentPartial, ext("10:02:00", "10:10:00"),
			[]PartialBucket{
				{Label: at("10:01:00"), Lower: at("10:00:15"), Upper: at("10:01:00"), Edge: BucketEdgeStart},
				{Label: at("10:10:20"), Lower: at("10:10:00"), Upper: at("10:10:20"), Edge: BucketEdgeEnd},
			},
		},
		{
			"stop labels under truncate", unaligned, SampleModelBucketStop, StepAlignmentTruncate,
			ext("10:01:00", "10:10:00"), nil,
		},
		{"an unresolved mode plans as drop", unaligned, SampleModelBucket, 0, ext("10:01:00", "10:09:00"), nil},
		{
			"stop labels an open-ended live window by its end on the grid",
			RequestedRange{Start: at("10:00:00"), End: at("10:30:30"), OpenEnded: true},
			SampleModelBucketStop, StepAlignmentPartialEnd, ext("10:01:00", "10:30:00"),
			[]PartialBucket{{Label: at("10:31:00"), Lower: at("10:30:00"), Edge: BucketEdgeEnd}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p := PlanRange(test.r, time.Minute, 0, test.model, test.mode, now)
			if !p.Full {
				t.Fatal("expected complete buckets")
			}
			if p.Interior != test.interior {
				t.Errorf("interior: got %s want %s", p.Interior, test.interior)
			}
			if int(p.PartialCount) != len(test.partials) {
				t.Fatalf("partial buckets: got %+v want %+v", p.Partials[:p.PartialCount], test.partials)
			}
			for i, want := range test.partials {
				if p.Partials[i] != want {
					t.Errorf("partial bucket %d: got %+v want %+v", i, p.Partials[i], want)
				}
			}
		})
	}

	t.Run("phased grid", func(t *testing.T) {
		base := time.Unix(1_700_000_000, 0).Truncate(time.Hour)
		r := RequestedRange{Start: base.Add(20 * time.Minute), End: base.Add(160 * time.Minute)}
		for mode, want := range map[StepAlignment]Extent{
			StepAlignmentDrop:     {Start: base.Add(75 * time.Minute), End: base.Add(75 * time.Minute)},
			StepAlignmentTruncate: {Start: base.Add(15 * time.Minute), End: base.Add(75 * time.Minute)},
		} {
			p := PlanRange(r, time.Hour, 15*time.Minute, SampleModelBucket, mode, base.Add(24*time.Hour))
			if !p.Full || p.Interior != want {
				t.Errorf("%s: got %s (full %t) want %s", mode, p.Interior, p.Full, want)
			}
		}
	})

	t.Run("before the epoch", func(t *testing.T) {
		r := RequestedRange{Start: time.Unix(-90, 0), End: time.Unix(30, 0)}
		p := PlanRange(r, time.Minute, 0, SampleModelBucket, StepAlignmentTruncate, time.Unix(3600, 0))
		if want := (Extent{Start: time.Unix(-120, 0), End: time.Unix(-60, 0)}); !p.Full || p.Interior != want {
			t.Errorf("got %s want %s", p.Interior, want)
		}
	})

	t.Run("no complete bucket", func(t *testing.T) {
		for name, r := range map[string]RequestedRange{
			"inside one bucket":        {Start: at("10:00:15"), End: at("10:00:45")},
			"across one boundary":      {Start: at("10:00:15"), End: at("10:01:10")},
			"inside the live bucket":   {Start: at("10:30:05"), End: at("10:30:25")},
			"reaching only the live":   {Start: at("10:29:10"), End: at("10:30:30"), OpenEnded: true},
			"an inclusive single edge": {Start: at("10:00:00"), End: at("10:00:00"), EndInclusive: true},
		} {
			for _, mode := range plannedModes {
				if p := PlanRange(r, time.Minute, 0, SampleModelBucket, mode, now); p.Full || p.PartialCount != 0 {
					t.Errorf("%s under %s: got %+v", name, mode, p)
				}
			}
		}
		if p := PlanRange(unaligned, 0, 0, SampleModelBucket, StepAlignmentDrop, now); p.Full {
			t.Error("a non-positive step plans nothing")
		}
	})
}

func TestPartialBucketIsLive(t *testing.T) {
	now := time.Unix(630, 0)
	for _, test := range []struct {
		lower time.Time
		want  bool
	}{
		{time.Unix(600, 0), true},
		{time.Unix(615, 0), true},
		{time.Unix(540, 0), false},
		{time.Unix(599, 0), false},
	} {
		pb := PartialBucket{Lower: test.lower}
		if got := pb.IsLive(time.Minute, 0, now); got != test.want {
			t.Errorf("lower %d: got %t want %t", test.lower.Unix(), got, test.want)
		}
	}
}

func TestPlanEdges(t *testing.T) {
	now := time.Unix(10_000, 0)
	requested := RequestedRange{Start: time.Unix(630, 0), End: time.Unix(1_230, 0)}

	t.Run("bucketed queries are planned from the requested range", func(t *testing.T) {
		trq := &TimeRangeQuery{
			Step: time.Minute, SampleModel: SampleModelBucket, StepAlignment: StepAlignmentPartial,
			Requested: requested, Extent: Extent{Start: time.Unix(1, 0), End: time.Unix(2, 0)},
		}
		if !trq.PlanEdges(now) {
			t.Fatal("expected complete buckets")
		}
		if want := (Extent{Start: time.Unix(660, 0), End: time.Unix(1_140, 0)}); trq.Extent != want {
			t.Errorf("extent: got %s want %s", trq.Extent, want)
		}
		if trq.PartialCount != 2 || trq.Partials[0].Edge != BucketEdgeStart || trq.Partials[1].Edge != BucketEdgeEnd {
			t.Errorf("partial buckets: %+v", trq.Partials)
		}
	})

	t.Run("no complete bucket keeps the extent", func(t *testing.T) {
		original := Extent{Start: time.Unix(1, 0), End: time.Unix(2, 0)}
		trq := &TimeRangeQuery{
			Step: time.Minute, SampleModel: SampleModelBucket, StepAlignment: StepAlignmentPartial,
			Requested: RequestedRange{Start: time.Unix(630, 0), End: time.Unix(650, 0)}, Extent: original,
			PartialCount: 2,
		}
		if trq.PlanEdges(now) || trq.Extent != original || trq.PartialCount != 0 {
			t.Errorf("got %s with %d partial buckets", trq.Extent, trq.PartialCount)
		}
	})

	for name, trq := range map[string]*TimeRangeQuery{
		"instant samples":             {SampleModel: SampleModelInstant, Requested: requested},
		"stored samples":              {SampleModel: SampleModelStored, Requested: requested},
		"no recorded range":           {SampleModel: SampleModelBucket},
		"bucketed, non-positive step": {SampleModel: SampleModelBucket, Requested: requested},
	} {
		t.Run(name+" keep the aligned parser extent", func(t *testing.T) {
			if name != "bucketed, non-positive step" {
				trq.Step = time.Minute
			}
			trq.Extent = Extent{Start: time.Unix(630, 0), End: time.Unix(20_000, 0)}
			trq.StepAlignment, trq.PartialCount = StepAlignmentPartial, 1
			want := trq.Clone()
			want.alignExtent(now)
			if !trq.PlanEdges(now) || trq.Extent != want.Extent || trq.PartialCount != 0 {
				t.Errorf("got %s with %d partial buckets, want %s", trq.Extent, trq.PartialCount, want.Extent)
			}
		})
	}
}

func eachPlanCase(fn func(r RequestedRange, step, phase time.Duration, now time.Time)) {
	// aligned and unaligned edges, both comparators, open ends, ends after now, phases, and the epoch
	for _, step := range []time.Duration{time.Minute, time.Hour} {
		for _, phase := range []time.Duration{0, step / 4} {
			for _, base := range []time.Time{time.Unix(1_700_000_000, 0), time.Unix(-3*86400, 0)} {
				base = FloorToGrid(base, step, phase)
				offsets := []time.Duration{0, 1, step / 2, step - 1, step, step + step/3}
				now := base.Add(4*step + step/2)
				for _, so := range offsets {
					for bucket := range 4 {
						for _, eo := range offsets {
							end := base.Add(time.Duration(bucket)*step + eo)
							for flags := range 8 {
								r := RequestedRange{
									Start: base.Add(so), End: end,
									StartExclusive: flags&1 != 0, EndInclusive: flags&2 != 0,
								}
								if flags&4 != 0 {
									r.End, r.OpenEnded, r.EndInclusive = now, true, false
								}
								if r.End.Before(r.Start) {
									continue
								}
								fn(r, step, phase, now)
								fn(r, step, phase, end.Add(-step/3))
							}
						}
					}
				}
			}
		}
	}
}

func checkPlanInvariants(r RequestedRange, step, phase time.Duration, now time.Time) error {
	plans := make(map[StepAlignment]EdgePlan, len(plannedModes))
	for _, mode := range plannedModes {
		p := PlanRange(r, step, phase, SampleModelBucket, mode, now)
		plans[mode] = p
		if !p.Full {
			if p.PartialCount != 0 {
				return fmt.Errorf("%s: partial buckets without complete buckets", mode)
			}
			continue
		}
		in := p.Interior
		if !OnGrid(in.Start, step, phase) || !OnGrid(in.End, step, phase) || in.End.Before(in.Start) {
			return fmt.Errorf("%s: interior %s is not aligned", mode, in)
		}
		if in.End.Add(step).After(now) {
			return fmt.Errorf("%s: interior %s holds a bucket that has not ended by %s", mode, in, now)
		}
		start, end := mode.Edges()
		var hasStart, hasEnd bool
		for _, pb := range p.Partials[:p.PartialCount] {
			if !OnGrid(pb.Label, step, phase) {
				return fmt.Errorf("%s: partial label %s is off the grid", mode, pb.Label)
			}
			switch pb.Edge {
			case BucketEdgeStart:
				hasStart = true
				if start != EdgePartial || !pb.Label.Before(in.Start) || !pb.Lower.Equal(r.Start) ||
					!pb.Upper.Equal(in.Start) {
					return fmt.Errorf("%s: start partial %+v against interior %s", mode, pb, in)
				}
			case BucketEdgeEnd:
				hasEnd = true
				if end != EdgePartial || !pb.Label.After(in.End) || !pb.Lower.Equal(in.End.Add(step)) {
					return fmt.Errorf("%s: end partial %+v against interior %s", mode, pb, in)
				}
				if r.OpenEnded != pb.Upper.IsZero() || (!r.OpenEnded && !pb.Upper.Equal(r.End)) {
					return fmt.Errorf("%s: end partial %+v does not reach the client's end", mode, pb)
				}
			}
		}
		startAligned := !r.StartExclusive && OnGrid(r.Start, step, phase)
		endAligned := !r.OpenEnded && !r.EndInclusive && !r.End.After(now) && OnGrid(r.End, step, phase)
		if (startAligned && hasStart) || (endAligned && hasEnd) {
			return fmt.Errorf("%s: a partial bucket on an aligned edge", mode)
		}
		// under partial, the start partial, the interior and the end partial tile the range
		if mode == StepAlignmentPartial {
			if !hasStart && !in.Start.Equal(r.Start) {
				return fmt.Errorf("the interior %s does not start at %s", in, r.Start)
			}
			if !hasEnd && !in.End.Add(step).Equal(r.End) {
				return fmt.Errorf("the interior %s does not end at %s", in, r.End)
			}
		}
	}
	truncate, drop := plans[StepAlignmentTruncate], plans[StepAlignmentDrop]
	for _, mode := range plannedModes {
		p := plans[mode]
		if p.Full != drop.Full || (p.Full && !p.Interior.End.Equal(drop.Interior.End)) {
			return fmt.Errorf("%s and drop disagree on %+v: %+v vs %+v", mode, r, p, drop)
		}
		want := drop
		if start, _ := mode.Edges(); start == EdgeTruncate {
			want = truncate
		}
		if p.Interior != want.Interior {
			return fmt.Errorf("%s interior %s, want %s", mode, p.Interior, want.Interior)
		}
	}
	if truncate.Full && truncate.Interior.Start.After(drop.Interior.Start) {
		return fmt.Errorf("truncate starts after drop: %s vs %s", truncate.Interior, drop.Interior)
	}
	return nil
}

func TestPlanRangeInvariants(t *testing.T) {
	var cases int
	eachPlanCase(func(r RequestedRange, step, phase time.Duration, now time.Time) {
		cases++
		if err := checkPlanInvariants(r, step, phase, now); err != nil {
			t.Fatalf("step %s phase %s now %s range %+v: %v", step, phase, now, r, err)
		}
	})
	if cases < 10_000 {
		t.Fatalf("only %d cases", cases)
	}
}

func FuzzPlanRange(f *testing.F) {
	f.Add(int64(1_700_000_015), int64(620), int64(60), int64(0), int64(700), uint8(0))
	f.Add(int64(-90), int64(120), int64(60), int64(15), int64(3600), uint8(3))
	f.Add(int64(1_700_000_000), int64(3600), int64(3600), int64(900), int64(1800), uint8(4))
	f.Fuzz(func(t *testing.T, start, length, stepSec, phaseSec, nowOffset int64, flags uint8) {
		const bound = int64(1) << 32
		if stepSec <= 0 || stepSec > 86400*7 || length < 0 || length > bound || start > bound ||
			start < -bound || nowOffset < -bound || nowOffset > bound {
			return
		}
		step := time.Duration(stepSec) * time.Second
		r := RequestedRange{
			Start: time.Unix(start, 0), End: time.Unix(start+length, 0),
			StartExclusive: flags&1 != 0, EndInclusive: flags&2 != 0,
		}
		now := r.Start.Add(time.Duration(nowOffset) * time.Second)
		if flags&4 != 0 {
			if now.Before(r.Start) {
				return
			}
			r.End, r.OpenEnded, r.EndInclusive = now, true, false
		}
		phase := time.Duration(phaseSec%stepSec) * time.Second
		if err := checkPlanInvariants(r, step, phase, now); err != nil {
			t.Fatalf("%+v step %s phase %s now %s: %v", r, step, phase, now, err)
		}
	})
}

func TestPlanRangeTruncateParity(t *testing.T) {
	// truncate floors both half-open bounds, so it matches AlignExtent over the parser's inclusive
	// extent except for a partial end bucket, which AlignExtent keeps and truncate drops
	eachPlanCase(func(r RequestedRange, step, phase time.Duration, now time.Time) {
		if r.StartExclusive || r.EndInclusive || r.OpenEnded || r.End.After(now) {
			return
		}
		p := PlanRange(r, step, phase, SampleModelBucket, StepAlignmentTruncate, now)
		if !p.Full {
			return
		}
		legacy := &TimeRangeQuery{
			Step: step, Phase: phase, Extent: Extent{Start: r.Start, End: r.End.Add(-time.Nanosecond)},
		}
		legacy.alignExtent(now)
		want := legacy.Extent
		if !OnGrid(r.End, step, phase) {
			want.End = want.End.Add(-step)
		}
		if p.Interior != want {
			t.Fatalf("%+v step %s phase %s: planner %s, aligned %s", r, step, phase, p.Interior, want)
		}

		// windows labeled by their stop time match AlignExtent over Flux's shifted extent exactly
		stop := PlanRange(r, step, phase, SampleModelBucketStop, StepAlignmentTruncate, now)
		flux := &TimeRangeQuery{Step: step, Phase: phase, Extent: Extent{Start: r.Start.Add(step), End: r.End}}
		flux.alignExtent(now)
		if stop.Interior != flux.Extent {
			t.Fatalf("%+v step %s phase %s: stop labels %s, aligned %s", r, step, phase, stop.Interior, flux.Extent)
		}
	})
}

func TestPlanRangeDoesNotAllocate(t *testing.T) {
	r := RequestedRange{Start: time.Unix(1_700_000_015, 0), End: time.Unix(1_700_000_635, 0)}
	now := time.Unix(1_700_001_000, 0)
	trq := &TimeRangeQuery{
		Step: time.Minute, SampleModel: SampleModelBucket, Requested: r,
		StepAlignment: StepAlignmentPartial,
	}
	if n := testing.AllocsPerRun(100, func() {
		_ = PlanRange(r, time.Minute, 0, SampleModelBucketStop, StepAlignmentPartial, now)
		trq.PlanEdges(now)
	}); n != 0 {
		t.Errorf("planning allocated %.0f times", n)
	}
}

func BenchmarkPlanRange(b *testing.B) {
	r := RequestedRange{Start: time.Unix(1_700_000_015, 0), End: time.Unix(1_700_000_635, 0)}
	now := time.Unix(1_700_001_000, 0)
	for b.Loop() {
		_ = PlanRange(r, time.Minute, 0, SampleModelBucket, StepAlignmentPartial, now)
	}
}
