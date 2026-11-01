// Package media 封装对外部 ffmpeg / ffprobe 的调用：探测、抽音、切片、
// 转码、截帧。主程序不做解码，内存与文件规模无关。
//
// 定位可执行文件的顺序：显式配置 → PATH → 程序目录/当前目录（含 ffmpeg*/bin），
// 因此把 ffmpeg 解压在程序旁边就能直接用，不需要配 PATH。
package media

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNoAudio 表示输入文件不含音轨。
var ErrNoAudio = errors.New("media: 文件没有音轨")

// ErrCanceled 表示调用方取消了处理。
var ErrCanceled = errors.New("media: 已取消")

// Media 是一个已定位到 ffmpeg/ffprobe 的执行器。
type Media struct {
	mu       sync.Mutex
	ffmpeg   string
	ffprobe  string
	tempDir  string
	readyErr error
}

// New 创建执行器；配置留空时自动探测。
func New(configured string) *Media {
	m := &Media{}
	m.resolve(configured)
	return m
}

// SetTempDir 设置临时文件目录（空则用系统临时目录）。
func (m *Media) SetTempDir(dir string) {
	m.mu.Lock()
	m.tempDir = dir
	m.mu.Unlock()
}

// FFmpeg 返回 ffmpeg 路径（可能为空，调用前应 Check）。
func (m *Media) FFmpeg() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ffmpeg
}

// FFprobe 返回 ffprobe 路径（可能为空）。
func (m *Media) FFprobe() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ffprobe
}

// resolve 依次尝试：显式配置、PATH、程序/工作目录附近。
func (m *Media) resolve(configured string) {
	dirs := candidateDirs()

	find := func(name string) (string, string) {
		exe := name
		if runtime.GOOS == "windows" {
			exe += ".exe"
		}
		if p := strings.TrimSpace(configured); p != "" {
			if st, err := os.Stat(p); err == nil {
				if st.IsDir() {
					cand := filepath.Join(p, exe)
					if fileExists(cand) {
						return cand, filepath.Join(p, "ffprobe"+suffixExe())
					}
				} else if strings.Contains(strings.ToLower(filepath.Base(p)), name) {
					if name == "ffmpeg" {
						return p, filepath.Join(filepath.Dir(p), "ffprobe"+suffixExe())
					}
					return filepath.Join(filepath.Dir(p), "ffmpeg"+suffixExe()), p
				}
			}
		}
		if p, err := exec.LookPath(name); err == nil {
			abs, _ := filepath.Abs(p)
			if abs == "" {
				abs = p
			}
			other := "ffprobe"
			if name == "ffprobe" {
				other = "ffmpeg"
			}
			return abs, filepath.Join(filepath.Dir(abs), other+suffixExe())
		}
		for _, dir := range dirs {
			for _, cand := range []string{
				filepath.Join(dir, exe),
				filepath.Join(dir, "bin", exe),
			} {
				if fileExists(cand) {
					return cand, filepath.Join(filepath.Dir(cand), "ffprobe"+suffixExe())
				}
			}
			matches, _ := filepath.Glob(filepath.Join(dir, "ffmpeg*", "bin", exe))
			for _, cand := range matches {
				if fileExists(cand) {
					return cand, filepath.Join(filepath.Dir(cand), "ffprobe"+suffixExe())
				}
			}
		}
		return "", ""
	}

	ffmpeg, ffprobe := find("ffmpeg")
	// 显式配置指向 ffprobe 时修正。
	if strings.Contains(strings.ToLower(filepath.Base(configured)), "ffprobe") {
		ffprobe = configured
		ffmpeg, _ = find("ffmpeg")
	}
	if ffmpeg != "" && !fileExists(ffprobe) {
		ffprobe = ""
	}
	m.mu.Lock()
	m.ffmpeg, m.ffprobe = ffmpeg, ffprobe
	m.mu.Unlock()
}

func suffixExe() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func candidateDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	dirs = append(dirs, filepath.Dir(filepath.Dir(mediaSourceDir())))
	return dirs
}

// mediaSourceDir 返回源码目录（开发运行时有助于找到项目内的 ffmpeg）。
func mediaSourceDir() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// Check 检查 ffmpeg 是否可用；不可用时返回可读的安装提示。
func (m *Media) Check() error {
	m.mu.Lock()
	err := m.readyErr
	ff := m.ffmpeg
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if ff == "" {
		msg := "未找到 ffmpeg：请把它放到程序目录下的 ffmpeg\\bin\\ 里，或在设置中填写 ffmpeg 的完整路径"
		m.mu.Lock()
		m.readyErr = errors.New(msg)
		m.mu.Unlock()
		return errors.New(msg)
	}
	return nil
}

