package server

import (
	"os"
	"path/filepath"
)

// diskUsage 返回所在磁盘的总容量与可用空间。
func diskUsage(dir string) (total, free int64, err error) {
	return platformDiskUsage(dir)
}

// nearestExisting 从 dir 向上找到第一个存在的目录（新建的数据目录可能还没有子目录）。
func nearestExisting(dir string) string {
	for {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir || parent == "" {
			return "."
		}
		dir = parent
	}
}
