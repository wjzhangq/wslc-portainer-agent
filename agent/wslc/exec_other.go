//go:build !windows

package wslc

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
