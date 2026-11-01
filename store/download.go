package store

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Progress 回调用于上报下载进度（done/total 为字节数；total 未知时为 0）。
type Progress func(done, total int64)

func noop(_, _ int64) {}

// Download 把指定模型下载到 modelsDir 下；已存在的条目会被跳过（幂等）。
func Download(ctx context.Context, m Model, modelsDir string, onProgress Progress) error {
	if onProgress == nil {
		onProgress = noop
	}
	target := filepath.Join(modelsDir, m.Dest)
	switch m.Source.Kind {
	case "file":
		if fileExists(target) {
			return nil
		}
		return downloadFile(ctx, m.Source.URL, target, onProgress)
	case "tar", "tarmembers":
		if isInstalled(m, modelsDir) {
			return nil
		}
		return downloadTar(ctx, m, modelsDir, onProgress)
	case "hfrepo":
		if isInstalled(m, modelsDir) {
			return nil
		}
		return downloadHFRepo(ctx, m, modelsDir, onProgress)
	default:
		return fmt.Errorf("store: 未知模型来源类型 %q", m.Source.Kind)
	}
}

// IsInstalled 判断模型是否已存在于 modelsDir 中。
func IsInstalled(m Model, modelsDir string) bool {
	return isInstalled(m, modelsDir)
}

func isInstalled(m Model, modelsDir string) bool {
	target := filepath.Join(modelsDir, m.Dest)
	switch m.Source.Kind {
	case "file":
		return fileExists(target)
	case "tar":
		return dirNonEmpty(target)
	case "tarmembers":
		for _, w := range m.Source.Wanted {
			if !fileExists(filepath.Join(target, w)) {
				return false
			}
		}
		return true
	case "hfrepo":
		return dirNonEmpty(target)
	}
	return false
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func dirNonEmpty(p string) bool {
	info, err := os.Stat(p)
	if err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(p)
	return err == nil && len(entries) > 0
}

// downloadFile 流式下载到 dest.part 后原子改名；支持断点续传式跳过已存在文件。
func downloadFile(ctx context.Context, url, dest string, onProgress Progress) error {
	if onProgress == nil {
		onProgress = noop
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if fileExists(dest) {
		return nil
	}
	tmp := dest + ".part"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "stt-sub-model-store/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("store: 下载失败 %s：%w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("store: 下载失败 %s：HTTP %d", url, resp.StatusCode)
	}
	total, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)

	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	done := int64(0)
	buf := make([]byte, 1<<16)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return werr
			}
			done += int64(n)
			onProgress(done, total)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// downloadTar 下载 tar.bz2 到临时文件并解包到 target 目录。
func downloadTar(ctx context.Context, m Model, modelsDir string, onProgress Progress) error {
	tmp, err := os.CreateTemp(modelsDir, ".store-*.tar.bz2.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.Source.URL, nil)
	if err != nil {
		tmp.Close()
		return err
	}
	req.Header.Set("User-Agent", "stt-sub-model-store/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("store: 下载失败 %s：%w", m.Source.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tmp.Close()
		return fmt.Errorf("store: 下载失败 %s：HTTP %d", m.Source.URL, resp.StatusCode)
	}
	total, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	done := int64(0)
	buf := make([]byte, 1<<16)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				tmp.Close()
				return werr
			}
			done += int64(n)
			onProgress(done, total)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			tmp.Close()
			return rerr
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return extractTarBz2(tmpName, filepath.Join(modelsDir, m.Dest), m.Source.Wanted)
}

// extractTarBz2 解包 tar.bz2：统一扁平到 target 目录（只取文件名），
// 仅当 wanted 非空时保留指定 base 文件。过程不依赖 tar 内部的顶层目录名。
func extractTarBz2(archivePath, target string, wanted []string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}

	// 按文件头魔数判断是否 bzip2 压缩（"BZh"），否则当作裸 tar 直接解包。
	_, _ = f.Seek(0, io.SeekStart)
	var r io.Reader = f
	magic := make([]byte, 3)
	if _, err := io.ReadFull(f, magic); err == nil && string(magic) == "BZh" {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		r = bzip2.NewReader(f)
	} else {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	tr := tar.NewReader(r)

	got := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("store: 读取 tar 失败：%w", err)
		}
		base := filepath.Base(hdr.Name)
		if base == "" || base == "." || base == ".." {
			continue
		}
		if len(wanted) > 0 && !contains(wanted, base) {
			continue
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		out := filepath.Join(target, base)
		if strings.HasPrefix(base, "..") {
			continue
		}
		if err := writeFileFromTar(tr, out, hdr.Size); err != nil {
			return err
		}
		got[base] = true
	}

	if len(wanted) > 0 {
		var missing []string
		for _, w := range wanted {
			if !got[w] {
				missing = append(missing, w)
			}
		}
		if len(missing) > 0 {
			_ = os.RemoveAll(target)
			return fmt.Errorf("store: 压缩包缺少必需文件 %v，已移除不完整目录", missing)
		}
	}
	return nil
}

func writeFileFromTar(r io.Reader, out string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, io.LimitReader(r, size)); err != nil {
		return err
	}
	return nil
}

// downloadHFRepo 通过 Hugging Face 的 tree API 列出仓库文件，逐一下载到 target。
func downloadHFRepo(ctx context.Context, m Model, modelsDir string, onProgress Progress) error {
	target := filepath.Join(modelsDir, m.Dest)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}

	treeURL := "https://huggingface.co/api/models/" + m.Source.Repo + "/tree/main"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, treeURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "stt-sub-model-store/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("store: 获取仓库文件列表失败 %s：%w", treeURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("store: 获取仓库文件列表失败 %s：HTTP %d", treeURL, resp.StatusCode)
	}
	var entries []struct {
		Path  string `json:"path"`
		Type  string `json:"type"`
		Size  int64  `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return fmt.Errorf("store: 解析仓库文件列表失败：%w", err)
	}

	var total int64
	for _, e := range entries {
		if e.Type == "file" {
			total += e.Size
		}
	}
	done := int64(0)
	for _, e := range entries {
		if e.Type != "file" {
			continue
		}
		dest := filepath.Join(target, e.Path)
		if fileExists(dest) {
			done += e.Size
			continue
		}
		url := "https://huggingface.co/" + m.Source.Repo + "/resolve/main/" + e.Path
		if err := downloadFile(ctx, url, dest, func(d, _ int64) {
			onProgress(done+d, total)
		}); err != nil {
			return err
		}
		done += e.Size
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
