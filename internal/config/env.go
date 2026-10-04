// Package config reads deployment data without shell evaluation or expansion.
package config

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func ReadEnv(r io.Reader) (map[string]string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for line := 1; scanner.Scan(); line++ {
		s := strings.TrimSpace(scanner.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		key, raw, ok := strings.Cut(s, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("env line %d: expected KEY=value", line)
		}
		for i, c := range key {
			if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9') {
				return nil, fmt.Errorf("env line %d: invalid key", line)
			}
		}
		if _, ok := values[key]; ok {
			return nil, fmt.Errorf("env line %d: duplicate %s", line, key)
		}
		raw = strings.TrimSpace(raw)
		var value string
		if len(raw) > 0 && (raw[0] == '\'' || raw[0] == '"') {
			quote := raw[0]
			end := strings.IndexByte(raw[1:], quote)
			if end < 0 {
				return nil, fmt.Errorf("env line %d: unterminated quote", line)
			}
			end++
			value = raw[1:end]
			tail := strings.TrimSpace(raw[end+1:])
			if tail != "" && !strings.HasPrefix(tail, "#") {
				return nil, fmt.Errorf("env line %d: trailing characters", line)
			}
		} else {
			// A # only starts an unquoted comment after whitespace. URL fragments remain literal.
			for i, c := range raw {
				if c == '#' && (i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t') {
					raw = raw[:i]
					break
				}
			}
			value = strings.TrimSpace(raw)
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("env line %d: invalid control character", line)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read env: %w", err)
	}
	return values, nil
}

func Parse(r io.Reader) (Config, error) {
	values, err := ReadEnv(r)
	if err != nil {
		return Config{}, err
	}
	return FromValues(values)
}
func FromValues(v map[string]string) (Config, error) {
	c := Config{PublicURL: "https://127.0.0.1", HTTPPort: 8080, HTTPSPort: 443, RTCPort: 8000, DataDir: "/data", HardwareProfile: "auto", ListenAddress: ":8081"}
	for k, target := range map[string]*string{"ONE_NVR_PUBLIC_URL": &c.PublicURL, "ONE_NVR_MEDIA_HOST": &c.MediaHost, "ONE_NVR_DATA_DIR": &c.DataDir, "ONE_NVR_DATABASE_URL": &c.DatabaseURL, "ONE_NVR_TLS_DIR": &c.TLSDir, "ONE_NVR_HARDWARE_PROFILE": &c.HardwareProfile, "ONE_NVR_LISTEN_ADDRESS": &c.ListenAddress} {
		if s, ok := v[k]; ok {
			*target = s
		}
	}
	for k, target := range map[string]*int{"ONE_NVR_HTTP_PORT": &c.HTTPPort, "ONE_NVR_HTTPS_PORT": &c.HTTPSPort, "ONE_NVR_RTC_PORT": &c.RTCPort} {
		if s, ok := v[k]; ok {
			n, err := strconv.Atoi(s)
			if err != nil {
				return Config{}, fmt.Errorf("%s must be an integer", k)
			}
			*target = n
		}
	}
	for k, target := range map[string]*bool{"ONE_NVR_FRIGATE_ENABLE": &c.FrigateEnabled, "ONE_NVR_OPENLIST_ENABLE": &c.OpenListEnabled} {
		if s, ok := v[k]; ok {
			switch s {
			case "yes":
				*target = true
			case "no":
				*target = false
			default:
				return Config{}, fmt.Errorf("%s must be yes or no", k)
			}
		}
	}
	return c, nil
}
