package adapters

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// localCLI 调用任意「命令行 STT 工具」（whisper.cpp、faster-whisper CLI 等）。
// 输出支持 JSON / SRT / 纯文本，并自动处理 Windows 代码页乱码。
type localCLI struct {
	name      string
	cfg       Config
	bin       string
	argsTpl   []string
	outputTpl string
	env       []string
}

func newLocalCLI(cfg Config) (Transcriber, error) {
	bin := firstNonEmpty(cfg.Command, "whisper-cli")
	argsRaw := strings.TrimSpace(cfg.Args)
	outputRaw := strings.TrimSpace(cfg.OutputFile)
	if argsRaw == "" {
		argsRaw = "-m {model} -f {audio} -oj -of {out}"
		if strings.TrimSpace(cfg.Language) != "" {
			argsRaw = "-m {model} -f {audio} -l {language} -oj -of {out}"
		}
		if outputRaw == "" {
			outputRaw = "{out}.json"
		}
	}
	cli := &localCLI{
		name:      "命令行引擎",
		cfg:       cfg,
		bin:       bin,
		argsTpl:   splitArgs(argsRaw),
		outputTpl: outputRaw,
		env:       parseEnvList(cfg.Extra["env"]),
	}
	if _, err := exec.LookPath(bin); err != nil {
		if _, statErr := os.Stat(bin); statErr != nil {
			return nil, fmt.Errorf("找不到命令 %q：请在设置中填写程序完整路径（本地引擎推荐用内置 sherpa-onnx）", bin)
		}
	}
	return cli, nil
}

func (l *localCLI) Name() string { return l.name }

func (l *localCLI) Transcribe(audioPath string, opts Options, onProgress func(float64)) ([]Segment, Info, error) {
	if _, err := os.Stat(audioPath); err != nil {
		return nil, Info{}, fmt.Errorf("无法读取音频：%w", err)
	}
	timeout := l.cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	values := l.templateValues(audioPath, opts)
	args := make([]string, 0, len(l.argsTpl))
	for _, item := range l.argsTpl {
		if rendered := renderTemplate(item, values); rendered != "" {
			args = append(args, rendered)
		}
	}
	bin, fullArgs := wrapCommand(l.bin, args)

	cmd := exec.CommandContext(ctx, bin, fullArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	if len(l.env) > 0 {
		cmd.Env = append(cmd.Env, l.env...)
	}
	start := time.Now()
	err := cmd.Run()
	if onProgress != nil {
		onProgress(0.9)
	}
	if ctx.Err() == context.DeadlineExceeded {
		return nil, Info{}, fmt.Errorf("命令执行超时（%s）", timeout)
	}
	if err != nil {
		return nil, Info{}, fmt.Errorf("命令执行失败：%v\n%s", err, Clip(DecodeText(stderr.Bytes()), 800))
	}

	data, readErr := l.readResult(values, stdout.Bytes())
	if readErr != nil {
		return nil, Info{}, fmt.Errorf("读取识别结果失败：%v\n%s", readErr, Clip(DecodeText(stderr.Bytes()), 400))
	}
	segs, perr := ParseAny(data, ParseTimeUnit(l.cfg.Extra["time_unit"]))
	if perr != nil {
		return nil, Info{}, perr
	}
	return segs, Info{Model: l.cfg.Model, ElapsedSeconds: time.Since(start).Seconds()}, nil
}

func (l *localCLI) readResult(values map[string]string, stdout []byte) ([]byte, error) {
	if l.outputTpl == "" {
		return stdout, nil
	}
	path := renderTemplate(l.outputTpl, values)
	raw, err := os.ReadFile(path)
	if err != nil {
		if len(bytes.TrimSpace(stdout)) > 0 {
			return stdout, nil
		}
		return nil, fmt.Errorf("找不到结果文件 %s", path)
	}
	return raw, nil
}

func (l *localCLI) templateValues(audioPath string, opts Options) map[string]string {
	absPath := audioPath
	if abs, err := filepath.Abs(audioPath); err == nil {
		absPath = abs
	}
	dir := filepath.Dir(absPath)
	base := strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))
	model := firstNonEmpty(opts.Model, l.cfg.ModelPath, l.cfg.Model)
	return map[string]string{
		"audio":      absPath,
		"audiodir":   dir,
		"base":       base,
		"out":        filepath.Join(dir, base+"_stt"),
		"model":      model,
		"model_path": firstNonEmpty(l.cfg.ModelPath, opts.Model),
		"model_name": firstNonEmpty(opts.Model, l.cfg.Model),
		"language":   firstNonEmpty(opts.Language, l.cfg.Language),
	}
}

func renderTemplate(tpl string, values map[string]string) string {
	var b strings.Builder
	for i := 0; i < len(tpl); {
		if tpl[i] != '{' {
			b.WriteByte(tpl[i])
			i++
			continue
		}
		end := strings.IndexByte(tpl[i:], '}')
		if end < 0 {
			b.WriteString(tpl[i:])
			break
		}
		key := strings.TrimSpace(tpl[i+1 : i+end])
		b.WriteString(values[key])
		i += end + 1
	}
	return b.String()
}

// splitArgs 按空白切分，支持单双引号。
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	quote := byte(0)
	has := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '"' || c == '\'':
			quote = c
			has = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteByte(c)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}

// wrapCommand 在 Windows 上把 .bat/.cmd/.ps1 包装成可执行命令。
func wrapCommand(bin string, args []string) (string, []string) {
	if runtime.GOOS != "windows" {
		return bin, args
	}
	low := strings.ToLower(bin)
	switch {
	case strings.HasSuffix(low, ".ps1"):
		full := append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", bin}, args...)
		return "powershell", full
	case strings.HasSuffix(low, ".bat"), strings.HasSuffix(low, ".cmd"):
		return "cmd", append([]string{"/c", bin}, args...)
	}
	return bin, args
}

func parseEnvList(s string) []string {
	var envs []string
	for _, item := range strings.Split(s, ";") {
		if strings.TrimSpace(item) != "" {
			envs = append(envs, strings.TrimSpace(item))
		}
	}
	return envs
}
