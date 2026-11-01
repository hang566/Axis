package server

import (
	"path/filepath"

	"github.com/local/stt-sub/data"
	"github.com/local/stt-sub/media"
)

// newMediaFor 在设置变更后重建媒体执行器（ffmpeg 路径可能变了）。
// projectDir 是内容目录，中间文件放在其 temp/ 下。
func newMediaFor(ffmpegPath, projectDir string) *media.Media {
	m := media.New(ffmpegPath)
	m.SetTempDir(filepath.Join(projectDir, data.TempDir))
	return m
}
