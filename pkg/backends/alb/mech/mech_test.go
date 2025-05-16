package mech

import "testing"

func TestGetMechanismByName(t *testing.T) {

	_, b := GetMechanismByName("test")
	if b {
		t.Error("expected false")
	}
}

func TestMechansimString(t *testing.T) {

	v, _ := GetMechanismByName("rr")
	if v.String() != "rr" {
		t.Errorf("expected %s got %s", "rr", v.String())
	}

	v = 85
	if v.String() != "85" {
		t.Errorf("expected %s got %s", "85", v.String())
	}
}
