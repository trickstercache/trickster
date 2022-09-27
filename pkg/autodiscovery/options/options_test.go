package options

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/autodiscovery/templates"
)

func TestOptions(t *testing.T) {
	// New; new options object should have empty Queries/Backends
	o1 := New()
	if i := len(o1.Backends); i != 0 {
		t.Errorf("New options should have Backends len 0, has len %d", i)
	}
	if i := len(o1.Queries); i != 0 {
		t.Errorf("New options should have Queries len 0, has len %d", i)
	}

	// Create a test backend and clone the options object; test equality
	o1.Backends["test_backend"] = &templates.Options{
		UseBackend: "test",
		Override:   make(templates.OverrideMap),
	}
	o2 := o1.Clone()

	if b1, b2 := o1.Backends["test_backend"].UseBackend, o2.Backends["test_backend"].UseBackend; b1 != b2 {
		t.Errorf("Options should have equivalent backends[test_backend].UseBackend; got %s and %s", b1, b2)
	}

	// Replacing value in o2 shouldn't change o1
	o2.Backends["test_backend"].UseBackend = "test2"

	if b1 := o1.Backends["test_backend"].UseBackend; b1 == "test2" {
		t.Errorf("Changing an options clone shouldn't change the original")
	}
}
