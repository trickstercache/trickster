package duration

import (
	"testing"
	"time"
)

type testCase struct {
	literal    string
	shouldPass bool
	expect     time.Duration
}

func testParseDuration(t *testing.T, tc testCase) {
	t.Run(tc.literal, func(t *testing.T) {
		d, err := ParseDuration(tc.literal)
		if tc.shouldPass && err != nil {
			t.Error(err)
			t.FailNow()
		}
		if !tc.shouldPass && err == nil {
			t.Errorf("test case expected to fail but does not")
			t.FailNow()
		}
		if d != tc.expect {
			t.Errorf("expected %v, got %v", tc.expect, d)
			t.FailNow()
		}
	})
}

// Test against simple cases; if we parse every available unit, is it equal to its assigned duration?
func TestSimpleParseDuration(t *testing.T) {
	var simpleCases []testCase = []testCase{}
	for unit, value := range Durations {
		simpleCases = append(simpleCases, testCase{"1" + string(unit), true, value})
	}
	for _, tc := range simpleCases {
		testParseDuration(t, tc)
	}
}

// Test against complex cases, including negatives, composite durations, and failure states
func TestComplexParseDuration(t *testing.T) {
	var complexCases []testCase = []testCase{
		{"-1w", true, -7 * 24 * time.Hour},
		{"1d1h1m1s", true, 24*time.Hour + time.Hour + time.Minute + time.Second},
		{"1dh", false, 0},
		{"1d1", false, 0},
	}
	for _, tc := range complexCases {
		testParseDuration(t, tc)
	}
}
