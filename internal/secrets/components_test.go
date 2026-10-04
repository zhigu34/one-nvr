package secrets

import "testing"

func TestPostgresCredentialPersistentAndSeparated(t *testing.T) {
	s, e := Init(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.ComponentCredential("postgres")
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.ComponentCredential("postgres")
	z, _ := s.ComponentCredential("zlm")
	if e != nil || a != b || a == z || len(a) != 64 {
		t.Fatal("unstable database credential")
	}
}
