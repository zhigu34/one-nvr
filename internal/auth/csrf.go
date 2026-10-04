package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

func (s *Service) PreAuthToken() (cookie, token string, err error) {
	token, err = randomToken()
	if err != nil {
		return "", "", err
	}
	unsigned := token + "." + strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10)
	return unsigned + "." + s.signPreAuth(unsigned), token, nil
}
func (s *Service) signPreAuth(value string) string {
	h := hmac.New(sha256.New, s.csrfKey)
	h.Write([]byte("preauth:" + value))
	return hex.EncodeToString(h.Sum(nil))
}
func (s *Service) VerifyPreAuth(cookie, token string) bool {
	p := strings.Split(cookie, ".")
	if len(p) != 3 || len(p[0]) != 64 || len(p[2]) != 64 {
		return false
	}
	expires, err := strconv.ParseInt(p[1], 10, 64)
	if err != nil || expires <= time.Now().Unix() {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(p[0]), []byte(token)) != 1 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(s.signPreAuth(p[0]+"."+p[1])), []byte(p[2])) == 1
}
