package secrets

import (
	"bytes"
	"testing"

	"github.com/zhigu34/one-nvr/internal/id"
)

func TestCredentialAADAndMissingKey(t *testing.T) {
	s, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c, _ := id.New()
	r, _ := id.New()
	other, _ := id.New()
	value := []byte("fixture-password-marker")
	encrypted, err := s.SealCredential(s.SiteID, c, r, "password", value)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.SealCredential(s.SiteID, c, r, "password", value)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(encrypted.Nonce, second.Nonce) || bytes.Equal(encrypted.Ciphertext, second.Ciphertext) {
		t.Fatal("nonce/ciphertext reused")
	}
	plain, err := s.OpenCredential(s.SiteID, c, r, "password", encrypted)
	if err != nil || !bytes.Equal(plain, value) {
		t.Fatal("credential not recoverable", err)
	}
	for _, bad := range []struct {
		site, channel, revision id.ID
		field                   string
	}{{other, c, r, "password"}, {s.SiteID, other, r, "password"}, {s.SiteID, c, other, "password"}, {s.SiteID, c, r, "username"}} {
		if _, err := s.OpenCredential(bad.site, bad.channel, bad.revision, bad.field, encrypted); err == nil {
			t.Fatal("AAD mismatch accepted")
		}
	}
	broken := s
	broken.MasterKey = ""
	if _, err := broken.OpenCredential(s.SiteID, c, r, "password", encrypted); err == nil {
		t.Fatal("missing key accepted")
	}
	broken = s
	broken.MasterKey = second.KeyID
	if _, err := broken.OpenCredential(s.SiteID, c, r, "password", encrypted); err == nil {
		t.Fatal("wrong key accepted")
	}
	corrupt := encrypted
	corrupt.Ciphertext = bytes.Clone(encrypted.Ciphertext)
	corrupt.Ciphertext[0] ^= 1
	if _, err := s.OpenCredential(s.SiteID, c, r, "password", corrupt); err == nil {
		t.Fatal("tampered credential accepted")
	}
	empty, err := s.SealCredential(s.SiteID, c, r, "password", nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, err = s.OpenCredential(s.SiteID, c, r, "password", empty)
	if err != nil || len(plain) != 0 {
		t.Fatal("explicit empty password lost")
	}
}
