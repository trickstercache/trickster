package index

import (
	"bytes"
	"testing"
)

func TestObjectRoundTrip(t *testing.T) {
	v := Object{
		Key:   "test-key",
		Size:  1024,
		Value: []byte("cached-data"),
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 Object
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Key != "test-key" {
		t.Fatal("Key mismatch")
	}
	if v2.Size != 1024 {
		t.Fatal("Size mismatch")
	}
	if !bytes.Equal(v2.Value, []byte("cached-data")) {
		t.Fatal("Value mismatch")
	}
}
