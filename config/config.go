// Package config 负责读取与校验 config.yaml（flag 覆盖由 main 完成）。
//
// 配置分两层：
//   - config.yaml：随程序分发的默认值，普通用户不需要改；
//   - data/settings.json：界面里改的可调参数（并发、字幕风格、默认语言等），
//     由 server 层读写在 YAML 之上，避免把「界面设置」写回出厂配置。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是完整的配置文件结构。
type Config struct {
	General  General  `yaml:"general"`
	Audio    Audio    `yaml:"audio"`
	STT      STT      `yaml:"stt"`
	Subtitle Subtitle `yaml:"subtitle"`
}

// General 是通用运行参数。
type General struct {
	FFmpegPath  string `yaml:"ffmpeg_path"`
	WorkDir     string `yaml:"work_dir"`
	ModelsDir   string `yaml:"models_dir"`
	DataDir     string `yaml:"data_dir"`
	// ProjectDir 是内容文件目录（素材、项目、任务、临时文件）；
	// 留空 = data 同级目录下的 project/。元数据始终保存在 data/library.db。
	ProjectDir  string `yaml:"project_dir"`
	KeepTemp    bool   `yaml:"keep_temp"`
	Concurrency int    `yaml:"concurrency"`
	Progress    *bool  `yaml:"progress"`
	Verbose     bool   `yaml:"verbose"`
}

// Audio 是音频预处理参数。
type Audio struct {
	SampleRate int      `yaml:"sample_rate"`
	Channels   int      `yaml:"channels"`
	Chunk      Duration `yaml:"chunk_seconds"`
}

// STT 是模型后端配置，切换 provider 即可切换模型。
type STT struct {
	Provider    string            `yaml:"provider"`
	Endpoint    string            `yaml:"endpoint"`
	APIKey      string            `yaml:"api_key"`
	APIKeyEnv   string            `yaml:"api_key_env"`
	Model       string            `yaml:"model"`
	ModelPath   string            `yaml:"model_path"`
	ModelID     string            `yaml:"model_id"`
	Language    string            `yaml:"language"`
	Prompt      string            `yaml:"prompt"`
	Temperature float64           `yaml:"temperature"`
	Command     string            `yaml:"command"`
	Args        string            `yaml:"args"`
	OutputFile  string            `yaml:"output_file"`
	Timeout     Duration          `yaml:"timeout"`
	Extra       map[string]string `yaml:"extra"`
}

// Subtitle 是字幕输出参数。
type Subtitle struct {
	Format         string   `yaml:"format"`
	MaxLineLength  int      `yaml:"max_line_length"`
	MaxLines       int      `yaml:"max_lines"`
	MaxCharsPerCue int      `yaml:"max_chars_per_cue"`
	MaxDuration    Duration `yaml:"max_duration"`
	MinDuration    Duration `yaml:"min_duration"`
	MaxPause       Duration `yaml:"max_pause"`
	SentenceMerge  *bool    `yaml:"sentence_merge"`
	// StripPunctuation 为 true 时，字幕输出不带标点（断句仍按标点判断）。
	StripPunctuation *bool `yaml:"strip_punctuation"`
}

// Duration 是对 time.Duration 的 YAML 友好封装，支持 "6s" / "10m" 等字符串。
type Duration time.Duration

// UnmarshalYAML 支持字符串（"6s"）与纯数字（按秒）两种写法。
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Kind == yaml.ScalarNode && strings.TrimSpace(value.Value) == "" {
		*d = 0
		return nil
	}
	var s string
	if err := value.Decode(&s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			*d = 0
			return nil
		}
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("config: 无法解析时长 %q：%w", s, err)
		}
		*d = Duration(parsed)
		return nil
	}
	var num float64
	if err := value.Decode(&num); err == nil {
		*d = Duration(time.Duration(num * float64(time.Second)))
		return nil
	}
	return fmt.Errorf("config: 无法把 %v 解析为时长", value.Value)
}

// MarshalYAML 以 "6s" 形式写回，保持配置文件可读。
func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

