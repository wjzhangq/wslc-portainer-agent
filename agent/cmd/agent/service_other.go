//go:build !windows

package main

import "context"

func runAsWindowsService(func(context.Context) error) (bool, error) {
	return false, nil
}

func agentRuntime() string { return "console" }
