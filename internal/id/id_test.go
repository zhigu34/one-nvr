package id

import "testing"

func TestUUIDValidation(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	b, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("reused UUID")
	}
	if _, err := Parse(string(a)); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"", "../../etc/passwd", "11111111-1111-4111-8111-11111111111z", "11111111111141118111111111111111"} {
		if _, err := Parse(v); err == nil {
			t.Errorf("accepted %q", v)
		}
	}
}