// Default 返回内置默认配置。
func Default() Config {
	yes, progress := true, true
	return Config{
		General: General{
			FFmpegPath:  "ffmpeg",
			ModelsDir:   "models",
			Concurrency: 1,
			Progress:    &progress,
		},
		Audio: Audio{
			SampleRate: 16000,
			Channels:   1,
		},
		STT: STT{
			Provider:  "local_command",
			APIKeyEnv: "OPENAI_API_KEY",
			Endpoint:  "https://api.openai.com/v1/audio/transcriptions",
			Model:     "whisper-1",
			Timeout:   Duration(30 * time.Minute),
			Extra:     map[string]string{},
		},
		Subtitle: Subtitle{
			Format:           "srt",
			MaxLineLength:    42,
			MaxLines:         2,
			MaxCharsPerCue:   84,
			MaxDuration:      Duration(6 * time.Second),
			MinDuration:      Duration(700 * time.Millisecond),
			MaxPause:         Duration(1500 * time.Millisecond),
			SentenceMerge:    &yes,
			StripPunctuation: &yes,
		},
	}
}

// LoadFile 读取 YAML 配置文件，并与默认值合并。
// 文件不存在时返回默认值（仅当 require==true 时才报错）。
func LoadFile(path string, require bool) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !require {
			return cfg, nil
		}
		return cfg, fmt.Errorf("config: 读取 %s 失败：%w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("config: 解析 %s 失败：%w", path, err)
	}
	if cfg.STT.Extra == nil {
		cfg.STT.Extra = map[string]string{}
	}
	return cfg, nil
}

// WriteFile 以原子方式把配置写回 YAML（先写 .tmp 再改名，防止写一半断电损坏）。
func WriteFile(path string, cfg Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	header := "# 视听工作台配置文件\n" +
		"# 由程序在保存设置时生成；手工修改同样生效，重启后读取。\n\n"
	return atomicWrite(path, append([]byte(header), data...))
}

func atomicWrite(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if f, err := os.Open(tmp); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Normalize 兜底补齐零值与非法值，保证下游无需处处校验。
func (c *Config) Normalize() {
	if c.General.Concurrency < 1 {
		c.General.Concurrency = 1
	}
	if c.General.Concurrency > 4 {
		c.General.Concurrency = 4
	}
	if c.Audio.SampleRate <= 0 {
		c.Audio.SampleRate = 16000
	}
	if c.Audio.Channels <= 0 {
		c.Audio.Channels = 1
	}
	if strings.TrimSpace(c.General.ModelsDir) == "" {
		c.General.ModelsDir = "models"
	}
	if strings.TrimSpace(c.STT.Provider) == "" {
		c.STT.Provider = "local_command"
	}
	if c.STT.Timeout <= 0 {
		c.STT.Timeout = Duration(30 * time.Minute)
	}
	format := strings.ToLower(strings.TrimSpace(c.Subtitle.Format))
	if format == "" {
		c.Subtitle.Format = "srt"
	} else if format != "srt" && format != "vtt" && format != "json" {
		c.Subtitle.Format = "srt"
	}
	if c.Subtitle.MaxLineLength <= 0 {
		c.Subtitle.MaxLineLength = 42
	}
	if c.Subtitle.MaxLines <= 0 {
		c.Subtitle.MaxLines = 2
	}
	if c.Subtitle.MaxCharsPerCue <= 0 {
		c.Subtitle.MaxCharsPerCue = c.Subtitle.MaxLineLength * c.Subtitle.MaxLines
	}
	if c.Subtitle.MaxDuration <= 0 {
		c.Subtitle.MaxDuration = Duration(6 * time.Second)
	}
	if c.Subtitle.MinDuration <= 0 {
		c.Subtitle.MinDuration = Duration(700 * time.Millisecond)
	}
	if c.Subtitle.MaxPause <= 0 {
		c.Subtitle.MaxPause = Duration(1500 * time.Millisecond)
	}
}

// SentenceMergeOn 返回是否开启按句合并（nil 时默认开启）。
func (c *Config) SentenceMergeOn() bool {
	return c.Subtitle.SentenceMerge == nil || *c.Subtitle.SentenceMerge
}

// StripPunctuationOn 返回字幕是否需要去掉标点（nil 时默认去掉）。
func (c *Config) StripPunctuationOn() bool {
	return c.Subtitle.StripPunctuation == nil || *c.Subtitle.StripPunctuation
}

// ProgressOn 返回是否显示进度条（nil 时默认开启）。
func (c *Config) ProgressOn() bool {
	return c.General.Progress == nil || *c.General.Progress
}

// LocalEngine 返回是否使用内置的 sherpa-onnx 本地引擎。
func (c *Config) LocalEngine() bool {
	p := strings.ToLower(strings.TrimSpace(c.STT.Provider))
	return p == "local_command" || p == "local" || p == "cli" || p == "sherpa"
}
