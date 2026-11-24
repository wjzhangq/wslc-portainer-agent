//go:build !windows

package wslc

func MemTotal() uint64 { return 0 }