// Version 返回 ffmpeg 版本行与路径，供「环境自检」展示。
func (m *Media) Version() (string, string) {
	if m.Check() != nil {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, m.FFmpeg(), "-version").Output()
	line := strings.SplitN(string(out), "\n", 2)[0]
	return strings.TrimSpace(line), m.FFmpeg()
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// ---- 进程执行 ----

// progressParser 解析 ffmpeg -progress pipe:1 的输出。
type progressParser struct {
	total   time.Duration
	last    time.Time
	mu      sync.Mutex
	handler func(fraction float64, position time.Duration, speed string)
}

func (p *progressParser) feed(r io.Reader) {
	if p == nil {
		return
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 32*1024), 256*1024)
	var pos time.Duration
	var speed string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "out_time_us="):
			if us, err := strconv.ParseInt(strings.TrimPrefix(line, "out_time_us="), 10, 64); err == nil && us > 0 {
				pos = time.Duration(us) * time.Microsecond
			}
		case strings.HasPrefix(line, "speed="):
			speed = strings.TrimPrefix(line, "speed=")
			p.mu.Lock()
			total := p.total
			p.mu.Unlock()
			if p.handler != nil && total > 0 {
				frac := float64(pos) / float64(total)
				if frac > 1 {
					frac = 1
				}
				// 节流到 150ms，避免刷爆 SSE。
				if time.Since(p.last) > 150*time.Millisecond || frac >= 1 {
					p.last = time.Now()
					p.handler(frac, pos, speed)
				}
			}
		case strings.HasPrefix(line, "progress=") && line != "progress=continue":
			if p.handler != nil {
				p.handler(1, pos, speed)
			}
		}
	}
}

// run 执行 ffmpeg；progress 非空时启用 -progress pipe:1 并回调节奏进度。
func (m *Media) run(ctx context.Context, total time.Duration, onProgress func(frac float64, pos time.Duration, speed string), args []string) error {
	if err := m.Check(); err != nil {
		return err
	}
	if onProgress != nil {
		args = append([]string{"-progress", "pipe:1", "-nostats"}, args...)
	}
	cmd := exec.CommandContext(ctx, m.FFmpeg(), args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	var stdout io.ReadCloser
	if onProgress != nil {
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("media: 创建进度管道失败：%w", err)
		}
		stdout = pipe
	}
	if err := cmd.Start(); err != nil {
		return wrapFFmpegError(err, stderr.String(), ctx)
	}
	if stdout != nil {
		parser := &progressParser{total: total, handler: onProgress}
		parser.feed(stdout)
	}
	err := cmd.Wait()
	if cerr := ctx.Err(); cerr != nil {
		return ErrCanceled
	}
	return wrapFFmpegError(err, stderr.String(), ctx)
}

func wrapFFmpegError(err error, stderr string, ctx context.Context) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ErrCanceled
	}
	msg := strings.TrimSpace(stderr)
	// 只保留最后几行，ffmpeg 的报错关键信息通常在末尾。
	if lines := strings.Split(msg, "\n"); len(lines) > 8 {
		msg = strings.Join(lines[len(lines)-8:], "\n")
	}
	if msg == "" {
		return fmt.Errorf("media: ffmpeg 执行失败：%w", err)
	}
	return fmt.Errorf("media: ffmpeg 执行失败：%s", msg)
}

// ---- 音频抽取 / 切片 ----

// ExtractAudio 抽取音轨并转为 PCM WAV（默认 16kHz 单声道）。
func (m *Media) ExtractAudio(ctx context.Context, input, output string, sampleRate, channels int, onProgress func(frac float64)) error {
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	if channels <= 0 {
		channels = 1
	}
	dur, _ := m.Duration(ctx, input)
	var cb func(float64, time.Duration, string)
	if onProgress != nil {
		cb = func(f float64, _ time.Duration, _ string) { onProgress(f) }
	}
	args := []string{
		"-nostdin", "-y", "-hide_banner", "-loglevel", "error",
		"-i", input,
		"-vn",
		"-ac", strconv.Itoa(channels),
		"-ar", strconv.Itoa(sampleRate),
		"-c:a", "pcm_s16le",
		"-f", "wav",
		output,
	}
	return m.run(ctx, dur, cb, args)
}

// ExtractRange 从 WAV 中截取一段并重编码为标准 PCM WAV（用于长音频分段）。
func (m *Media) ExtractRange(ctx context.Context, input, output string, start, dur time.Duration, sampleRate int) error {
	if sampleRate <= 0 {
		sampleRate = 16000
	}
	args := []string{
		"-nostdin", "-y", "-hide_banner", "-loglevel", "error",
		"-ss", fmt.Sprintf("%.3f", start.Seconds()),
		"-t", fmt.Sprintf("%.3f", dur.Seconds()),
		"-i", input,
		"-ac", "1",
		"-ar", strconv.Itoa(sampleRate),
		"-c:a", "pcm_s16le",
		"-f", "wav",
		output,
	}
	return m.run(ctx, 0, nil, args)
}

