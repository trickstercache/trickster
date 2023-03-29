package readers

import "testing"

func TestBadReader(t *testing.T) {
	r := &BadReader{}
	n, err := r.Read(nil)
	if n != 0 {
		t.Errorf("Expected 0 bytes read, got %d", n)
	}
	if err == nil {
		t.Errorf("Expected 'bad reader' error")
	}
}
