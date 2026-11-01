package server

import (
	"path/filepath"
	"sync"

	"github.com/local/stt-sub/config"
	"github.com/local/stt-sub/data"
)

// Settings 是界面里可调整的运行参数，持久化在 data/settings.json。
// 它叠加在 config.yaml 之上：YAML 管「出厂默认」，这里管「用户选择」。
type Settings struct {
	Engine         string `json:"engine"`     // local / remote / cli
	ModelID        string `json:"modelId"`    // 本地模型目录名，空=自动
	Language       string `json:"language"`   // 默认语言，空=自动检测
	OutputFormat   string `json:"outputFormat"`
	Concurrency    int    `json:"concurrency"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
	ChunkMinutes   int    `json:"chunkMinutes"` // 长音频分段长度，0=自动

	SubMaxLineLength  int  `json:"subMaxLineLength"`
	SubMaxLines       int  `json:"subMaxLines"`
	SubMaxCueSeconds  int  `json:"subMaxCueSeconds"`
	SubMinCueMs       int  `json:"subMinCueMs"`
	SubMaxPauseMs     int  `json:"subMaxPauseMs"`
	SubSentenceMerge  bool `json:"subSentenceMerge"`
	SubStripPunct     bool `json:"subStripPunctuation"`

	FFmpegPath string `json:"ffmpegPath"`
	ModelsDir  string `json:"modelsDir"`

	RemoteEndpoint string `json:"remoteEndpoint"`
	RemoteModel    string `json:"remoteModel"`
	APIKey         string `json:"apiKey"`
	APIKeyEnv      string `json:"apiKeyEnv"`

	Theme           string `json:"theme"` // auto / light / dark
	ConfirmDelete   bool   `json:"confirmDelete"`
	AutoOpenBrowser bool   `json:"autoOpenBrowser"`
}

// DefaultSettings 根据 config.yaml 生成初始设置。
func DefaultSettings(cfg config.Config) Settings {
	return Settings{
		Engine:         "local",
		ModelID:        cfg.STT.ModelID,
		Language:       cfg.STT.Language,
		OutputFormat:   "srt",
		Concurrency:    1,
		TimeoutSeconds: 1800,
		ChunkMinutes:   15,

		SubMaxLineLength: cfg.Subtitle.MaxLineLength,
		SubMaxLines:      cfg.Subtitle.MaxLines,
		SubMaxCueSeconds: int(cfg.Subtitle.MaxDuration.Std().Seconds()),
		SubMinCueMs:      int(cfg.Subtitle.MinDuration.Std().Milliseconds()),
		SubMaxPauseMs:    int(cfg.Subtitle.MaxPause.Std().Milliseconds()),
		SubSentenceMerge: cfg.SentenceMergeOn(),
		SubStripPunct:    cfg.StripPunctuationOn(),

		FFmpegPath:       cfg.General.FFmpegPath,
		ModelsDir:        cfg.General.ModelsDir,
		RemoteEndpoint:   cfg.STT.Endpoint,
		RemoteModel:      cfg.STT.Model,
		APIKeyEnv:        cfg.STT.APIKeyEnv,
		Theme:            "auto",
		ConfirmDelete:    true,
		AutoOpenBrowser:  true,
	}
}

// SettingsStore 提供并发安全的读写与持久化。
type SettingsStore struct {
	mu   sync.RWMutex
	path string
	cfg  config.Config
	cur  Settings
}

// OpenSettings 读取 settings.json（不存在则用默认值写入）。
func OpenSettings(path string, cfg config.Config) (*SettingsStore, error) {
	s := &SettingsStore{path: path, cfg: cfg, cur: DefaultSettings(cfg)}
	var loaded Settings
	if err := data.ReadJSON(path, &loaded); err == nil {
		s.cur = mergeSettings(s.cur, loaded)
	} else if err := s.save(); err != nil {
		return nil, err
	}
	return s, nil
}

// mergeSettings 用已保存的值覆盖默认值（零值视为「未设置」）。
func mergeSettings(def, loaded Settings) Settings {
	out := def
	if loaded.Engine != "" {
		out.Engine = loaded.Engine
	}
	out.ModelID = loaded.ModelID
	out.Language = loaded.Language
	if loaded.OutputFormat != "" {
		out.OutputFormat = loaded.OutputFormat
	}
	if loaded.Concurrency > 0 {
		out.Concurrency = loaded.Concurrency
	}
	if loaded.TimeoutSeconds > 0 {
		out.TimeoutSeconds = loaded.TimeoutSeconds
	}
	if loaded.ChunkMinutes > 0 {
		out.ChunkMinutes = loaded.ChunkMinutes
	}
	if loaded.SubMaxLineLength > 0 {
		out.SubMaxLineLength = loaded.SubMaxLineLength
	}
	if loaded.SubMaxLines > 0 {
		out.SubMaxLines = loaded.SubMaxLines
	}
	if loaded.SubMaxCueSeconds > 0 {
		out.SubMaxCueSeconds = loaded.SubMaxCueSeconds
	}
	if loaded.SubMinCueMs > 0 {
		out.SubMinCueMs = loaded.SubMinCueMs
	}
	if loaded.SubMaxPauseMs > 0 {
		out.SubMaxPauseMs = loaded.SubMaxPauseMs
	}
	if loaded.FFmpegPath != "" {
		out.FFmpegPath = loaded.FFmpegPath
	}
	if loaded.ModelsDir != "" {
		out.ModelsDir = loaded.ModelsDir
	}
	out.RemoteEndpoint = loaded.RemoteEndpoint
	out.RemoteModel = loaded.RemoteModel
	out.APIKey = loaded.APIKey
	out.APIKeyEnv = loaded.APIKeyEnv
	if loaded.Theme != "" {
		out.Theme = loaded.Theme
	}
	out.ConfirmDelete = loaded.ConfirmDelete
	out.AutoOpenBrowser = loaded.AutoOpenBrowser
	return out
}

// Get 返回当前设置快照。
func (s *SettingsStore) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Update 合并并保存新设置。
func (s *SettingsStore) Update(next Settings) error {
	s.mu.Lock()
	s.cur = next
	s.mu.Unlock()
	return s.save()
}

func (s *SettingsStore) save() error {
	s.mu.RLock()
	cur := s.cur
	s.mu.RUnlock()
	return data.WriteJSON(s.path, cur)
}

// SubtitleOptions 把设置转换为字幕合并参数。
func (s *SettingsStore) SubtitleOptions() (maxLine, maxLines, maxCueSec, minCueMs, maxPauseMs int, sentence, strip bool) {
	cur := s.Get()
	return cur.SubMaxLineLength, cur.SubMaxLines, cur.SubMaxCueSeconds,
		cur.SubMinCueMs, cur.SubMaxPauseMs, cur.SubSentenceMerge, cur.SubStripPunct
}

// SettingsPath 便于日志展示。
func SettingsPath(dir string) string { return filepath.Join(dir, "settings.json") }
