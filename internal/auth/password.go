package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"golang.org/x/crypto/argon2"
	"strings"
	"unicode/utf8"
)

type PasswordHasher struct{ slots chan struct{} }

func NewPasswordHasher(concurrency int) *PasswordHasher {
	if concurrency < 1 || concurrency > 4 {
		concurrency = 2
	}
	return &PasswordHasher{slots: make(chan struct{}, concurrency)}
}
func ValidatePassword(p string) error {
	if utf8.RuneCountInString(p) < 12 || len(p) > 128 || strings.ContainsAny(p, "\x00\r\n") {
		return fmt.Errorf("password must be 12 characters minimum and 128 bytes maximum")
	}
	return nil
}
func (h *PasswordHasher) derive(ctx context.Context, p string, salt []byte) ([]byte, error) {
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return argon2.IDKey([]byte(p), salt, 3, 64*1024, 1, 32), nil
}
func (h *PasswordHasher) Hash(ctx context.Context, p string) (string, error) {
	if err := ValidatePassword(p); err != nil {
		return "", err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := h.derive(ctx, p, salt)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return "$argon2id$v=19$m=65536,t=3,p=1$" + enc.EncodeToString(salt) + "$" + enc.EncodeToString(key), nil
}
func (h *PasswordHasher) Verify(ctx context.Context, encoded, p string) bool {
	if len(p) > 128 {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=65536,t=3,p=1" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) != 32 {
		return false
	}
	got, err := h.derive(ctx, p, salt)
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}
