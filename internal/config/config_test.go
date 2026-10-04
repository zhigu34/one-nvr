package config

import (
	"os"
	"strings"
	"testing"
)

func TestParseDoesNotExecuteEnv(t *testing.T) {
	marker := t.TempDir() + "/executed"
	input := "ONE_NVR_PUBLIC_URL='http://192.168.1.10:8080'\nONE_NVR_DATABASE_URL=\"postgres://u:p#word@postgres/nvr\" # comment\nONE_NVR_DATA_DIR='$(touch " + marker + ")`touch " + marker + "`'\n"
	c, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if c.DatabaseURL != "postgres://u:p#word@postgres/nvr" {
		t.Fatal("quoted # lost")
	}
	if !strings.Contains(c.DataDir, "$(touch") || !strings.Contains(c.DataDir, "`touch") {
		t.Fatal("expression expanded")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("env executed")
	}
	if c.FrigateEnabled || c.OpenListEnabled {
		t.Fatal("optional modules enabled by default")
	}
	for _, s := range []string{"ONE_NVR_FRIGATE_ENABLE=true", "ONE_NVR_OPENLIST_ENABLE=1", "ONE_NVR_RTC_PORT=bad", "ONE_NVR_DATA_DIR='unfinished", "ONE_NVR_PUBLIC_URL=x\nONE_NVR_PUBLIC_URL=y"} {
		if _, err := Parse(strings.NewReader(s)); err == nil {
			t.Errorf("accepted invalid env %q", s)
		}
	}
}

func TestValidateSelectedEntryAndRTC(t *testing.T) {
	cases := []struct {
		name, input string
		ok          bool
		media       string
	}{
		{"https8443", "ONE_NVR_PUBLIC_URL=https://192.168.33.200:8443\nONE_NVR_HTTPS_PORT=8443", true, "192.168.33.200"},
		{"httpWithoutCertificate", "ONE_NVR_PUBLIC_URL=http://192.168.1.10:8080", true, "192.168.1.10"},
		{"wrongActivePort", "ONE_NVR_PUBLIC_URL=https://192.168.1.10:8443", false, ""},
		{"rtcCollision", "ONE_NVR_PUBLIC_URL=http://192.168.1.10:8080\nONE_NVR_RTC_PORT=8080", false, ""},
		{"domainNeedsMediaIP", "ONE_NVR_PUBLIC_URL=https://nvr.example.com", false, ""},
		{"domainWithOverride", "ONE_NVR_PUBLIC_URL=https://nvr.example.com\nONE_NVR_MEDIA_HOST=192.168.1.10", true, "192.168.1.10"},
		{"credentialsInURL", "ONE_NVR_PUBLIC_URL=https://user:pass@192.168.1.10", false, ""},
		{"pathInURL", "ONE_NVR_PUBLIC_URL=https://192.168.1.10/admin", false, ""},
		{"invalidPort", "ONE_NVR_PUBLIC_URL=http://192.168.1.10:8080\nONE_NVR_RTC_PORT=65536", false, ""},
		{"hardwareProfile", "ONE_NVR_PUBLIC_URL=http://192.168.1.10:8080\nONE_NVR_HARDWARE_PROFILE=guess", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Parse(strings.NewReader(tc.input))
			if err != nil {
				t.Fatal(err)
			}
			err = c.Validate()
			if (err == nil) != tc.ok {
				t.Fatalf("Validate=%v want valid=%v", err, tc.ok)
			}
			if tc.ok && c.MediaHost != tc.media {
				t.Fatalf("media=%q", c.MediaHost)
			}
		})
	}
}
