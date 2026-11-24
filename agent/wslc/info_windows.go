//go:build windows

package wslc

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var globalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func MemTotal() uint64 {
	var status memoryStatusEx
	status.Length = uint32(unsafe.Sizeof(status))
	r, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if r == 0 {
		return 0
	}
	return status.TotalPhys
}
