package main

import "testing"

func TestSanitizeTime(t *testing.T) {
	fixtures := []struct {
		input  string
		output string
	}{
		{"2015-07-01T20:10:30.781Z", "2015-07-01T20:10:30.781Z"},
		{"1523077733", "1523077733"},
		{"1523077733.2", "1523077733.2"},
	}

	for _, f := range fixtures {
		if out := sanitizeTime(f.input); out != f.output {
			t.Errorf("Expected %s, got %s for input %s", f.output, out, f.input)
		}
	}
}
