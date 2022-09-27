package templates

import "testing"

func TestOptions(t *testing.T) {
	o1 := New()

	if i := len(o1.Override); i != 0 {
		t.Errorf("New options should have len 0 for override; got %d", i)
	}

	o1.UseBackend = "test_backend"
	o2 := o1.Clone()
	if b1, b2 := o1.UseBackend, o2.UseBackend; b1 != b2 {
		t.Errorf("Cloned template should have equivalent UseBackend value; got %s and %s", b1, b2)
	}

	o2.UseBackend = "test_backend_2"

	if b1 := o1.UseBackend; b1 == "test_backend_2" {
		t.Errorf("Cloned template should not copy its changes over to the original")
	}

	o2.Override["test_override"] = "test_value"

	if original, ok := o1.Override["test_override"]; ok {
		t.Errorf("After setting override in clone, original should be nil with that key; got %s", original)
	}
}
