//go:build !windows

package server

import "syscall"

// platformDiskUsage 在类 Unix 系统上读取文件系统统计。
func platformDiskUsage(dir string) (total, free int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(nearestExisting(dir), &st); err != nil {
		return 0, 0, err
	}
	bsize := int64(st.Bsize)
	return int64(st.Blocks) * bsize, int64(st.Bavail) * bsize, nil
}
