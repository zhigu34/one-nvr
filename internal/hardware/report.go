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
