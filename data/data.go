// Package data 提供工作区的持久化基建：数据目录定位、原子写、JSON 读写、
// 文件校验与各种小工具。所有「写到磁盘」的操作都经过这里，避免出现半截文件。
package data

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// 目录布局分两类：
//
// 元数据（data/，SQLite 与设置）：
//
//	data/
//	  settings.json      界面设置
//	  library.db         素材/项目/文件夹索引（SQLite，可由内容目录重建）
//	  logs/              运行日志
//
// 内容文件（默认 project/，可用 general.project_dir 改到任意位置）：
//
//	project/
//	  assets/<ab>/<hash><ext> 素材文件（内容寻址，同内容只存一份）
//	  projects/<id>/          项目目录：project.json、media/、out/、subs/
//	  jobs/<id>.json          任务记录
//	  temp/                   上传暂存与转码中间文件
//	  trash/                  删除项目的临时回收站
const (
	AssetsDir   = "assets"
	ProjectsDir = "projects"
	JobsDir     = "jobs"
	TempDir     = "temp"
	LogsDir     = "logs"
	TrashDir    = "trash"
)

// ProjectDirName 是内容目录的默认名称（与 data/ 同级）。
const ProjectDirName = "project"

// ResolveProjectDir 决定内容文件目录：显式配置 > data 上级目录下的 project
// > data 目录内的 project。默认 data/ 与 project/ 同级，便于整体迁移。
func ResolveProjectDir(dataDir, configured string) string {
	if p := strings.TrimSpace(configured); p != "" {
		return absPath(p)
	}
	clean := filepath.Clean(dataDir)
	if filepath.Base(clean) == "data" {
		return filepath.Join(filepath.Dir(clean), ProjectDirName)
	}
	return filepath.Join(clean, ProjectDirName)
}

// ResolveDir 决定数据目录位置：显式配置 > 程序所在目录（便携模式）> 当前目录。
// 只在启动时调用一次。
func ResolveDir(configured string) string {
	if p := strings.TrimSpace(configured); p != "" {
		return absPath(p)
	}
	if dir := ExeDir(); dir != "" {
		// 便携模式：程序目录可写就放在这里，数据跟着程序走。
		probe := filepath.Join(dir, ".write-probe")
		if err := os.WriteFile(probe, []byte("ok"), 0o644); err == nil {
			_ = os.Remove(probe)
			return filepath.Join(dir, "data")
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, "data")
	}
	return filepath.Join(os.TempDir(), "stt-sub-data")
}

// ExeDir 返回可执行文件所在目录（开发时 os.Executable 指向临时构建产物，
// 因此再回退到当前工作目录）。
func ExeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// EnsureLayout 创建元数据目录（data/）中需要的子目录。
func EnsureLayout(base string) error {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return fmt.Errorf("data: 创建元数据目录失败：%w", err)
	}
	if err := os.MkdirAll(filepath.Join(base, LogsDir), 0o755); err != nil {
		return fmt.Errorf("data: 创建目录 %s 失败：%w", LogsDir, err)
	}
	return nil
}

// EnsureProjectLayout 创建内容目录结构。
func EnsureProjectLayout(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("data: 创建内容目录失败：%w", err)
	}
	for _, sub := range []string{AssetsDir, ProjectsDir, JobsDir, TempDir, TrashDir} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			return fmt.Errorf("data: 创建目录 %s 失败：%w", sub, err)
		}
	}
	return nil
}

// WriteFile 原子写入：先写同目录临时文件，fsync 后改名覆盖。
// Windows 上杀毒软件偶尔会短暂占用目标文件，因此改名失败时重试几次。
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if perm != 0 {
		_ = os.Chmod(tmpName, perm)
	}
	return renameWithRetry(tmpName, path)
}

// WriteJSON 以缩进格式原子写入 JSON。
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(path, append(data, '\n'), 0o644)
}

// ReadJSON 读取 JSON；文件不存在返回 os.ErrNotExist。主文件损坏时尝试 .bak。
func ReadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	data = trimBOM(data)
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, v); err == nil {
		return nil
	} else if bak, berr := os.ReadFile(path + ".bak"); berr == nil {
		if jerr := json.Unmarshal(trimBOM(bak), v); jerr == nil {
			return nil
		}
	}
	return fmt.Errorf("data: %s 不是合法 JSON", filepath.Base(path))
}

// WriteJSONBackup 写主文件前先把旧版本保留为 .bak，便于损坏后恢复。
func WriteJSONBackup(path string, v any) error {
	if old, err := os.ReadFile(path); err == nil && len(old) > 2 {
		_ = WriteFile(path+".bak", old, 0o644)
	}
	return WriteJSON(path, v)
}

