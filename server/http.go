package server

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/local/stt-sub/jobs"
	"github.com/local/stt-sub/library"
)

//go:embed all:web
var webAssets embed.FS

// MaxUpload 是单次上传的体积上限（本地工作台，放宽到 8GB）。
const MaxUpload int64 = 8 << 30

// apiError 是带状态码与可操作建议的错误。
type apiError struct {
	Status int
	Msg    string
	Hint   string
}

func (e apiError) Error() string { return e.Msg }

func errBad(msg string) error  { return apiError{Status: http.StatusBadRequest, Msg: msg} }
func errNotFound(msg string) error {
	return apiError{Status: http.StatusNotFound, Msg: msg}
}
func errConflict(msg string) error { return apiError{Status: http.StatusConflict, Msg: msg} }
func errServer(msg string) error {
	return apiError{Status: http.StatusInternalServerError, Msg: msg}
}

// writeError 翻译错误并输出 JSON。
func writeError(w http.ResponseWriter, err error) {
	var ae apiError
	if errors.As(err, &ae) {
		writeJSON(w, ae.Status, map[string]any{"error": ae.Msg, "hint": ae.Hint})
		return
	}
	if errors.Is(err, library.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "内容不存在或已被删除"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
}

// decodeJSON 读取请求体 JSON（限制 2MB）。
func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return errBad("请求体为空")
		}
		return errBad("请求格式不正确：" + err.Error())
	}
	return nil
}

// ---- 页面与静态资源 ----

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		// 前端是单页应用，未知路径回落到首页（避免刷新 404）。
		if !strings.HasPrefix(r.URL.Path, "/api/") && !strings.Contains(path.Base(r.URL.Path), ".") {
			a.serveAsset(w, "index.html")
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "接口不存在：" + r.URL.Path})
		return
	}
	a.serveAsset(w, "index.html")
}

func (a *App) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	if name == "" || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	a.serveAsset(w, name)
}

func (a *App) serveAsset(w http.ResponseWriter, name string) {
	raw, err := webAssets.ReadFile("web/" + name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", assetContentType(name))
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(raw)
}

func assetContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}

// Entry 返回「打开工作台首页」的处理器：适合 start.bat 这类固定地址的入口，
// 供 serve 在随机端口时跳转使用。
func (a *App) Entry(w http.ResponseWriter, r *http.Request) {
	a.handleIndex(w, r)
}

// ---- 系统 ----

func (a *App) handleSystem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 GET"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":      "视听工作台",
		"version":   Version,
		"startedAt": a.startedAt,
		"dataDir":    a.DataDir,
		"projectDir": a.ProjectDir,
		"modelsDir":  a.ModelsDir,
		"platform":  platformName(),
		"settings":  a.Settings.Get(),
		"engine":    a.EngineName(),
	})
}

func (a *App) handleSystemCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 GET"})
		return
	}
	writeJSON(w, http.StatusOK, a.CheckEnvironment())
}

func (a *App) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 GET"})
		return
	}
	stats := a.Lib.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"library":   stats,
		"activeJobs": a.Jobs.ActiveCount(),
		"recentJobs": a.Jobs.List(jobs.ListFilter{Limit: 8}),
		"projects":  a.Lib.ListProjects(library.ProjectListOptions{Limit: 6}),
	})
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"settings": a.Settings.Get(),
			"defaults": DefaultSettings(a.Cfg),
		})
	case http.MethodPost, http.MethodPut:
		var next Settings
		if err := decodeJSON(r, &next); err != nil {
			writeError(w, err)
			return
		}
		next = sanitizeSettings(next, a.Settings.Get())
		if err := a.Settings.Update(next); err != nil {
			writeError(w, errServer("保存设置失败："+err.Error()))
			return
		}
		// ffmpeg/模型路径可能变了：让引擎与媒体层重新解析。
		a.ResetTranscriber()
		a.Media = newMediaFor(next.FFmpegPath, a.ProjectDir)
		if strings.TrimSpace(next.ModelsDir) != "" {
			if filepath.IsAbs(next.ModelsDir) {
				a.ModelsDir = next.ModelsDir
			} else {
				a.ModelsDir = filepath.Join(a.ScriptDir, next.ModelsDir)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"settings": a.Settings.Get(), "saved": true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 GET / POST"})
	}
}

// sanitizeSettings 兜底非法输入，避免界面把服务搞坏。
func sanitizeSettings(next, prev Settings) Settings {
	if next.Engine != "remote" && next.Engine != "cli" {
		next.Engine = "local"
	}
	if next.OutputFormat != "srt" && next.OutputFormat != "vtt" && next.OutputFormat != "json" {
		next.OutputFormat = "srt"
	}
	if next.Concurrency < 1 {
		next.Concurrency = 1
	}
	if next.Concurrency > 4 {
		next.Concurrency = 4
	}
	if next.TimeoutSeconds < 60 {
		next.TimeoutSeconds = 60
	}
	if next.TimeoutSeconds > 7200 {
		next.TimeoutSeconds = 7200
	}
	if next.ChunkMinutes < 0 {
		next.ChunkMinutes = 0
	}
	if next.ChunkMinutes > 60 {
		next.ChunkMinutes = 60
	}
	if next.SubMaxLineLength < 10 || next.SubMaxLineLength > 120 {
		next.SubMaxLineLength = 42
	}
	if next.SubMaxLines < 1 || next.SubMaxLines > 4 {
		next.SubMaxLines = 2
	}
	if next.SubMaxCueSeconds < 1 || next.SubMaxCueSeconds > 20 {
		next.SubMaxCueSeconds = 6
	}
	if next.SubMinCueMs < 100 || next.SubMinCueMs > 3000 {
		next.SubMinCueMs = 700
	}
	if next.SubMaxPauseMs < 100 || next.SubMaxPauseMs > 5000 {
		next.SubMaxPauseMs = 1500
	}
	if next.Theme != "light" && next.Theme != "dark" {
		next.Theme = "auto"
	}
	return next
}

func platformName() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}
