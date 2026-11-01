package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// localEngine 通过常驻的 Python 子进程调用 sherpa-onnx：
// 模型只加载一次，后续每段音频只需几十毫秒，比每次拉起进程快一个数量级，
// 也让长音频的进度能实时回传。
//
// 协议：stdin/stdout 各一行 JSON。
//
//	-> {"type":"transcribe","id":"1","audio":"...","language":"zh","model":"sense-voice"}
//	<- {"type":"progress","id":"1","pct":0.42,"stage":"识别中"}
//	<- {"type":"result","id":"1","segments":[...],"language":"zh","model":"..."}
//	<- {"type":"error","id":"1","message":"..."}
type localEngine struct {
	cfg     Config
	timeout time.Duration

	mu      sync.Mutex // 串行化识别请求（一个 worker 一次只处理一个）
	process *workerProcess
	seq     int
	closed  bool
}

// workerProcess 持有 Python 子进程与读写端。
type workerProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan []byte
	stderr *ringBuffer
	done   chan struct{}
	err    error
}

func init() {
	Register("local", newLocalEngine)
	Register("local_command", newLocalEngine)
	Register("sherpa", newLocalEngine)
	Register("cli", newLocalCLI)
}

func newLocalEngine(cfg Config) (Transcriber, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Minute
	}
	e := &localEngine{cfg: cfg, timeout: cfg.Timeout}
	if err := e.ensureProcess(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *localEngine) Name() string { return "本地引擎（sherpa-onnx）" }

// Init 预加载模型，尽早暴露环境问题。
func (e *localEngine) Init() error { return e.ensureProcess() }

func (e *localEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	e.killLocked()
	return nil
}

// ---- 进程管理 ----

func (e *localEngine) ensureProcess() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return fmt.Errorf("adapters: 本地引擎已关闭")
	}
	if e.process != nil {
		select {
		case <-e.process.done:
			e.process = nil // 进程已退出，重建
		default:
			return nil
		}
	}
	p, err := e.startProcess()
	if err != nil {
		return err
	}
	e.process = p
	return nil
}

func (e *localEngine) startProcess() (*workerProcess, error) {
	script, err := e.locateScript()
	if err != nil {
		return nil, err
	}
	python, args, err := findPython()
	if err != nil {
		return nil, err
	}
	modelsDir := e.cfg.ModelsDir
	if modelsDir == "" {
		modelsDir = "models"
	}
	full := append(args, "-X", "utf8", script,
		"--models-dir", modelsDir,
		"--model", e.cfg.ModelID,
		"--threads", "2",
	)
	cmd := exec.Command(python, full...)
	cmd.Dir = e.cfg.ScriptRoot
	cmd.Env = append(os.Environ(),
		"PYTHONIOENCODING=utf-8",
		"PYTHONUTF8=1",
		"PYTHONUNBUFFERED=1",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("adapters: 启动本地语音引擎失败：%w", err)
	}
	p := &workerProcess{
		cmd:    cmd,
		stdin:  stdin,
		lines:  make(chan []byte, 32),
		stderr: newRingBuffer(40),
		done:   make(chan struct{}),
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for sc.Scan() {
			line := append([]byte(nil), sc.Bytes()...)
			select {
			case p.lines <- line:
			case <-p.done:
				return
			}
		}
		close(p.lines)
	}()
	go func() {
		sc := bufio.NewScanner(stderrPipe)
		for sc.Scan() {
			p.stderr.add(sc.Text())
		}
	}()
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()

	// 等 worker 报告 ready，最多 60 秒（首次加载模型可能较慢）。
	select {
	case line, ok := <-p.lines:
		if !ok {
			return nil, fmt.Errorf("adapters: 本地语音引擎启动失败：%s", p.stderr.tail())
		}
		var msg struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Model   string `json:"model"`
		}
		_ = json.Unmarshal(line, &msg)
		if msg.Type == "error" {
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("adapters: 本地语音引擎初始化失败：%s", msg.Message)
		}
		if msg.Type != "ready" {
			// 兼容：首行不是 ready 时继续等待。
			return nil, fmt.Errorf("adapters: 本地语音引擎响应异常：%s", Clip(string(line), 200))
		}
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("adapters: 本地语音引擎启动超时（模型加载过慢）")
	case <-p.done:
		return nil, fmt.Errorf("adapters: 本地语音引擎退出：%s", p.stderr.tail())
	}
	return p, nil
}

func (e *localEngine) killLocked() {
	if e.process == nil {
		return
	}
	p := e.process
	e.process = nil
	_ = p.stdin.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
	}
}

// locateScript 找到 sherpa_worker.py：优先程序目录，其次配置，最后当前目录。
func (e *localEngine) locateScript() (string, error) {
	var dirs []string
	if e.cfg.ScriptRoot != "" {
		dirs = append(dirs, e.cfg.ScriptRoot)
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	for _, dir := range dirs {
		for _, rel := range []string{
			filepath.Join("scripts", "sherpa_worker.py"),
			"sherpa_worker.py",
		} {
			cand := filepath.Join(dir, rel)
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				return cand, nil
			}
		}
	}
	return "", fmt.Errorf("adapters: 找不到 scripts/sherpa_worker.py（本地语音引擎脚本缺失）")
}

