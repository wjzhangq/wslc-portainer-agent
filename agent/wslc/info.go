package wslc

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const apiVersion = "1.44"

type SystemText struct {
	WSLVersion     string
	KernelVersion  string
	WindowsVersion string
	ManagerVersion string
	Session        string
	SettingsFile   string
}

func ParseInfo(text string) SystemText {
	var info SystemText
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "WSL 版本":
			info.WSLVersion = val
		case "内核版本":
			info.KernelVersion = val
		case "Windows 版本":
			info.WindowsVersion = val
		case "会话管理器版本":
			info.ManagerVersion = val
		case "会话":
			info.Session = val
		case "设置文件":
			info.SettingsFile = val
		}
	}
	return info
}

func StoragePath() string {
	base := os.Getenv("LOCALAPPDATA")
	return parseStoragePath(readSettings(base), base)
}

func readSettings(localAppData string) string {
	if localAppData == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(localAppData, "wslc", "settings.yaml"))
	if err != nil {
		return ""
	}
	return string(b)
}

func parseStoragePath(text, fallback string) string {
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		key, val, ok := strings.Cut(trim, ":")
		if !ok || strings.TrimSpace(key) != "storagePath" {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if val == "" || val == "default" {
			return fallback
		}
		return val
	}
	return fallback
}

func countContainerStates(ndjson string) (running, paused, stopped, total int) {
	rows, err := DecodeNDJSONBytes([]byte(ndjson))
	if err != nil {
		return 0, 0, 0, 0
	}
	for _, raw := range rows {
		var row struct {
			State string
		}
		if json.Unmarshal(raw, &row) != nil {
			continue
		}
		total++
		switch strings.ToLower(row.State) {
		case "running":
			running++
		case "paused":
			paused++
		default:
			stopped++
		}
	}
	return running, paused, stopped, total
}

func countImageIDs(ndjson string) int {
	rows, err := DecodeNDJSONBytes([]byte(ndjson))
	if err != nil {
		return 0
	}
	seen := map[string]struct{}{}
	for _, raw := range rows {
		var row struct {
			ID string
		}
		if json.Unmarshal(raw, &row) != nil || row.ID == "" {
			continue
		}
		seen[row.ID] = struct{}{}
	}
	return len(seen)
}

func (h *Handler) version(w http.ResponseWriter, r *http.Request) {
	stdout, stderr, code, err := h.runner.Run(r.Context(), "info")
	if !h.success(w, stdout, stderr, code, err) {
		return
	}
	parsed := ParseInfo(stdout)
	writeJSON(w, http.StatusOK, map[string]any{
		"Platform":      map[string]any{"Name": "wslc"},
		"Version":       parsed.ManagerVersion,
		"ApiVersion":    apiVersion,
		"MinAPIVersion": apiVersion,
		"Os":            "linux",
		"Arch":          "amd64",
		"KernelVersion": parsed.KernelVersion,
		"GoVersion":     "",
		"GitCommit":     "",
		"BuildTime":     "",
		"Experimental":  false,
	})
}

func (h *Handler) dockerInfo(w http.ResponseWriter, r *http.Request) {
	infoText, stderr, code, err := h.runner.Run(r.Context(), "info")
	if !h.success(w, infoText, stderr, code, err) {
		return
	}
	psOut, stderr, code, err := h.runner.Run(r.Context(), "ps", "-a", "--format", "json", "--no-trunc")
	if !h.success(w, psOut, stderr, code, err) {
		return
	}
	imgOut, stderr, code, err := h.runner.Run(r.Context(), "images", "--format", "json", "--no-trunc")
	if !h.success(w, imgOut, stderr, code, err) {
		return
	}
	writeJSON(w, http.StatusOK, buildInfo(infoText, psOut, imgOut))
}

func buildInfo(infoText, psNDJSON, imagesNDJSON string) map[string]any {
	parsed := ParseInfo(infoText)
	running, paused, stopped, total := countContainerStates(psNDJSON)
	host, _ := os.Hostname()
	osName := strings.TrimSpace("WSL " + parsed.WSLVersion)
	return map[string]any{
		"ID":                 host,
		"Containers":         total,
		"ContainersRunning":  running,
		"ContainersPaused":   paused,
		"ContainersStopped":  stopped,
		"Images":             countImageIDs(imagesNDJSON),
		"NCPU":               runtime.NumCPU(),
		"MemTotal":           MemTotal(),
		"Name":               host,
		"ServerVersion":      parsed.ManagerVersion,
		"KernelVersion":      parsed.KernelVersion,
		"OperatingSystem":    osName,
		"OSVersion":          parsed.WindowsVersion,
		"OSType":             "linux",
		"Architecture":       "amd64",
		"Driver":             "wslc",
		"DockerRootDir":      StoragePath(),
		"SystemTime":         time.Now().UTC().Format(time.RFC3339Nano),
		"LoggingDriver":      "json-file",
		"CgroupDriver":       "cgroupfs",
		"CgroupVersion":      "2",
		"DefaultRuntime":     "runc",
		"LiveRestoreEnabled": false,
		"InitBinary":         "",
		"MemoryLimit":        true,
		"SwapLimit":          false,
		"IPv4Forwarding":     true,
		"BridgeNfIptables":   true,
		"BridgeNfIp6tables":  true,
		"CPUCfsPeriod":       true,
		"CPUCfsQuota":        true,
		"CPUShares":          true,
		"CPUSet":             true,
		"PidsLimit":          true,
		"OomKillDisable":     true,
		"ExperimentalBuild":  false,
		"NFd":                0,
		"NGoroutines":        runtime.NumGoroutine(),
		"IndexServerAddress": "https://index.docker.io/v1/",
		"Labels":             []string{},
		"SecurityOptions":    []string{},
		"Warnings":           []string{},
		"Runtimes": map[string]any{
			"runc": map[string]any{"path": "runc"},
		},
		"Plugins": map[string]any{
			"Volume":        []string{},
			"Network":       []string{},
			"Authorization": []string{},
			"Log":           []string{},
		},
		"RegistryConfig": map[string]any{
			"IndexConfigs":          map[string]any{},
			"InsecureRegistryCIDRs": []string{},
			"Mirrors":               []string{},
		},
		"Swarm": map[string]any{
			"LocalNodeState":   "inactive",
			"NodeID":           "",
			"NodeAddr":         "",
			"ControlAvailable": false,
			"Error":            "",
			"RemoteManagers":   nil,
		},
	}
}
