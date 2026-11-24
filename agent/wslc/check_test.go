package wslc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		got, min string
		ok       bool
	}{
		{"3.0.3.0", MinClientVersion, true},
		{"3.0.3", MinClientVersion, true},
		{"3.0.4.0", MinClientVersion, true},
		{"3.1", MinClientVersion, true},
		{"10.0.0.0", MinClientVersion, true},
		{"3.0.2.9", MinClientVersion, false},
		{"2.6.1.0", MinClientVersion, false},
		{"", MinClientVersion, false},
		{"3.0.3.0-rc1", MinClientVersion, true},
	}
	for _, tc := range cases {
		if got := versionAtLeast(tc.got, tc.min); got != tc.ok {
			t.Errorf("versionAtLeast(%q, %q) = %v, want %v", tc.got, tc.min, got, tc.ok)
		}
	}
}

func TestParseClientVersion(t *testing.T) {
	if got := parseClientVersion(`{"Client":{"Version":"3.0.3.0"}}`); got != "3.0.3.0" {
		t.Fatalf("json %q", got)
	}
	if got := parseClientVersion("wslc 3.0.3.0\n"); got != "3.0.3.0" {
		t.Fatalf("table %q", got)
	}
	if got := parseClientVersion("not a version"); got != "" {
		t.Fatalf("garbage %q", got)
	}
}

func TestCheckRuntime(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "wslc.exe")
	if err := os.WriteFile(bin, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wslcsession.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(settings, []byte("storagePath: default\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info := strings.ReplaceAll(readFixture(t, "testdata/info.txt"), `C:\Users\zhangwj50\AppData\Local\wslc\settings.yaml`, settings)

	ok := &scripted{run: func(args []string) (string, string, int, error) {
		switch args[0] {
		case "version":
			return `{"Client":{"Version":"3.0.3.0"}}`, "", 0, nil
		case "info":
			return info, "", 0, nil
		default:
			t.Errorf("unexpected %v", args)
			return "", "", 1, nil
		}
	}}
	got, err := checkRuntime(context.Background(), bin, ok)
	if err != nil {
		t.Fatal(err)
	}
	if got.ManagerVersion != "3.0.3" || got.KernelVersion != "6.18.54.1-1" || got.SettingsFile != settings {
		t.Fatalf("%#v", got)
	}

	old := &scripted{run: func(args []string) (string, string, int, error) {
		return "wslc 2.4.11.0", "", 0, nil
	}}
	_, err = checkRuntime(context.Background(), bin, old)
	if err == nil || !strings.Contains(err.Error(), "2.4.11.0") || !strings.Contains(err.Error(), "wsl --update") {
		t.Fatalf("old version: %v", err)
	}

	broken := &scripted{run: func(args []string) (string, string, int, error) {
		if args[0] == "version" {
			return `{"Client":{"Version":"3.0.3.0"}}`, "", 0, nil
		}
		return "", "需要启用虚拟机平台", 1, nil
	}}
	_, err = checkRuntime(context.Background(), bin, broken)
	if err == nil || !strings.Contains(err.Error(), "虚拟机平台") || !strings.Contains(err.Error(), "wslc info") {
		t.Fatalf("info failure: %v", err)
	}

	partial := &scripted{run: func(args []string) (string, string, int, error) {
		if args[0] == "version" {
			return `{"Client":{"Version":"3.0.3.0"}}`, "", 0, nil
		}
		return "客户端:\nWSL 版本: 3.0.3.0\n", "", 0, nil
	}}
	_, err = checkRuntime(context.Background(), bin, partial)
	if err == nil || !strings.Contains(err.Error(), "内核版本=（空）") || !strings.Contains(err.Error(), "wsl --update") {
		t.Fatalf("partial info: %v", err)
	}

	bare := filepath.Join(t.TempDir(), "wslc.exe")
	if err := os.WriteFile(bare, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = checkRuntime(context.Background(), bare, ok)
	if err == nil || !strings.Contains(err.Error(), "wslcsession.exe") {
		t.Fatalf("sibling: %v", err)
	}

	missingSettings := strings.ReplaceAll(info, settings, filepath.Join(dir, "missing.yaml"))
	noFile := &scripted{run: func(args []string) (string, string, int, error) {
		if args[0] == "version" {
			return `{"Client":{"Version":"3.0.3.0"}}`, "", 0, nil
		}
		return missingSettings, "", 0, nil
	}}
	_, err = checkRuntime(context.Background(), bin, noFile)
	if err == nil || !strings.Contains(err.Error(), "settings.yaml") || !strings.Contains(err.Error(), "wslc info") {
		t.Fatalf("settings: %v", err)
	}
}

func TestBinaryHintMentionsInstall(t *testing.T) {
	msg := binaryHint(os.ErrNotExist)
	for _, want := range []string{"winget install --id Microsoft.WSL", "wsl --update", "WSLC_PATH", MinClientVersion} {
		if !strings.Contains(msg, want) {
			t.Fatalf("hint missing %q\n%s", want, msg)
		}
	}
}
