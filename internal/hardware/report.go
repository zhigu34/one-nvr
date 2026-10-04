package hardware

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

type Report struct {
	Inventory    Inventory  `json:"inventory"`
	Validation   Validation `json:"validation"`
	Proposal     Proposal   `json:"proposal"`
	Validated    bool       `json:"validated"`
	ErrorCode    string     `json:"error_code,omitempty"`
	CheckedAt    time.Time  `json:"checked_at"`
	SampleSource string     `json:"sample_source"`
	Image        string     `json:"image"`
}

func ReadReport(data string) (Report, error) {
	var out Report
	root, e := os.OpenRoot(data)
	if e != nil {
		return out, e
	}
	defer root.Close()
	file, e := root.Open("hardware/latest.json")
	if e != nil {
		return out, e
	}
	defer file.Close()
	info, e := file.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() > 128*1024 {
		return out, fmt.Errorf("invalid hardware report")
	}
	dec := json.NewDecoder(io.LimitReader(file, 128*1024+1))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&out); e != nil {
		return Report{}, fmt.Errorf("invalid hardware report")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || out.CheckedAt.IsZero() || len(out.Inventory.Devices) > 32 || len(out.Validation) > 33 {
		return Report{}, fmt.Errorf("invalid hardware report")
	}
	return out, nil
}

// Persist an interrupted probe as failure, preserving the last expected profile.
// This is used if the Docker tool cannot start, before normal selection can run.
func RecordFailure(data string) error {
	report, _ := ReadReport(data)
	report.Validated = false
	report.ErrorCode = "hardware_probe_failed"
	report.CheckedAt = time.Now().UTC()
	report.Validation = Validation{}
	root, e := os.OpenRoot(data)
	if e != nil {
		return e
	}
	defer root.Close()
	dir, e := root.OpenRoot("hardware")
	if e != nil {
		return e
	}
	defer dir.Close()
	bytes, e := json.MarshalIndent(report, "", "  ")
	if e != nil {
		return e
	}
	name := fmt.Sprintf(".failure-%d", time.Now().UnixNano())
	f, e := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer dir.Remove(name)
	if _, e = f.Write(bytes); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return dir.Rename(name, "latest.json")
}
