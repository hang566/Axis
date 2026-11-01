// Package server 是「视听工作台」的本地 HTTP 服务：
// 提供项目库、任务队列、流式进度（SSE）、媒体流与内置网页界面。
package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/local/stt-sub/adapters"
	"github.com/local/stt-sub/config"
	"github.com/local/stt-sub/data"
	"github.com/local/stt-sub/jobs"
	"github.com/local/stt-sub/library"
	"github.com/local/stt-sub/media"
)

// Version 是产品版本号。
const Version = "3.0.0"

// App 汇总服务所需的全部依赖。
type App struct {
	Cfg        config.Config
	Lib        *library.Library
	Jobs       *jobs.Manager
	Media      *media.Media
	Settings   *SettingsStore
	DataDir    string
	ProjectDir string
	ModelsDir  string
	ScriptDir  string
	LogDir     string
	startedAt  time.Time

	// transcriber 在配置变化时重建；用锁保护。
	trMu sync.Mutex
	tr   adapters.Transcriber
	trName string
	trErr  string
}

// NewApp 初始化全部子系统（不启动监听）。
func NewApp(cfg config.Config, dataDir string) (*App, error) {
	cfg.Normalize()
	if err := data.EnsureLayout(dataDir); err != nil {
		return nil, err
	}
	// 元数据（SQLite/设置）放 data/，素材与项目文件放独立的 project/。
	projectDir := data.ResolveProjectDir(dataDir, cfg.General.ProjectDir)
	lib, err := library.OpenAt(dataDir, projectDir)
	if err != nil {
		return nil, fmt.Errorf("打开资料库失败：%w", err)
	}
	// 索引缺失/损坏时从项目目录自愈。
	if _, err := lib.Recover(); err != nil {
		log.Printf("[library] 扫描项目目录失败：%v", err)
	}
	jobMgr, err := jobs.NewManager(filepath.Join(projectDir, data.JobsDir))
	if err != nil {
		return nil, fmt.Errorf("初始化任务队列失败：%w", err)
	}
	m := media.New(cfg.General.FFmpegPath)
	m.SetTempDir(filepath.Join(projectDir, data.TempDir))

	settings, err := OpenSettings(filepath.Join(dataDir, "settings.json"), cfg)
	if err != nil {
		log.Printf("[settings] 读取设置失败，使用默认值：%v", err)
		settings = &SettingsStore{path: filepath.Join(dataDir, "settings.json"), cfg: cfg}
	}

	modelsDir := cfg.General.ModelsDir
	if !filepath.IsAbs(modelsDir) {
		modelsDir = filepath.Join(scriptRoot(dataDir), modelsDir)
	}
	app := &App{
		Cfg:        cfg,
		Lib:        lib,
		Jobs:       jobMgr,
		Media:      m,
		Settings:   settings,
		DataDir:    dataDir,
		ProjectDir: projectDir,
		ModelsDir:  modelsDir,
		ScriptDir:  scriptRoot(dataDir),
		LogDir:     filepath.Join(dataDir, data.LogsDir),
		startedAt:  time.Now(),
	}
	app.registerRunners()
	// 启动时清理一天前的临时文件与临时项目（避免无限增长）。
	go app.housekeeping()
	return app, nil
}

