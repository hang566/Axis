//go:build windows

package server

import (
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpace = kernel32.NewProc("GetDiskFreeSpaceExW")
)

// platformDiskUsage 调用 Windows API 取磁盘容量。
func platformDiskUsage(dir string) (total, free int64, err error) {
	path, err := syscall.UTF16PtrFromString(nearestExisting(dir))
	if err != nil {
		return 0, 0, err
	}
	var freeBytesAvailable, totalBytes, totalFreeBytes uint64
	r, _, callErr := getDiskFreeSpace.Call(
		uintptr(unsafe.Pointer(path)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)
	if r == 0 {
		return 0, 0, callErr
	}
	return int64(totalBytes), int64(freeBytesAvailable), nil
}
