package channel

import (
	"encoding/json"
	"strings"
	"testing"
)

const importHeader = "channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path"

func TestImportCSVLimitsAndPasswordSemantics(t *testing.T) {
	rows, err := ParseImport(strings.NewReader("\ufeff"+importHeader+",onvif_port\r\n1,\"门口,\n北侧\",192.168.33.20,554,admin,,/main,/sub,8000\r\n"), "csv")
	if err != nil || len(rows) != 1 || rows[0].ChannelName != "门口,\n北侧" || rows[0].PasswordAction != "clear" || rows[0].Config.MainPath != "/main" || rows[0].Config.ONVIFPort == nil || *rows[0].Config.ONVIFPort != 8000 {
		t.Fatal("CSV import semantics", rows, err)
	}
	good := "1,门口,192.168.33.20,554,admin,secret,/main,/sub\n"
	for _, bad := range []string{importHeader + ",unknown\n" + good, importHeader + ",ip\n" + good, strings.Replace(importHeader, "channel_no", "no", 1) + "\n" + good, importHeader + "\n" + strings.Repeat(good, 33), importHeader + "\n" + strings.Replace(good, "secret", "******", 1), strings.Repeat("a", (1<<20)+1), importHeader + "\n" + strings.Replace(good, "secret", "private-secret\"bad", 1)} {
		_, err := ParseImport(strings.NewReader(bad), "csv")
		if err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("unsafe or unbounded input admitted", err)
		}
	}
	if rows, err := ParseImport(strings.NewReader(importHeader+"\n"+strings.Repeat(good, 32)), "csv"); err != nil || len(rows) != 32 {
		t.Fatal("32 row boundary", len(rows), err)
	}
}
func TestImportJSONPasswordAndMetadata(t *testing.T) {
	source := map[string]any{"format_version": 1, "site_id": "01887644-36d3-45d6-8d8b-5a4c5b9ebfea", "channels": []any{map[string]any{"channel_no": 1, "channel_name": "门口", "ip": "192.168.33.20", "rtsp_port": 554, "main_path": "/main", "sub_path": ""}}}
	encode := func() string { raw, _ := json.Marshal(source); return string(raw) }
	rows, err := ParseImport(strings.NewReader(encode()), "json")
	if err != nil || len(rows) != 1 || rows[0].PasswordAction != "keep" {
		t.Fatal("missing JSON password must keep", rows, err)
	}
	row := source["channels"].([]any)[0].(map[string]any)
	row["password"] = ""
	rows, err = ParseImport(strings.NewReader(encode()), "json")
	if err != nil || rows[0].PasswordAction != "clear" {
		t.Fatal("empty JSON password must clear", err)
	}
	row["recording_policy"] = map[string]any{"mode": "continuous"}
	row["permissions"] = []any{"playback"}
	rows, err = ParseImport(strings.NewReader(encode()), "json")
	if err != nil || len(rows[0].Warnings) == 0 {
		t.Fatal("export metadata not explicitly ignored", err)
	}
	row["password"] = "******"
	if _, err := ParseImport(strings.NewReader(encode()), "json"); err == nil {
		t.Fatal("masked password accepted")
	}
	delete(row, "password")
	source["format_version"] = 2
	if _, err := ParseImport(strings.NewReader(encode()), "json"); err == nil {
		t.Fatal("unknown format accepted")
	}
	source["format_version"] = 1
	source["unknown"] = true
	if _, err := ParseImport(strings.NewReader(encode()), "json"); err == nil {
		t.Fatal("unknown top-level accepted")
	}
}