// scriptRoot 返回程序（或当前目录）位置，用于定位 models/ 与 scripts/。
func scriptRoot(dataDir string) string {
	// data 目录在程序目录下时，上一级就是程序目录。
	if filepath.Base(filepath.Clean(dataDir)) == "data" {
		return filepath.Dir(filepath.Clean(dataDir))
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	wd, _ := os.Getwd()
	return wd
}

func (a *App) housekeeping() {
	time.Sleep(20 * time.Second)
	removed := a.Lib.CleanTemp(24 * time.Hour)
	stale := a.Lib.PurgeStaleTemporary(72 * time.Hour)
	pruned := a.Jobs.Prune(7 * 24 * time.Hour)
	if removed+stale+pruned > 0 {
		log.Printf("[maintenance] 清理临时文件 %d 个、临时项目 %d 个、历史任务 %d 个", removed, stale, pruned)
	}
}

// Transcriber 返回当前识别引擎（失败时返回错误说明）。
func (a *App) Transcriber() (adapters.Transcriber, error) {
	a.trMu.Lock()
	defer a.trMu.Unlock()
	if a.tr != nil {
		return a.tr, nil
	}
	if a.trErr != "" {
		return nil, errors.New(a.trErr)
	}
	tr, name, err := a.buildTranscriber()
	if err != nil {
		a.trErr = err.Error()
		return nil, err
	}
	a.tr = tr
	a.trName = name
	return tr, nil
}

// ResetTranscriber 在设置变化后丢弃旧引擎（下次调用时重建）。
func (a *App) ResetTranscriber() {
	a.trMu.Lock()
	old := a.tr
	a.tr, a.trErr, a.trName = nil, "", ""
	a.trMu.Unlock()
	if old != nil {
		if initer, ok := old.(adapters.Initer); ok {
			_ = initer.Close()
		}
	}
}

// Close 释放资源。
func (a *App) Close() {
	a.trMu.Lock()
	old := a.tr
	a.tr = nil
	a.trMu.Unlock()
	if old != nil {
		if initer, ok := old.(adapters.Initer); ok {
			_ = initer.Close()
		}
	}
}

func (a *App) buildTranscriber() (adapters.Transcriber, string, error) {
	s := a.Settings.Get()
	engine := s.Engine
	if engine == "" {
		engine = "local"
	}
	acfg := adapters.Config{
		Endpoint:   s.RemoteEndpoint,
		APIKey:     s.APIKey,
		APIKeyEnv:  s.APIKeyEnv,
		Model:      s.RemoteModel,
		ModelPath:  a.Cfg.STT.ModelPath,
		ModelsDir:  a.ModelsDir,
		ModelID:    s.ModelID,
		Language:   s.Language,
		Command:    a.Cfg.STT.Command,
		Args:       a.Cfg.STT.Args,
		OutputFile: a.Cfg.STT.OutputFile,
		Timeout:    time.Duration(s.TimeoutSeconds) * time.Second,
		Extra:      a.Cfg.STT.Extra,
		ScriptRoot: a.ScriptDir,
	}
	tr, err := adapters.New(engine, acfg)
	if err != nil {
		return nil, "", err
	}
	if initer, ok := tr.(adapters.Initer); ok {
		if err := initer.Init(); err != nil {
			return nil, "", fmt.Errorf("%s 初始化失败：%w", tr.Name(), err)
		}
	}
	return tr, tr.Name(), nil
}

// EngineName 返回当前引擎名（不触发初始化）。
func (a *App) EngineName() string {
	a.trMu.Lock()
	defer a.trMu.Unlock()
	if a.trName != "" {
		return a.trName
	}
	return ""
}

// ---- 路由 ----

// Handler 返回 HTTP 路由。
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()

	// 页面与静态资源
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/static/", a.handleStatic)

	// 系统
	mux.HandleFunc("/api/system", a.handleSystem)
	mux.HandleFunc("/api/system/check", a.handleSystemCheck)
	mux.HandleFunc("/api/settings", a.handleSettings)
	mux.HandleFunc("/api/events", a.handleEvents)
	mux.HandleFunc("/api/stats", a.handleStats)

	// 项目库
	mux.HandleFunc("/api/projects", a.handleProjects)
	mux.HandleFunc("/api/projects/", a.handleProjectRoutes)
	mux.HandleFunc("/api/ingest", a.handleIngest)
	mux.HandleFunc("/api/assets/", a.handleAsset)

	// 组织：文件夹与项目集
	mux.HandleFunc("/api/folders", a.handleFolders)
	mux.HandleFunc("/api/folders/", a.handleFolderRoutes)
	mux.HandleFunc("/api/collections", a.handleCollections)
	mux.HandleFunc("/api/collections/", a.handleCollectionRoutes)
	mux.HandleFunc("/api/library/batch", a.handleBatch)
	mux.HandleFunc("/api/library/trash", a.handleEmptyTrash)

	// 任务
	mux.HandleFunc("/api/jobs", a.handleJobs)
	mux.HandleFunc("/api/jobs/", a.handleJobRoutes)

	// 转码
	mux.HandleFunc("/api/transcode/presets", a.handleTranscodePresets)
	mux.HandleFunc("/api/transcode/preview", a.handleTranscodePreview)

	// 模型
	mux.HandleFunc("/api/models", a.handleModels)
	mux.HandleFunc("/api/models/", a.handleModelRoutes)

	return withLogging(withSecurityHeaders(mux))
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		if strings.HasPrefix(r.URL.Path, "/api/events") {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}

// Listen 启动服务并阻塞。
func (a *App) Listen(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("无法监听 %s：%w（端口可能已被占用）", addr, err)
	}
	srv := &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: 20 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		// 关闭 SQLite 索引库（WAL 落盘）。
		if err := a.Lib.Close(); err != nil {
			log.Printf("[library] 关闭索引数据库失败：%v", err)
		}
	}()
	log.Printf("[server] 视听工作台已启动：http://%s", displayAddr(ln.Addr().String()))
	return srv.Serve(ln)
}

func displayAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err == nil && (host == "" || host == "0.0.0.0" || host == "::") {
		return "127.0.0.1:" + port
	}
	return addr
}
