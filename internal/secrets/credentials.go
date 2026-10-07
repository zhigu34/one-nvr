package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/zhigu34/one-nvr/internal/id"
)

var ErrCredentialUnavailable = errors.New("credential_unavailable")

type EncryptedCredential struct {
	KeyID             string
	Nonce, Ciphertext []byte
}

func (EncryptedCredential) String() string { return "<encrypted credential redacted>" }

func (s State) credentialCipher() (cipher.AEAD, string, error) {
	master, err := hex.DecodeString(s.MasterKey)
	if err != nil || len(master) != 32 {
		return nil, "", ErrCredentialUnavailable
	}
	derive := func(label string) []byte {
		m := hmac.New(sha256.New, master)
		m.Write([]byte(label))
		return m.Sum(nil)
	}
	key := derive("one-nvr/source-credentials/key/v1")
	block, err := aes.NewCipher(key)
	clear(key)
	clear(master)
	if err != nil {
		return nil, "", ErrCredentialUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", ErrCredentialUnavailable
	}
	// The identity is derived independently; never a bare hash of a password.
	master, err = hex.DecodeString(s.MasterKey)
	if err != nil {
		return nil, "", ErrCredentialUnavailable
	}
	identity := hmac.New(sha256.New, master)
	identity.Write([]byte("one-nvr/source-credentials/id/v1"))
	clear(master)
	return aead, hex.EncodeToString(identity.Sum(nil)), nil
}
func (s State) credentialAAD(siteID, channelID, revisionID id.ID, field string) ([]byte, error) {
	if siteID != s.SiteID || (field != "username" && field != "password" && field != "import_draft") {
		return nil, ErrCredentialUnavailable
	}
	for _, value := range []id.ID{siteID, channelID, revisionID} {
		if _, err := id.Parse(string(value)); err != nil {
			return nil, ErrCredentialUnavailable
		}
	}
	return json.Marshal([]string{"one-nvr/source-credential/v1", string(siteID), string(channelID), string(revisionID), field})
}
func (s State) SealCredential(siteID, channelID, revisionID id.ID, field string, value []byte) (EncryptedCredential, error) {
	var out EncryptedCredential
	aad, err := s.credentialAAD(siteID, channelID, revisionID, field)
	if err != nil {
		return out, err
	}
	aead, keyID, err := s.credentialCipher()
	if err != nil {
		return out, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return out, ErrCredentialUnavailable
	}
	return EncryptedCredential{KeyID: keyID, Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, value, aad)}, nil
}
func (s State) OpenCredential(siteID, channelID, revisionID id.ID, field string, value EncryptedCredential) ([]byte, error) {
	aad, err := s.credentialAAD(siteID, channelID, revisionID, field)
	if err != nil {
		return nil, err
	}
	aead, keyID, err := s.credentialCipher()
	if err != nil {
		return nil, err
	}
	if !hmac.Equal([]byte(keyID), []byte(value.KeyID)) || len(value.Nonce) != aead.NonceSize() || len(value.Ciphertext) < aead.Overhead() {
		return nil, ErrCredentialUnavailable
	}
	plain, err := aead.Open(nil, value.Nonce, value.Ciphertext, aad)
	if err != nil {
		return nil, ErrCredentialUnavailable
	}
	return plain, nil
}
