// Package adapters 是 STT 模型适配层：主程序只依赖 Transcriber 接口，
// 新增模型只需实现接口并注册，无需改动流水线。
package adapters

import (
	"errors"
	"time"
)

// ErrUnsupported 表示适配器不支持请求的能力。
var ErrUnsupported = errors.New("adapters: 当前适配器不支持该能力")

// ErrRestartable 表示进程/连接已损坏，重建适配器后可以重试。
var ErrRestartable = errors.New("adapters: 适配器需要重建")

// Segment 是一段识别结果。
type Segment struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

// Options 是识别参数，尽量保持后端无关。
type Options struct {
	Language    string            // 语言代码（空 = 自动检测）
	Prompt      string            // 提示词
	Model       string            // 运行时覆盖模型
	Temperature float64           // 采样温度
	Extra       map[string]string // 透传参数
}

// Info 描述一次识别的附加信息。
type Info struct {
	Model            string  `json:"model,omitempty"`
	DetectedLanguage string  `json:"detectedLanguage,omitempty"`
	ElapsedSeconds   float64 `json:"elapsedSeconds,omitempty"`
}

// Transcriber 是所有 STT 后端必须实现的接口。
// onProgress 收到 0-1 的识别进度（可为 nil）。
type Transcriber interface {
	Transcribe(audioPath string, opts Options, onProgress func(float64)) ([]Segment, Info, error)
	Name() string
}

// Initer 是需要预加载资源的适配器可选实现。
type Initer interface {
	Init() error
	Close() error
}

// Config 是构造适配器所需的全部外部信息。
type Config struct {
	Endpoint  string
	APIKey    string
	APIKeyEnv string
	Model     string
	ModelPath string
	ModelsDir string
	ModelID   string
	Language  string
	Command   string
	Args      string
	OutputFile string
	Timeout   time.Duration
	Extra     map[string]string
	// ScriptRoot 是程序目录（用于定位 scripts/ 下的本地引擎脚本）。
	ScriptRoot string
}

// Factory 根据配置创建适配器。
type Factory func(cfg Config) (Transcriber, error)

var factories = map[string]Factory{}

// Register 注册 provider（在 init 中调用）。
func Register(name string, f Factory) {
	if name == "" || f == nil {
		panic("adapters: 注册参数非法")
	}
	if _, exists := factories[name]; exists {
		panic("adapters: provider 重复注册：" + name)
	}
	factories[name] = f
}

// Names 返回已注册的 provider（按注册顺序不保证，界面会自行排序）。
func Names() []string {
	out := make([]string, 0, len(factories))
	for name := range factories {
		out = append(out, name)
	}
	return out
}

// Exists 判断是否已注册。
func Exists(name string) bool {
	_, ok := factories[name]
	return ok
}

// New 创建适配器实例。
func New(name string, cfg Config) (Transcriber, error) {
	factory, ok := factories[name]
	if !ok {
		return nil, errors.New("adapters: 未知的识别引擎：" + name)
	}
	if cfg.Extra == nil {
		cfg.Extra = map[string]string{}
	}
	return factory(cfg)
}