// SplitAudio 把 WAV 按固定时长切片，返回排序后的文件列表。
func (m *Media) SplitAudio(ctx context.Context, input, pattern string, chunk time.Duration) ([]string, error) {
	if chunk <= 0 {
		return nil, errors.New("media: 分片时长必须为正数")
	}
	args := []string{
		"-nostdin", "-y", "-hide_banner", "-loglevel", "error",
		"-i", input,
		"-f", "segment",
		"-segment_time", fmt.Sprintf("%.3f", chunk.Seconds()),
		"-reset_timestamps", "1",
		"-c", "copy",
		pattern,
	}
	if err := m.run(ctx, 0, nil, args); err != nil {
		return nil, err
	}
	return listSegments(pattern), nil
}

// Thumbnail 抽取一帧作为封面（返回 JPEG 路径）。
func (m *Media) Thumbnail(ctx context.Context, input, output string, at time.Duration, width int) error {
	if width <= 0 {
		width = 640
	}
	if at < 0 {
		at = 0
	}
	args := []string{
		"-nostdin", "-y", "-hide_banner", "-loglevel", "error",
		"-ss", fmt.Sprintf("%.3f", at.Seconds()),
		"-i", input,
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:-2", width),
		"-q:v", "3",
		output,
	}
	return m.run(ctx, 0, nil, args)
}

// SceneTimes 用 ffprobe 读取关键帧时间点（用于字幕编辑器的时间轴刻度）。
func (m *Media) SceneTimes(ctx context.Context, input string, limit int) []float64 {
	if m.FFprobe() == "" {
		return nil
	}
	if limit <= 0 {
		limit = 400
	}
	args := []string{
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "packet=pts_time,flags",
		"-of", "csv=p=0",
		input,
	}
	out, err := exec.CommandContext(ctx, m.FFprobe(), args...).Output()
	if err != nil {
		return nil
	}
	var times []float64
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Split(strings.TrimSpace(line), ",")
		if len(parts) < 2 || !strings.Contains(parts[1], "K") {
			continue
		}
		if v, err := strconv.ParseFloat(parts[0], 64); err == nil && v >= 0 {
			times = append(times, v)
		}
		if len(times) >= limit {
			break
		}
	}
	return times
}

// SilenceRanges 用 ffmpeg silencedetect 找出静音区间，用于把切分点落在停顿处。
func (m *Media) SilenceRanges(ctx context.Context, audio string, noiseDB float64, minSilence time.Duration) [][2]float64 {
	if err := m.Check(); err != nil {
		return nil
	}
	if noiseDB == 0 {
		noiseDB = -35
	}
	if minSilence <= 0 {
		minSilence = 600 * time.Millisecond
	}
	cmd := exec.CommandContext(ctx, m.FFmpeg(),
		"-nostdin", "-hide_banner", "-i", audio,
		"-af", fmt.Sprintf("silencedetect=noise=%sdB:d=%.3f", strconv.FormatFloat(noiseDB, 'f', -1, 64), minSilence.Seconds()),
		"-f", "null", "-")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	_ = cmd.Run()
	return parseSilence(stderr.String())
}

func parseSilence(log string) [][2]float64 {
	var out [][2]float64
	var start float64 = -1
	for _, line := range strings.Split(log, "\n") {
		if i := strings.Index(line, "silence_start:"); i >= 0 {
			if v, err := strconv.ParseFloat(strings.Fields(line[i+len("silence_start:"):])[0], 64); err == nil {
				start = v
			}
			continue
		}
		if i := strings.Index(line, "silence_end:"); i >= 0 {
			fields := strings.Fields(line[i+len("silence_end:"):])
			if len(fields) > 0 {
				if v, err := strconv.ParseFloat(fields[0], 64); err == nil && start >= 0 {
					out = append(out, [2]float64{start, v})
					start = -1
				}
			}
		}
	}
	return out
}

func listSegments(pattern string) []string {
	dir := filepath.Dir(pattern)
	glob := regexpUnescapeSegment(filepath.Base(pattern))
	files, err := filepath.Glob(filepath.Join(dir, glob))
	if err != nil {
		return nil
	}
	sortStrings(files)
	return files
}

// regexpUnescapeSegment 把 "chunk_%05d.wav" 变成 "chunk_*.wav"。
func regexpUnescapeSegment(base string) string {
	var b strings.Builder
	for i := 0; i < len(base); i++ {
		if base[i] == '%' {
			j := i + 1
			for j < len(base) && base[j] >= '0' && base[j] <= '9' {
				j++
			}
			if j < len(base) && (base[j] == 'd' || base[j] == 'i') {
				b.WriteByte('*')
				i = j
				continue
			}
		}
		b.WriteByte(base[i])
	}
	return b.String()
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
