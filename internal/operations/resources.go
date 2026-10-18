package operations

import (
	"bufio"
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HostResources is what the machine is doing right now (DASH-02).
//
// The three figures come from different places and have different reliability,
// so each carries its own Known flag: PRD DASH-02 requires unknown to be shown as
// unknown, never as zero. A zero CPU reading is indistinguishable from a healthy
// idle machine, which is the one thing an operator must not be told when the
// measurement failed.
type HostResources struct {
	// CPUPercent is the busy share of all cores since the previous sample.
	// Known is false on the first call, because a single /proc/stat reading is a
	// counter with no interval to divide by.
	CPUPercent float64 `json:"cpu_percent"`
	CPUKnown   bool    `json:"cpu_known"`
	CPUCores   int     `json:"cpu_cores"`

	// MemoryPercent is used/total from /proc/meminfo, where used excludes
	// buffers and cache — MemAvailable already accounts for reclaimable pages,
	// and counting cache as "used" would report a healthy host as nearly full.
	MemoryPercent float64 `json:"memory_percent"`
	MemoryKnown   bool    `json:"memory_known"`
	MemoryTotal   uint64  `json:"memory_total_bytes"`
	MemoryUsed    uint64  `json:"memory_used_bytes"`

	// LoadAverage is the 1-minute figure. It is a different question from CPU
	// percentage — a host can sit at 100% of one core with a load of 1.0 — so it
	// travels separately rather than being folded into CPUPercent.
	LoadAverage float64 `json:"load_average"`
	LoadKnown   bool    `json:"load_known"`

	ObservedAt time.Time `json:"observed_at"`
}

// hostSampler keeps the previous CPU counters, which is the only way to turn
// /proc/stat into a percentage: the file reports totals since boot.
type hostSampler struct {
	mu      sync.Mutex
	prevID  string
	prevTot cpuTimes
	proc    string
}

type cpuTimes struct {
	idle  uint64
	total uint64
}

func newHostSampler() *hostSampler {
	return &hostSampler{proc: "/proc"}
}

// Sample reads the host figures. A read failure leaves the corresponding Known
// flag false instead of zeroing the value.
func (h *hostSampler) Sample(ctx context.Context) HostResources {
	out := HostResources{ObservedAt: time.Now().UTC()}
	out.CPUCores = runtime.NumCPU()

	h.mu.Lock()
	times, id, ok := h.readCPU()
	if ok {
		if h.prevTot.total != 0 && h.prevID == id {
			totalDelta := times.total - h.prevTot.total
			idleDelta := times.idle - h.prevTot.idle
			// Counters can wrap or reset when a container is recreated; a
			// negative delta means the comparison is meaningless.
			if totalDelta > 0 && times.idle >= h.prevTot.idle {
				out.CPUPercent = round1(float64(totalDelta-idleDelta) / float64(totalDelta) * 100)
				out.CPUKnown = true
			}
		}
		h.prevTot, h.prevID = times, id
	}
	h.mu.Unlock()

	total, available, ok := h.readMemory()
	if ok && total > 0 && available <= total {
		out.MemoryTotal = total
		out.MemoryUsed = total - available
		out.MemoryPercent = round1(float64(out.MemoryUsed) / float64(total) * 100)
		out.MemoryKnown = true
	}

	if load, ok := h.readLoad(); ok {
		out.LoadAverage = load
		out.LoadKnown = true
	}
	return out
}

func (h *hostSampler) readCPU() (cpuTimes, string, bool) {
	file, err := os.Open(h.proc + "/stat")
	if err != nil {
		return cpuTimes{}, "", false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		if len(fields) < 4 {
			return cpuTimes{}, "", false
		}
		var values []uint64
		for _, field := range fields {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return cpuTimes{}, "", false
			}
			values = append(values, value)
		}
		var total uint64
		for _, value := range values {
			total += value
		}
		// Fields 4 and 5 are idle and iowait; both are time the CPU was not
		// doing work, and counting iowait as busy would report a disk-bound host
		// as CPU-bound.
		idle := values[3]
		if len(values) > 4 {
			idle += values[4]
		}
		return cpuTimes{idle: idle, total: total}, "", true
	}
	return cpuTimes{}, "", false
}

func (h *hostSampler) readMemory() (total, available uint64, ok bool) {
	file, err := os.Open(h.proc + "/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	var foundTotal, foundAvailable bool
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			total, foundTotal = value*1024, true
		case "MemAvailable":
			available, foundAvailable = value*1024, true
		}
	}
	return total, available, foundTotal && foundAvailable
}

func (h *hostSampler) readLoad() (float64, bool) {
	raw, err := os.ReadFile(h.proc + "/loadavg")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, false
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return round1(value), true
}

func round1(value float64) float64 {
	return float64(int64(value*10+0.5)) / 10
}
