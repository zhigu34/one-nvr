package auth

import (
	"context"
	"strings"
	"testing"
)

func TestPasswordHashIsSaltedAndRejectsUnboundedParameters(t *testing.T) {
	h := NewPasswordHasher(2)
	ctx := context.Background()
	a, err := h.Hash(ctx, "strong-test-password")
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.Hash(ctx, "strong-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("reused salt")
	}
	if !h.Verify(ctx, a, "strong-test-password") || h.Verify(ctx, a, "wrong") {
		t.Fatal("incorrect verification")
	}
	if h.Verify(ctx, strings.Replace(a, "m=65536", "m=4294967295", 1), "strong-test-password") {
		t.Fatal("accepted unbounded memory parameters")
	}
}
