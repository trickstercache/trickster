package level

import "testing"

func TestGetLevelID(t *testing.T) {
	id := GetLevelID("invalid")
	if id != 0 {
		t.Errorf("expected %d got %d", 0, id)
	}
	id = GetLevelID(Info)
	if id != InfoID {
		t.Errorf("expected %d got %d", InfoID, id)
	}
}
