package site

import (
	_ "embed"
	"strings"
	"time"
)

// zones is the IANA name inventory from the pinned Go 1.27.1 zoneinfo archive.
// Runtime offsets/rules use the embedded time/tzdata database.
//
//go:embed zones.txt
var zones string

type Timezone struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	CurrentTime string `json:"current_time"`
}

func Timezones() []Timezone {
	labels := map[string]string{"Asia/Shanghai": "中国标准时间（上海）", "Asia/Tokyo": "日本标准时间（东京）", "Asia/Hong_Kong": "香港时间", "Asia/Singapore": "新加坡时间", "UTC": "协调世界时", "Europe/London": "伦敦时间", "America/New_York": "纽约时间"}
	now := time.Now()
	out := make([]Timezone, 0)
	for _, name := range strings.Fields(zones) {
		zone, err := time.LoadLocation(name)
		if err != nil {
			continue
		}
		label := labels[name]
		if label == "" {
			label = name
		}
		out = append(out, Timezone{name, label, now.In(zone).Format(time.RFC3339)})
	}
	return out
}
