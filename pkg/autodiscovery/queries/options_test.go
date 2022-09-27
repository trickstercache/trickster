package queries

import (
	"testing"
)

func TestOptions(t *testing.T) {
	// New; new options object should have empty Queries/Backends
	o1 := New()
	if i := len(o1.Parameters); i != 0 {
		t.Errorf("New options should have Parameters len 0, has len %d", i)
	}
	if i := len(o1.Results); i != 0 {
		t.Errorf("New options should have Results len 0, has len %d", i)
	}

	// Create a test backend and clone the options object; test equality
	o1.Parameters["test_parameter"] = "test_value"
	o2 := o1.Clone()

	if b1, b2 := o1.Parameters["test_parameter"], o2.Parameters["test_parameter"]; b1 != b2 {
		t.Errorf("Options should have equivalent parameters[test_parameter]; got %s and %s", b1, b2)
	}

	// Replacing value in o2 shouldn't change o1
	o2.Parameters["test_parameter"] = "test_value_2"

	if b1 := o1.Parameters["test_parameter"]; b1 == "test_value_2" {
		t.Errorf("Changing an options clone shouldn't change the original")
	}
}
