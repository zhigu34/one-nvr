package channel

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"github.com/zhigu34/one-nvr/internal/id"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxImportBytes = 1 << 20
const MaxImportRows = 32

// Private normalized input. Credentials never enter public preview/job payloads.
type ParsedItem struct {
	Row            int          `json:"row"`
	SiteID         id.ID        `json:"site_id,omitempty"`
	ChannelID      *id.ID       `json:"channel_id,omitempty"`
	ChannelNo      int          `json:"channel_no"`
	ChannelName    string       `json:"channel_name"`
	SourceState    string       `json:"source_state"`
	Config         SourceConfig `json:"config"`
	Username       *string      `json:"username,omitempty"`
	Password       *string      `json:"password,omitempty"`
	PasswordAction string       `json:"password_action"`
	Warnings       []string     `json:"warnings"`
}

func (ParsedItem) String() string { return "<private import draft redacted>" }
func invalidImport() error {
	return invalidSource("import_invalid", "导入文件格式、字段或限制不符合要求")
}
func ParseImport(r io.Reader, format string) ([]ParsedItem, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxImportBytes+1))
	if err != nil || len(raw) > MaxImportBytes || !utf8.Valid(raw) {
		return nil, invalidImport()
	}
	defer clear(raw)
	raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
	var out []ParsedItem
	switch format {
	case "csv":
		out, err = parseImportCSV(raw)
	case "json":
		out, err = parseImportJSON(raw)
	default:
		return nil, invalidImport()
	}
	if err != nil || len(out) < 1 || len(out) > MaxImportRows {
		return nil, invalidImport()
	}
	for i := range out {
		if out[i].ChannelNo < 1 || out[i].ChannelNo > 32 || maskedImportPassword(out[i].Password) {
			return nil, invalidImport()
		}
		out[i].Row = i + 1
		if out[i].Password == nil {
			out[i].PasswordAction = "keep"
		} else if *out[i].Password == "" {
			out[i].PasswordAction = "clear"
		} else {
			out[i].PasswordAction = "replace"
		}
		if out[i].Config.RTSPPort == 0 {
			out[i].Config.RTSPPort = 554
		}
		out[i].Config.Transport = "tcp"
		if out[i].SourceState == "" {
			out[i].SourceState = "configured"
		}
		if out[i].SourceState != "configured" && out[i].SourceState != "not_configured" {
			return nil, invalidImport()
		}
		if out[i].Warnings == nil {
			out[i].Warnings = []string{}
		}
	}
	return out, nil
}
func maskedImportPassword(p *string) bool { return p != nil && maskedPassword(*p) }
func parseImportCSV(raw []byte) ([]ParsedItem, error) {
	reader := csv.NewReader(bytes.NewReader(raw))
	header, err := reader.Read()
	if err != nil {
		return nil, invalidImport()
	}
	required := strings.Split("channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path", ",")
	indexes := map[string]int{}
	for i, name := range header {
		if _, duplicate := indexes[name]; duplicate {
			return nil, invalidImport()
		}
		allowed := name == "onvif_port"
		for _, field := range required {
			allowed = allowed || field == name
		}
		if !allowed {
			return nil, invalidImport()
		}
		indexes[name] = i
	}
	for _, name := range required {
		if _, ok := indexes[name]; !ok {
			return nil, invalidImport()
		}
	}
	var out []ParsedItem
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(out) >= MaxImportRows {
			return nil, invalidImport()
		}
		value := func(name string) string {
			i, ok := indexes[name]
			if !ok {
				return ""
			}
			return record[i]
		}
		number, err := strconv.Atoi(value("channel_no"))
		if err != nil {
			return nil, invalidImport()
		}
		port, err := importPort(value("rtsp_port"), false)
		if err != nil {
			return nil, err
		}
		user, password := value("username"), value("password")
		item := ParsedItem{ChannelNo: number, ChannelName: value("channel_name"), Username: &user, Password: &password, Config: SourceConfig{IP: value("ip"), RTSPPort: port, MainPath: value("main_path"), SubPath: value("sub_path")}}
		if onvif := value("onvif_port"); onvif != "" {
			port, err := importPort(onvif, true)
			if err != nil {
				return nil, err
			}
			item.Config.ONVIFPort = &port
		}
		if item.Config.IP == "" && item.Config.MainPath == "" && item.Config.SubPath == "" && user == "" && password == "" {
			item.SourceState = "not_configured"
		}
		out = append(out, item)
	}
	return out, nil
}
func importPort(raw string, required bool) (int, error) {
	if raw == "" && !required {
		return 554, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, invalidImport()
	}
	return port, nil
}
func strictImportJSON(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		return invalidImport()
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return invalidImport()
	}
	return nil
}
func parseImportJSON(raw []byte) ([]ParsedItem, error) {
	var file struct {
		FormatVersion int               `json:"format_version"`
		SiteID        id.ID             `json:"site_id"`
		ExportedAt    string            `json:"exported_at"`
		Channels      []json.RawMessage `json:"channels"`
	}
	if strictImportJSON(raw, &file) != nil || file.FormatVersion != 1 || len(file.Channels) < 1 || len(file.Channels) > MaxImportRows {
		return nil, invalidImport()
	}
	if file.SiteID != "" {
		if _, err := id.Parse(string(file.SiteID)); err != nil {
			return nil, invalidImport()
		}
	}
	var out []ParsedItem
	for _, raw := range file.Channels {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return nil, invalidImport()
		}
		allowed := strings.Split("channel_id,source_revision_id,source_id,source_revision_number,source_state,channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path,onvif_port", ",")
		metadata := map[string]bool{"recording_policy": true, "permissions": true, "storage_pool_id": true, "event_recording_enabled": true, "enabled": true, "group": true}
		var warnings []string
		for field := range fields {
			if metadata[field] {
				warnings = append(warnings, "metadata_ignored")
				delete(fields, field)
				continue
			}
			valid := false
			for _, key := range allowed {
				valid = valid || key == field
			}
			if !valid {
				return nil, invalidImport()
			}
		}
		clean, err := json.Marshal(fields)
		if err != nil {
			return nil, invalidImport()
		}
		var row struct {
			ChannelID            *id.ID  `json:"channel_id"`
			SourceRevisionID     *id.ID  `json:"source_revision_id"`
			SourceID             *id.ID  `json:"source_id"`
			SourceRevisionNumber *int64  `json:"source_revision_number"`
			ChannelNo            int     `json:"channel_no"`
			ChannelName          string  `json:"channel_name"`
			SourceState          string  `json:"source_state"`
			IP                   string  `json:"ip"`
			RTSPPort             int     `json:"rtsp_port"`
			MainPath             string  `json:"main_path"`
			SubPath              string  `json:"sub_path"`
			ONVIFPort            *int    `json:"onvif_port"`
			Username             *string `json:"username"`
			Password             *string `json:"password"`
		}
		if strictImportJSON(clean, &row) != nil {
			return nil, invalidImport()
		}
		for _, value := range []*id.ID{row.ChannelID, row.SourceID, row.SourceRevisionID} {
			if value != nil {
				if _, err := id.Parse(string(*value)); err != nil {
					return nil, invalidImport()
				}
			}
		}
		out = append(out, ParsedItem{SiteID: file.SiteID, ChannelID: row.ChannelID, ChannelNo: row.ChannelNo, ChannelName: row.ChannelName, SourceState: row.SourceState, Username: row.Username, Password: row.Password, Warnings: warnings, Config: SourceConfig{IP: row.IP, RTSPPort: row.RTSPPort, MainPath: row.MainPath, SubPath: row.SubPath, ONVIFPort: row.ONVIFPort}})
	}
	return out, nil
}