// findPython 找到可用的 64 位 Python，并返回前置参数（如 -3.14-64）。
func findPython() (string, []string, error) {
	candidates := []struct {
		exe  string
		args []string
	}{
		{"python", nil},
		{"python3", nil},
		{"py", []string{"-3"}},
	}
	for _, c := range candidates {
		path, err := exec.LookPath(c.exe)
		if err != nil {
			continue
		}
		if verifyPython(path, c.args) {
			return path, c.args, nil
		}
	}
	// 常见安装位置（PATH 未包含时）。
	local := os.Getenv("LOCALAPPDATA")
	if local != "" {
		matches, _ := filepath.Glob(filepath.Join(local, "Programs", "Python", "Python3*", "python.exe"))
		for _, m := range matches {
			if verifyPython(m, nil) {
				return m, nil, nil
			}
		}
	}
	return "", nil, fmt.Errorf("adapters: 未找到可用的 Python（本地语音引擎需要 64 位 Python 3.8+，请先运行 setup_local_asr.bat）")
}

func verifyPython(path string, args []string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmdArgs := append(append([]string{}, args...), "-c", "import sherpa_onnx, sys; print('ok')")
	cmd := exec.CommandContext(ctx, path, cmdArgs...)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "ok")
}

// ---- 识别 ----

func (e *localEngine) Transcribe(audioPath string, opts Options, onProgress func(float64)) ([]Segment, Info, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return nil, Info{}, fmt.Errorf("adapters: 本地引擎已关闭")
	}
	if e.process != nil {
		select {
		case <-e.process.done:
			e.process = nil
		default:
		}
	}
	if e.process == nil {
		p, err := e.startProcess()
		if err != nil {
			return nil, Info{}, err
		}
		e.process = p
	}
	e.seq++
	id := fmt.Sprintf("r%d", e.seq)

	req := map[string]any{
		"type":     "transcribe",
		"id":       id,
		"audio":    audioPath,
		"language": firstNonEmpty(opts.Language, e.cfg.Language),
		"model":    firstNonEmpty(opts.Model, e.cfg.ModelID),
		"prompt":   opts.Prompt,
	}
	payload, _ := json.Marshal(req)
	if _, err := e.process.stdin.Write(append(payload, '\n')); err != nil {
		e.killLocked()
		return nil, Info{}, fmt.Errorf("adapters: 本地语音引擎通信失败（%v），已自动重启", err)
	}

	deadline := time.Now().Add(e.timeout)
	for {
		if time.Now().After(deadline) {
			e.killLocked()
			return nil, Info{}, fmt.Errorf("adapters: 本地识别超时（%s）", e.timeout)
		}
		select {
		case <-e.process.done:
			stderr := e.process.stderr.tail()
			e.process = nil
			return nil, Info{}, fmt.Errorf("adapters: 本地语音引擎异常退出：%s", stderr)
		case line, ok := <-e.process.lines:
			if !ok {
				stderr := e.process.stderr.tail()
				e.process = nil
				return nil, Info{}, fmt.Errorf("adapters: 本地语音引擎无响应：%s", stderr)
			}
			var msg struct {
				Type     string   `json:"type"`
				ID       string   `json:"id"`
				Pct      float64  `json:"pct"`
				Stage    string   `json:"stage"`
				Message  string   `json:"message"`
				Language string   `json:"language"`
				Model    string   `json:"model"`
				Segments []struct {
					Start float64 `json:"start"`
					End   float64 `json:"end"`
					Text  string  `json:"text"`
				} `json:"segments"`
			}
			if err := json.Unmarshal(line, &msg); err != nil {
				continue
			}
			if msg.ID != "" && msg.ID != id {
				continue // 上一次超时请求的迟到消息
			}
			switch msg.Type {
			case "progress":
				if onProgress != nil {
					onProgress(msg.Pct)
				}
			case "result":
				segs := make([]Segment, 0, len(msg.Segments))
				for _, s := range msg.Segments {
					txt := strings.TrimSpace(s.Text)
					if txt == "" || s.End < s.Start {
						continue
					}
					segs = append(segs, Segment{
						Start: secToDuration(s.Start),
						End:   secToDuration(s.End),
						Text:  txt,
					})
				}
				return segs, Info{
					Model:            msg.Model,
					DetectedLanguage: msg.Language,
				}, nil
			case "error":
				return nil, Info{}, fmt.Errorf("adapters: 识别失败：%s", msg.Message)
			}
		}
	}
}

func secToDuration(v float64) time.Duration {
	if v < 0 {
		v = 0
	}
	return time.Duration(v * float64(time.Second))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// ---- 小工具 ----

type ringBuffer struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func newRingBuffer(max int) *ringBuffer { return &ringBuffer{max: max} }

func (r *ringBuffer) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
}

func (r *ringBuffer) tail() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) == 0 {
		return "无输出"
	}
	return strings.Join(r.lines, " | ")
}

// Clip 截断字符串。
func Clip(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

var _ = runtime.GOOS
