package wslc

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

type statsRow struct {
	ID       string
	Name     string
	CPUPerc  string
	MemUsage string
	NetIO    string
	BlockIO  string
	PIDs     int
}

type cpuCounters struct {
	Total  uint64
	System uint64
}

type cpuUsage struct {
	TotalUsage  uint64   `json:"total_usage"`
	PercpuUsage []uint64 `json:"percpu_usage"`
}

type cpuStats struct {
	CPUUsage       cpuUsage `json:"cpu_usage"`
	SystemCPUUsage uint64   `json:"system_cpu_usage"`
	OnlineCPUs     int      `json:"online_cpus"`
}

type memStats struct {
	Usage uint64 `json:"usage"`
	Limit uint64 `json:"limit"`
}

type netIO struct {
	RxBytes uint64 `json:"rx_bytes"`
	TxBytes uint64 `json:"tx_bytes"`
}

type blkioEntry struct {
	Major uint64 `json:"major"`
	Minor uint64 `json:"minor"`
	Op    string `json:"op"`
	Value uint64 `json:"value"`
}

type statsJSON struct {
	Read      string `json:"read"`
	Preread   string `json:"preread"`
	PidsStats struct {
		Current int `json:"current"`
	} `json:"pids_stats"`
	BlkioStats struct {
		IOServiceBytesRecursive []blkioEntry `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	NumProcs int              `json:"num_procs"`
	CPUStats cpuStats         `json:"cpu_stats"`
	PreCPU   cpuStats         `json:"precpu_stats"`
	Memory   memStats         `json:"memory_stats"`
	Networks map[string]netIO `json:"networks"`
	Name     string           `json:"name"`
	ID       string           `json:"id"`
}

func (h *Handler) containerStats(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query().Get("stream")
	stream := q == "" || truthy(q)
	w.Header().Set("Content-Type", "application/json")

	var prev *cpuCounters
	var prevAt time.Time
	enc := json.NewEncoder(w)
	for {
		body, next, at := h.oneStats(r.Context(), id, prev, prevAt)
		prev = &next
		prevAt = at
		if err := enc.Encode(body); err != nil {
			return
		}
		flush(w)
		if !stream {
			return
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (h *Handler) oneStats(ctx context.Context, id string, prev *cpuCounters, prevAt time.Time) (statsJSON, cpuCounters, time.Time) {
	now := time.Now().UTC()
	stdout, stderr, code, err := h.runner.Run(ctx, "stats", "--format", "json", "--no-trunc", id)
	if err != nil || code != 0 || strings.TrimSpace(stdout) == "" {
		if err != nil || code != 0 {
			log.Debug().Err(err).Int("code", code).Str("id", id).Str("stderr", strings.TrimSpace(stderr)).Msg("stats unavailable, returning zeros")
		}
		return zeroStats(id, "", now), cpuCounters{}, now
	}
	rows, decErr := DecodeNDJSONBytes([]byte(stdout))
	if decErr != nil || len(rows) == 0 {
		return zeroStats(id, "", now), cpuCounters{}, now
	}
	var row statsRow
	if json.Unmarshal(rows[0], &row) != nil {
		return zeroStats(id, "", now), cpuCounters{}, now
	}
	body, next := synthesize(row, runtime.NumCPU(), now, prevAt, prev)
	return body, next, now
}

// synthesize builds a Docker StatsJSON sample from one wslc stats row.
// system_cpu_usage grows by NCPU*1e9 per second and total_usage grows by
// CPUPerc/100*NCPU*1e9. online_cpus is 1 so those NCPU factors cancel and
// Portainer's (Δtotal/Δsystem)*online_cpus*100 returns CPUPerc.
func synthesize(row statsRow, ncpu int, at, prevAt time.Time, prev *cpuCounters) (statsJSON, cpuCounters) {
	if ncpu < 1 {
		ncpu = 1
	}
	perc := parsePercent(row.CPUPerc)
	sysStep := uint64(float64(ncpu) * 1e9)
	totalStep := uint64((perc / 100) * float64(ncpu) * 1e9)
	var next cpuCounters
	pre := zeroCPU()
	if prev != nil {
		next.Total = prev.Total + totalStep
		next.System = prev.System + sysStep
		pre.CPUUsage.TotalUsage = prev.Total
		pre.CPUUsage.PercpuUsage = []uint64{prev.Total}
		pre.SystemCPUUsage = prev.System
	} else {
		next.Total = totalStep
		next.System = sysStep
	}
	cur := cpuStats{
		CPUUsage: cpuUsage{
			TotalUsage:  next.Total,
			PercpuUsage: []uint64{next.Total},
		},
		SystemCPUUsage: next.System,
		OnlineCPUs:     1,
	}
	used, limit := parsePair(row.MemUsage)
	rx, tx := parsePair(row.NetIO)
	readBytes, writeBytes := parsePair(row.BlockIO)
	readAt := at.Format(time.RFC3339Nano)
	preread := at.Add(-time.Second).Format(time.RFC3339Nano)
	if !prevAt.IsZero() {
		preread = prevAt.Format(time.RFC3339Nano)
	}
	name := row.Name
	if name != "" && !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	body := statsJSON{
		Read:     readAt,
		Preread:  preread,
		NumProcs: row.PIDs,
		CPUStats: cur,
		PreCPU:   pre,
		Memory:   memStats{Usage: used, Limit: limit},
		Networks: map[string]netIO{"eth0": {RxBytes: rx, TxBytes: tx}},
		Name:     name,
		ID:       row.ID,
	}
	body.PidsStats.Current = row.PIDs
	body.BlkioStats.IOServiceBytesRecursive = []blkioEntry{
		{Op: "read", Value: readBytes},
		{Op: "write", Value: writeBytes},
	}
	return body, next
}

func zeroCPU() cpuStats {
	return cpuStats{
		OnlineCPUs: 1,
		CPUUsage:   cpuUsage{PercpuUsage: []uint64{0}},
	}
}

func zeroStats(id, name string, at time.Time) statsJSON {
	if name != "" && !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	stamp := at.Format(time.RFC3339Nano)
	body := statsJSON{
		Read:     stamp,
		Preread:  stamp,
		CPUStats: zeroCPU(),
		PreCPU:   zeroCPU(),
		Networks: map[string]netIO{"eth0": {}},
		Name:     name,
		ID:       id,
	}
	body.BlkioStats.IOServiceBytesRecursive = []blkioEntry{
		{Op: "read"},
		{Op: "write"},
	}
	return body
}

// portainerCPUPercent is the CPU formula Portainer applies to two adjacent samples.
func portainerCPUPercent(sample statsJSON) float64 {
	cpuDelta := float64(sample.CPUStats.CPUUsage.TotalUsage - sample.PreCPU.CPUUsage.TotalUsage)
	sysDelta := float64(sample.CPUStats.SystemCPUUsage - sample.PreCPU.SystemCPUUsage)
	if sysDelta == 0 {
		return 0
	}
	return cpuDelta / sysDelta * float64(sample.CPUStats.OnlineCPUs) * 100
}

func parsePercent(value string) float64 {
	value = strings.TrimSpace(strings.TrimSuffix(value, "%"))
	if value == "" {
		return 0
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return f
}

func parsePair(value string) (uint64, uint64) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return 0, 0
	}
	return parseSize(parts[0]), parseSize(parts[1])
}

func parseSize(value string) uint64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	i := 0
	for i < len(value) && (value[i] == '.' || (value[i] >= '0' && value[i] <= '9')) {
		i++
	}
	if i == 0 {
		return 0
	}
	n, err := strconv.ParseFloat(value[:i], 64)
	if err != nil {
		return 0
	}
	return uint64(n * unitMultiplier(strings.TrimSpace(value[i:])))
}

func unitMultiplier(unit string) float64 {
	switch strings.ToLower(unit) {
	case "", "b":
		return 1
	case "kb", "k":
		return 1e3
	case "mb", "m":
		return 1e6
	case "gb", "g":
		return 1e9
	case "tb", "t":
		return 1e12
	case "kib":
		return 1024
	case "mib":
		return 1024 * 1024
	case "gib":
		return 1024 * 1024 * 1024
	case "tib":
		return 1024 * 1024 * 1024 * 1024
	default:
		return 1
	}
}