func trimBOM(b []byte) []byte {
	return []byte(strings.TrimPrefix(string(b), "\ufeff"))
}

func renameWithRetry(from, to string) error {
	var err error
	for i := 0; i < 6; i++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if runtime.GOOS != "windows" {
			break
		}
		time.Sleep(time.Duration(30*(i+1)) * time.Millisecond)
	}
	return err
}

// NewID 生成可排序的短 ID：时间前缀（base36）+ 随机后缀。
func NewID(prefix string) string {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		copy(raw[:], []byte(fmt.Sprintf("%06d", time.Now().UnixNano()%1e6)))
	}
	ts := strings.ToLower(strconvBase36(time.Now().UnixMilli()))
	return prefix + ts + hex.EncodeToString(raw[:3])
}

func strconvBase36(v int64) string {
	if v == 0 {
		return "0"
	}
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	var buf [16]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%36]
		v /= 36
	}
	return string(buf[i:])
}

// HashFile 计算文件 SHA-256（同时返回大小），用于内容寻址与去重。
func HashFile(path string) (hash string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// CopyFile 带进度回调地复制文件，用于导入素材。
func CopyFile(src, dst string, onProgress func(done, total int64)) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	var done int64
	buf := make([]byte, 512*1024)
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			if onProgress != nil {
				onProgress(done, info.Size())
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return out.Sync()
}

// HumanBytes 把字节数格式化为 "12.3 MB"。
func HumanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n)
	for _, u := range units {
		v /= 1024
		if v < 1024 {
			if v >= 100 {
				return fmt.Sprintf("%.0f %s", v, u)
			}
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f PB", v/1024)
}

// SafeJoin 把 name 限制在 base 目录内，阻止路径穿越。
func SafeJoin(base, name string) (string, error) {
	name = strings.TrimPrefix(filepath.Clean("/"+strings.ReplaceAll(name, "\\", "/")), "/")
	if name == "" || name == "." {
		return "", errors.New("data: 非法路径")
	}
	full := filepath.Join(base, name)
	rel, err := filepath.Rel(base, full)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", errors.New("data: 非法路径")
	}
	return full, nil
}

// SafeName 把任意文件名变成安全的文件系统名字（保留扩展名）。
func SafeName(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." || base == ".." {
		return "file"
	}
	repl := func(r rune) rune {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			return '_'
		}
		if r < 32 {
			return '_'
		}
		return r
	}
	base = strings.Map(repl, base)
	base = strings.TrimRight(base, ". ")
	if base == "" {
		return "file"
	}
	// Windows 保留名
	switch strings.ToUpper(strings.TrimSuffix(base, filepath.Ext(base))) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "LPT1", "LPT2":
		base = "file_" + base
	}
	if len([]rune(base)) > 120 {
		ext := filepath.Ext(base)
		r := []rune(strings.TrimSuffix(base, ext))
		base = string(r[:80]) + ext
	}
	return base
}

// DirSize 递归统计目录占用（忽略错误，尽力而为）。
func DirSize(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

// RemoveDir 删除目录（Windows 上文件被占用时重试）。
func RemoveDir(path string) error {
	var err error
	for i := 0; i < 5; i++ {
		if err = os.RemoveAll(path); err == nil {
			return nil
		}
		time.Sleep(time.Duration(50*(i+1)) * time.Millisecond)
	}
	return err
}

// MoveInto 把文件移动/复制到目标（跨盘时复制）。
func MoveInto(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := CopyFile(src, dst, nil); err != nil {
		return err
	}
	return os.Remove(src)
}

// AppendLine 追加一行到日志文件（自动加时间前缀）。
func AppendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), line)
}

// Debouncer 把高频写盘（如进度更新）合并成低频落盘。
type Debouncer struct {
	mu    sync.Mutex
	timer *time.Timer
	delay time.Duration
	fn    func()
}

// NewDebouncer 返回值；Push 触发，Stop 停掉未执行的调用。
func NewDebouncer(delay time.Duration, fn func()) *Debouncer {
	return &Debouncer{delay: delay, fn: fn}
}

// Push 在 delay 后执行 fn；窗口内的多次调用只执行最后一次。
func (d *Debouncer) Push() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(d.delay, func() {
		d.mu.Lock()
		d.timer = nil
		d.mu.Unlock()
		d.fn()
	})
}

// Stop 取消尚未执行的调用。
func (d *Debouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}
