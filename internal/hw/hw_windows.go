//go:build windows

package hw

import (
	"syscall"
	"unsafe"
)

type memStatusEx struct {
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

func Memory() Info {
	k := syscall.NewLazyDLL("kernel32.dll")
	p := k.NewProc("GlobalMemoryStatusEx")
	var m memStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	if r, _, _ := p.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return Info{}
	}
	return Info{TotalRAM: m.TotalPhys, AvailRAM: m.AvailPhys}
}

func perfCores() int { return 0 }
