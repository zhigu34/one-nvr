package secrets

import "testing"

func TestMediaHookCredentialsSeparatedAndStable(t *testing.T) {
	s, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hook, err := s.ComponentCredential("recording-hook")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := s.ComponentCredential("media-probe")
	if err != nil {
		t.Fatal(err)
	}
	api, _ := s.ComponentCredential("zlm")
	again, _ := s.ComponentCredential("recording-hook")
	if hook != again || len(hook) != 64 || hook == probe || hook == api || probe == api {
		t.Fatal("media credentials lack domain separation")
	}
}
