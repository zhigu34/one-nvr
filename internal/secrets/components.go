package secrets

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// ComponentCredential derives stable, domain-separated internal service keys.
// Values are only written to private generated component configs, never DTOs.
func (s State) ComponentCredential(name string) (string, error) {
	switch name {
	case "zlm", "mqtt", "openlist", "gateway", "postgres", "recording-hook", "media-probe":
	default:
		return "", errors.New("unknown component credential")
	}
	key, err := hex.DecodeString(s.MasterKey)
	if err != nil || len(key) != 32 {
		return "", errors.New("invalid persistent master key")
	}
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte("one-nvr/component/v1/" + name))
	return hex.EncodeToString(m.Sum(nil)), nil
}
