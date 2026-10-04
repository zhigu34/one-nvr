// Package safejson bounds and rejects secret-bearing operational payloads.
package safejson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func Canonical(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	if len(raw) > 65536 {
		return nil, fmt.Errorf("operational payload exceeds limit")
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if err := check(v); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var extra any
	if d.Decode(&extra) == nil {
		return nil, fmt.Errorf("multiple JSON documents")
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid JSON")
	}
	return canonical, nil
}
func check(v any) error {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			l := strings.ToLower(k)
			for _, s := range []string{"password", "secret", "token", "credential", "private_key", "pem", "uri", "url"} {
				if strings.Contains(l, s) {
					return fmt.Errorf("sensitive field is forbidden in operational payload")
				}
			}
			if err := check(x); err != nil {
				return err
			}
		}
	case []any:
		for _, x := range v {
			if err := check(x); err != nil {
				return err
			}
		}
	case string:
		if strings.Contains(v, "://") || strings.Contains(v, "-----BEGIN ") {
			return fmt.Errorf("URI or PEM is forbidden in operational payload")
		}
	}
	return nil
}
