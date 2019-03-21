package md5

import "testing"

func TestChecksum(t *testing.T) {

	input := "test"
	expected := "098f6bcd4621d373cade4e832627b4f6"
	result := Checksum(input)
	if expected != result {
		t.Errorf("unexpected checksum for '%s', wanted %s got %s", input, expected, result)
	}

	input = ""
	expected = "d41d8cd98f00b204e9800998ecf8427e"
	result = Checksum(input)
	if expected != result {
		t.Errorf("unexpected checksum for '%s', wanted %s got %s", input, expected, result)
	}

}
