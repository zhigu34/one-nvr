// Package id supplies UUID identifiers without business data in their names.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

type ID string

func New() (ID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return ID(s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]), nil
}
func Parse(s string) (ID, error) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return "", fmt.Errorf("invalid UUID")
	}
	if _, err := hex.DecodeString(strings.ReplaceAll(s, "-", "")); err != nil {
		return "", fmt.Errorf("invalid UUID")
	}
	return ID(strings.ToLower(s)), nil
}
