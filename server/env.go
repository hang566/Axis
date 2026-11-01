package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/local/stt-sub/data"
	"github.com/local/stt-sub/store"
)

// EnvStatus 是「开箱即用」自检结果。
type EnvStatus struct {
	Platform      string       `json:"platform"`
	DataDir       string       `json:"dataDir"`
	ProjectDir    string       `json:"projectDir"`
	ModelsDir     string       `json:"modelsDir"`
	FFmpeg        Component    `json:"ffmpeg"`
	FFprobe       Component    `json:"ffprobe"`
	Python        Component    `json:"python"`
	Ready         bool         `json:"ready"`
	Problems      []string     `json:"problems"`
	Disk          DiskInfo     `json:"disk"`
	InstalledModels []ModelInfo `json:"installedModels"`
	FirstRun      bool         `json:"firstRun"`
}

// Component 描述一个外部依赖的状态。
type Component struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message,omitempty"`
	Fix     string `json:"fix,omitempty"` // 给用户的可操作建议
}

// DiskInfo 是磁盘占用概览。
type DiskInfo struct {
	TotalBytes int64 `json:"totalBytes"`
	FreeBytes  int64 `json:"freeBytes"`
	UsedByApp  int64 `json:"usedByApp"`
}

// ModelInfo 是本地已安装模型的信息。
type ModelInfo struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	Kind      string `json:"kind"`
	Languages string `json:"languages"`
	Installed bool   `json:"installed"`
}

// CheckEnvironment 执行完整环境自检。
func (a *App) CheckEnvironment() EnvStatus {
	st := EnvStatus{
		Platform:   runtime.GOOS + "/" + runtime.GOARCH,
		DataDir:    a.DataDir,
		ProjectDir: a.ProjectDir,
		ModelsDir:  a.ModelsDir,
	}
	st.FFmpeg = a.checkFFmpeg()
	st.FFprobe = a.checkFFprobe()
	st.Python = checkPython()
	st.InstalledModels = scanInstalledModels(a.ModelsDir)
	st.Disk = diskInfo(a.ProjectDir)

	if !st.FFmpeg.OK {
		st.Problems = append(st.Problems, "缺少 ffmpeg，无法提取音频与转码")
	}
	if st.Python.OK && len(st.InstalledModels) == 0 {
		st.Problems = append(st.Problems, "还没有下载语音模型，无法在本地识别（可在「模型中心」一键下载）")
	}
	if !st.Python.OK {
		st.Problems = append(st.Problems, "未检测到可用的 Python + sherpa_onnx，本地识别不可用（可改用在线接口）")
	}
	st.Ready = st.FFmpeg.OK && st.Python.OK && len(st.InstalledModels) > 0
	st.FirstRun = a.LibraryEmpty()
	return st
}

// LibraryEmpty 判断是否首次运行（没有任何项目）。
func (a *App) LibraryEmpty() bool {
	return len(a.Lib.ListProjects(libraryListAll())) == 0
}

