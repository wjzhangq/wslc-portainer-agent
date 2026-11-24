package wslc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MinClientVersion is the oldest wslc.exe this agent can drive.
// 3.0.3.0 is the release the command set and JSON output were checked against.
const MinClientVersion = "3.0.3.0"

// Preflight confirms wslc is installed, new enough, and able to start a session.
// A non-nil error is the message to show the operator, including how to install
// whatever is missing. The agent must not listen when this fails.
func Preflight(ctx context.Context) (SystemText, error) {
	path, err := FindBinary()
	if err != nil {
		return SystemText{}, errors.New(binaryHint(err))
	}
	exec, err := NewExecutor()
	if err != nil {
		return SystemText{}, errors.New(binaryHint(err))
	}
	return checkRuntime(ctx, path, exec)
}

func checkRuntime(ctx context.Context, binPath string, runner Runner) (SystemText, error) {
	if err := requireSibling(binPath, "wslcsession.exe"); err != nil {
		return SystemText{}, err
	}
	ver, err := clientVersion(ctx, runner)
	if err != nil {
		return SystemText{}, err
	}
	if !versionAtLeast(ver, MinClientVersion) {
		return SystemText{}, errors.New(versionHint(ver))
	}
	stdout, stderr, code, err := runner.Run(ctx, "info")
	if err != nil {
		return SystemText{}, errors.New(infoHint(-1, stderr, err))
	}
	if code != 0 {
		return SystemText{}, errors.New(infoHint(code, stderr, nil))
	}
	info := ParseInfo(stdout)
	if info.KernelVersion == "" || info.ManagerVersion == "" {
		return SystemText{}, errors.New(engineHint(info))
	}
	if err := requireSettings(info); err != nil {
		return SystemText{}, err
	}
	if info.WSLVersion == "" {
		info.WSLVersion = ver
	}
	return info, nil
}

func clientVersion(ctx context.Context, runner Runner) (string, error) {
	stdout, stderr, code, err := runner.Run(ctx, "version", "--format", "json")
	if err != nil {
		return "", errors.New(versionReadHint(stderr, err))
	}
	if v := parseClientVersion(stdout); code == 0 && v != "" {
		return v, nil
	}
	stdout, stderr, code, err = runner.Run(ctx, "version")
	if err != nil {
		return "", errors.New(versionReadHint(stderr, err))
	}
	if v := parseClientVersion(stdout); code == 0 && v != "" {
		return v, nil
	}
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = strings.TrimSpace(stdout)
	}
	return "", errors.New(versionReadHint(detail, nil))
}

func parseClientVersion(stdout string) string {
	text := strings.TrimSpace(stdout)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "{") {
		var doc struct {
			Client struct {
				Version string `json:"Version"`
			} `json:"Client"`
		}
		if json.Unmarshal([]byte(text), &doc) == nil && doc.Client.Version != "" {
			return doc.Client.Version
		}
	}
	fields := strings.Fields(text)
	for i, field := range fields {
		if strings.EqualFold(field, "wslc") && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

func versionAtLeast(got, min string) bool {
	gp := versionParts(got)
	mp := versionParts(min)
	if len(gp) == 0 {
		return false
	}
	n := len(gp)
	if len(mp) > n {
		n = len(mp)
	}
	for i := 0; i < n; i++ {
		g, m := 0, 0
		if i < len(gp) {
			g = gp[i]
		}
		if i < len(mp) {
			m = mp[i]
		}
		if g > m {
			return true
		}
		if g < m {
			return false
		}
	}
	return true
}

func versionParts(v string) []int {
	v = strings.TrimSpace(v)
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil
	}
	raw := strings.Split(v, ".")
	parts := make([]int, 0, len(raw))
	for _, piece := range raw {
		n := 0
		digits := false
		for _, r := range piece {
			if r < '0' || r > '9' {
				break
			}
			digits = true
			n = n*10 + int(r-'0')
		}
		if !digits {
			return nil
		}
		parts = append(parts, n)
	}
	return parts
}

func requireSibling(binPath, name string) error {
	sibling := filepath.Join(filepath.Dir(binPath), name)
	st, err := os.Stat(sibling)
	if err != nil || st.IsDir() {
		return errors.New(siblingHint(binPath, name))
	}
	return nil
}

