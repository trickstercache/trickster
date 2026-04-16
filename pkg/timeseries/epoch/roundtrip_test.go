package epoch

import (
	"testing"
)

func TestEpochsRoundTrip(t *testing.T) {
	v := Epochs{Epoch(1000000), Epoch(2000000)}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 Epochs
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(v2) != 2 {
		t.Fatal("expected 2 epochs")
	}
	if v2[0] != v[0] {
		t.Fatal("first epoch mismatch")
	}
	if v2[1] != v[1] {
		t.Fatal("second epoch mismatch")
	}
}
