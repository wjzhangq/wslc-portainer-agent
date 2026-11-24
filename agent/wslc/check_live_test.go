//go:build windows

package wslc

import (
	"context"
	"testing"
)

func TestPreflightLive(t *testing.T) {
	if _, err := FindBinary(); err != nil {
		t.Skip(err)
	}
	info, err := Preflight(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.WSLVersion == "" || info.KernelVersion == "" || info.ManagerVersion == "" || info.SettingsFile == "" {
		t.Fatalf("%#v", info)
	}
	t.Logf("wslc %s manager %s kernel %s", info.WSLVersion, info.ManagerVersion, info.KernelVersion)
}
