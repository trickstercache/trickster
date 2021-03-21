package context

import (
	"context"
	"testing"
)

func TestRequestBody(t *testing.T) {
	ctx := context.Background()
	b := RequestBody(ctx)
	if b != nil {
		t.Error("mismatch", string(b))
	}
	ctx = WithRequestBody(ctx, []byte("trickster"))
	b = RequestBody(ctx)
	if string(b) != "trickster" {
		t.Error("mismatch")
	}
}