func (a *App) checkFFmpeg() Component {
	path := a.Media.FFmpeg()
	if path == "" {
		return Component{
			Message: "未找到 ffmpeg",
			Fix:     "把 ffmpeg 解压到程序目录（例如 ffmpeg\\bin\\ffmpeg.exe），或在设置里填写完整路径",
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-version").Output()
	if err != nil {
		return Component{Path: path, Message: "ffmpeg 无法执行：" + err.Error(),
			Fix: "文件可能损坏或被安全软件拦截，请重新下载 ffmpeg"}
	}
	version := firstLine(string(out))
	return Component{OK: true, Version: version, Path: path}
}

func (a *App) checkFFprobe() Component {
	path := a.Media.FFprobe()
	if path == "" {
		return Component{
			Message: "未找到 ffprobe（会影响媒体信息与时长读取）",
			Fix:     "ffprobe 通常和 ffmpeg 一起发布，放在同一个 bin 目录即可",
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-version").Output()
	if err != nil {
		return Component{Path: path, Message: "ffprobe 无法执行"}
	}
	return Component{OK: true, Version: firstLine(string(out)), Path: path}
}

// checkPython 检测 Python + sherpa_onnx（本地识别引擎的依赖）。
func checkPython() Component {
	candidates := []struct {
		exe  string
		args []string
	}{
		{"python", nil},
		{"python3", nil},
		{"py", []string{"-3"}},
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		if matches, _ := filepath.Glob(filepath.Join(local, "Programs", "Python", "Python3*", "python.exe")); len(matches) > 0 {
			sort.Sort(sort.Reverse(sort.StringSlice(matches)))
			for _, m := range matches {
				candidates = append(candidates, struct {
					exe  string
					args []string
				}{m, nil})
			}
		}
	}
	for _, c := range candidates {
		path, err := exec.LookPath(c.exe)
		if err != nil {
			if _, statErr := os.Stat(c.exe); statErr != nil {
				continue
			}
			path = c.exe
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		args := append(append([]string{}, c.args...), "-c",
			"import sys,sherpa_onnx; print(sys.version.split()[0], '|', getattr(sherpa_onnx,'__version__','?'))")
		cmd := exec.CommandContext(ctx, path, args...)
		out, err := cmd.CombinedOutput()
		cancel()
		if err == nil {
			parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 2)
			ver := "Python " + parts[0]
			if len(parts) > 1 {
				ver += " · sherpa-onnx " + strings.TrimSpace(parts[1])
			}
			return Component{OK: true, Version: ver, Path: path}
		}
		// Python 存在但缺 sherpa_onnx：给出安装命令。
		ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
		out2, err2 := exec.CommandContext(ctx2, path, append(append([]string{}, c.args...), "--version")...).CombinedOutput()
		cancel2()
		if err2 == nil {
			return Component{
				Path:    path,
				Version: strings.TrimSpace(string(out2)),
				Message: "已安装 Python，但缺少 sherpa_onnx",
				Fix:     "运行 setup_local_asr.bat，或在命令行执行：" + path + " -m pip install -U sherpa-onnx",
			}
		}
	}
	return Component{
		Message: "未检测到 Python",
		Fix:     "安装 64 位 Python 3.10+ 后运行 setup_local_asr.bat；或改用在线识别接口",
	}
}

// scanInstalledModels 扫描 models/ 下的模型目录。
func scanInstalledModels(modelsDir string) []ModelInfo {
	var out []ModelInfo
	entries, err := os.ReadDir(modelsDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		full := filepath.Join(modelsDir, e.Name())
		kind := detectModelKind(full)
		if kind == "" {
			continue
		}
		info := ModelInfo{
			ID:        e.Name(),
			Path:      full,
			SizeBytes: data.DirSize(full),
			Kind:      kind,
			Installed: true,
			Languages: languagesFor(e.Name()),
		}
		out = append(out, info)
	}
	// 附带 catalog 里已知、但可能没扫描到的单文件模型（如 VAD）。
	for _, m := range store.Catalog {
		dest := filepath.Join(modelsDir, m.Dest)
		if st, err := os.Stat(dest); err == nil && !st.IsDir() {
			out = append(out, ModelInfo{ID: m.ID, Path: dest, SizeBytes: st.Size(), Kind: "辅助", Installed: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SizeBytes > out[j].SizeBytes })
	return out
}

// preferredLocalModel 返回默认使用的本地模型目录名。
// 优先级与引擎内部保持一致：SenseVoice（多语种+标点）> Paraformer > 其它。
func preferredLocalModel(models []ModelInfo) string {
	score := func(m ModelInfo) int {
		switch m.Kind {
		case "SenseVoice":
			return 100
		case "Paraformer":
			return 80
		case "Zipformer":
			return 60
		case "Whisper":
			return 40
		default:
			return 10
		}
	}
	best := ""
	bestScore := -1
	for _, m := range models {
		if s := score(m); s > bestScore {
			best, bestScore = m.ID, s
		}
	}
	return best
}

// detectModelKind 根据目录内的文件判断模型类型。
func detectModelKind(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	hasTokens := false
	hasOnnx := false
	hasEncoder := false
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if name == "tokens.txt" {
			hasTokens = true
		}
		if strings.HasSuffix(name, ".onnx") {
			hasOnnx = true
		}
		if strings.HasPrefix(name, "encoder") {
			hasEncoder = true
		}
	}
	if !hasOnnx {
		return ""
	}
	if hasEncoder {
		return "Zipformer"
	}
	lower := strings.ToLower(dir)
	switch {
	case strings.Contains(lower, "sense"):
		return "SenseVoice"
	case strings.Contains(lower, "paraformer"):
		return "Paraformer"
	case strings.Contains(lower, "whisper"):
		return "Whisper"
	}
	if hasTokens {
		return "ASR"
	}
	return ""
}

func languagesFor(id string) string {
	lower := strings.ToLower(id)
	switch {
	case strings.Contains(lower, "sense-voice"):
		return "中/英/日/韩/粤"
	case strings.Contains(lower, "paraformer-zh"):
		return "中文"
	case strings.Contains(lower, "reazonspeech"):
		return "日/英"
	case strings.Contains(lower, "whisper"):
		return "多语种"
	case strings.Contains(lower, "parakeet"):
		return "英/欧语"
	case strings.Contains(lower, "omnilingual"):
		return "1600+ 语种"
	}
	return "未知"
}

func diskInfo(dir string) DiskInfo {
	var d DiskInfo
	total, free, err := diskUsage(dir)
	if err == nil {
		d.TotalBytes, d.FreeBytes = total, free
	}
	d.UsedByApp = data.DirSize(dir)
	return d
}

func firstLine(s string) string {
	line := strings.SplitN(strings.TrimSpace(s), "\n", 2)[0]
	if len(line) > 160 {
		line = line[:160]
	}
	return strings.TrimSpace(line)
}

var _ = fmt.Sprintf
