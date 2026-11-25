//go:build windows

package power

import (
	"syscall"
	"unsafe"
)

type memoryStatusEx struct {
	dwLength                uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

func detectTotalRAMMB() uint64 {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GlobalMemoryStatusEx")
	var st memoryStatusEx
	st.dwLength = uint32(unsafe.Sizeof(st))
	r1, _, _ := proc.Call(uintptr(unsafe.Pointer(&st)))
	if r1 == 0 {
		return 0
	}
	return st.ullTotalPhys / (1024 * 1024)
}