func requireSettings(info SystemText) error {
	path := strings.TrimSpace(info.SettingsFile)
	if path == "" {
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			path = filepath.Join(base, "wslc", "settings.yaml")
		}
	}
	if path == "" {
		return errors.New(settingsHint("%LOCALAPPDATA%\\wslc\\settings.yaml"))
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return errors.New(settingsHint(path))
	}
	return nil
}

func binaryHint(cause error) string {
	return fmt.Sprintf(`找不到可用的 wslc.exe（%s）。
本 Agent 需要 WSL %s 或更高。wslc 随这个版本的 WSL 一起安装，默认路径是 C:\Program Files\WSL\wslc.exe。

补充依赖，用管理员 PowerShell 执行：
  winget install --id Microsoft.WSL -e
  wsl --update

装好后新开一个终端，执行 wslc version，应显示 %s 或更高。
如果 wslc.exe 不在默认路径，把环境变量 WSLC_PATH 设成它的完整路径。`, cause, MinClientVersion, MinClientVersion)
}

func siblingHint(binPath, name string) string {
	return fmt.Sprintf(`wslc.exe 在 %s，但同目录没有 %s。会话进程缺失，说明 WSL 安装不完整。

补充依赖，用管理员 PowerShell 重新安装或更新 WSL：
  wsl --update

更新后确认 %s 存在，再启动 Agent。`, filepath.Dir(binPath), name, filepath.Join(filepath.Dir(binPath), name))
}

func versionHint(got string) string {
	shown := got
	if shown == "" {
		shown = "未知"
	}
	return fmt.Sprintf(`当前 wslc 版本是 %s，本 Agent 需要 %s 或更高。低于这个版本时，容器命令和 JSON 输出与 Agent 不兼容。

补充依赖，用管理员 PowerShell 更新 WSL：
  wsl --update

然后执行 wslc version 确认版本不低于 %s。`, shown, MinClientVersion, MinClientVersion)
}

func versionReadHint(detail string, cause error) string {
	extra := strings.TrimSpace(detail)
	if cause != nil {
		if extra != "" {
			extra += "\n"
		}
		extra += cause.Error()
	}
	if extra == "" {
		extra = "没有输出"
	}
	return fmt.Sprintf(`无法读取 wslc 版本。
%s

补充依赖，用管理员 PowerShell 更新到 WSL %s 或更高：
  wsl --update

然后执行 wslc version --format json，应得到 {"Client":{"Version":"..."}}。`, extra, MinClientVersion)
}

func infoHint(code int, stderr string, cause error) string {
	detail := strings.TrimSpace(stderr)
	if cause != nil {
		if detail != "" {
			detail += "\n"
		}
		detail += cause.Error()
	}
	if detail == "" {
		detail = "没有错误输出"
	}
	codeText := strconv.Itoa(code)
	return fmt.Sprintf(`wslc info 失败（退出码 %s），容器引擎还不能用。
%s

请用将要运行 Agent 的那个 Windows 用户执行：
  wsl --status
  wslc info
输出里应有“内核版本”和“会话管理器版本”。

如果提示缺少虚拟机平台，用管理员 PowerShell 启用后重启：
  dism.exe /online /enable-feature /featurename:VirtualMachinePlatform /all /norestart
  wsl --update`, codeText, detail)
}

func engineHint(info SystemText) string {
	kernel := info.KernelVersion
	if kernel == "" {
		kernel = "（空）"
	}
	manager := info.ManagerVersion
	if manager == "" {
		manager = "（空）"
	}
	return fmt.Sprintf(`wslc info 没有同时给出内核版本和会话管理器版本，容器引擎不完整。
当前：内核版本=%s，会话管理器版本=%s

补充依赖，用管理员 PowerShell 更新 WSL 内核和组件：
  wsl --update
然后用运行 Agent 的用户执行 wslc info，确认这两行都有值。`, kernel, manager)
}

func settingsHint(path string) string {
	return fmt.Sprintf(`找不到 wslc 设置文件 %s。
Agent 必须用拥有容器数据的那个 Windows 用户运行，不能用 LocalSystem。

补充依赖：用该用户登录后执行一次
  wslc info
这会在 %%LOCALAPPDATA%%\wslc\settings.yaml 写下设置。
安装服务时 -User 填这个用户。`, path)
}
