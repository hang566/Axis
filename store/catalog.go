// Package store 实现「模型商店」：内置常用 ASR / VAD / 标点 / 说话人 / 翻译
// 模型清单，并支持把这些模型下载到本地 models/ 目录，供本地 provider 使用。
//
// 模型清单源自 hayamimi（k2-fsa/sherpa-onnx 发布的 ONNX 模型），下载逻辑为纯 Go
// 实现（单文件流式下载、tar.bz2 解包、Hugging Face 仓库快照下载），无需 Python。
package store

import (
	"fmt"
	"strings"
)

// Tier 表示模型所属集合，便于按需下载。
type Tier string

const (
	TierCore  Tier = "core"  // 基础核心（ja/en）
	TierFull  Tier = "full"  // 完整多语种
	TierOptIn Tier = "optin" // 可选附加
)

// Source 描述一个模型如何获取。
type Source struct {
	Kind string // file | tar | tarmembers | hfrepo
	URL  string // file / tar / tarmembers 的下载地址
	Repo string // hfrepo 的 Hugging Face 仓库名（owner/repo）
	// Wanted 仅 tarmembers 使用：只保留这些 base 文件（其余丢弃）。
	Wanted []string
}

// Model 是模型商店中的一个条目。
type Model struct {
	ID          string
	Name        string
	Description string
	Languages   string
	License     string
	Size        string // 体积估算，仅用于展示
	Tier        Tier
	Dest        string // 相对 models/ 的目标路径（文件或目录）
	Source      Source
}

// 上游发布地址（与 hayamimi 的 scripts/download_models.py 保持一致）。
const (
	ghReleases = "https://github.com/k2-fsa/sherpa-onnx/releases/download"
	asrTag     = "asr-models"
	speakerTag = "speaker-recongition-models"
	segTag     = "speaker-segmentation-models"
)

// Catalog 是模型商店的可下载清单。
var Catalog = []Model{
	{
		ID: "reazonspeech", Name: "ReazonSpeech Zipformer (ja/en)",
		Description: "日语 + 英语 ASR，核心识别模型", Languages: "ja, en",
		License: "Apache-2.0", Size: "~250MB", Tier: TierCore,
		Dest: "sherpa-onnx-zipformer-ja-en-reazonspeech-2025-01-17",
		Source: Source{Kind: "tar", URL: ghReleases + "/" + asrTag +
			"/sherpa-onnx-zipformer-ja-en-reazonspeech-2025-01-17.tar.bz2"},
	},
	{
		ID: "whisper-tiny", Name: "Whisper tiny (LID)",
		Description: "用于语种识别（LID）的轻量模型", Languages: "multilingual",
		License: "MIT", Size: "~75MB", Tier: TierCore,
		Dest: "sherpa-onnx-whisper-tiny",
		Source: Source{Kind: "tar", URL: ghReleases + "/" + asrTag +
			"/sherpa-onnx-whisper-tiny.tar.bz2"},
	},
	{
		ID: "silero-vad", Name: "Silero VAD",
		Description: "语音活动检测（静音切分）", Languages: "universal",
		License: "MIT", Size: "~2MB", Tier: TierCore,
		Dest: "silero_vad.onnx",
		Source: Source{Kind: "file", URL: ghReleases + "/" + asrTag + "/silero_vad.onnx"},
	},
	{
		ID: "mojicast-punct", Name: "Mojicast 日语标点恢复",
		Description: "日语标点恢复（fp32 ONNX）", Languages: "ja",
		License: "MIT", Size: "~100MB", Tier: TierCore,
		Dest: "mojicast-punct-onnx",
		Source: Source{Kind: "hfrepo", Repo: "ishiki-emo/mojicast-punct-onnx"},
	},
	{
		ID: "paraformer-zh", Name: "Paraformer 中文 (int8)",
		Description: "中文 ASR（FunASR Paraformer）", Languages: "zh",
		License: "Apache-2.0", Size: "~50MB", Tier: TierFull,
		Dest: "sherpa-onnx-paraformer-zh-int8-2025-10-07",
		Source: Source{Kind: "tar", URL: ghReleases + "/" + asrTag +
			"/sherpa-onnx-paraformer-zh-int8-2025-10-07.tar.bz2"},
	},
	{
		ID: "sense-voice", Name: "SenseVoice Small (int8)",
		Description: "中/英/日/韩/粤多语种 ASR", Languages: "zh, en, ja, ko, yue",
		License: "Apache-2.0", Size: "~200MB", Tier: TierFull,
		Dest: "sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17",
		Source: Source{Kind: "tar", URL: ghReleases + "/" + asrTag +
			"/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17.tar.bz2"},
	},
	{
		ID: "parakeet-v3", Name: "Parakeet TDT 0.6B v3 (int8)",
		Description: "英语 + 24 种欧洲语言 ASR", Languages: "en, eu",
		License: "Apache-2.0", Size: "~300MB", Tier: TierFull,
		Dest: "sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8",
		Source: Source{Kind: "tar", URL: ghReleases + "/" + asrTag +
			"/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8.tar.bz2"},
	},
	{
		ID: "omnilingual", Name: "Meta Omnilingual ASR 300M (int8)",
		Description: "约 1600 语种兜底 ASR", Languages: "1600+",
		License: "CC-BY-4.0", Size: "~250MB", Tier: TierFull,
		Dest: "omnilingual-300m-ctc-int8",
		Source: Source{Kind: "tarmembers", URL: ghReleases + "/" + asrTag +
			"/sherpa-onnx-omnilingual-asr-1600-languages-300M-ctc-int8-2025-11-12.tar.bz2",
			Wanted: []string{"model.int8.onnx", "tokens.txt"}},
	},
	{
		ID: "campplus-sv", Name: "CAM++ 说话人嵌入",
		Description: "说话人向量（用于区分不同说话人）", Languages: "zh, en",
		License: "Apache-2.0", Size: "~20MB", Tier: TierFull,
		Dest: "campplus_sv.onnx",
		Source: Source{Kind: "file", URL: ghReleases + "/" + speakerTag +
			"/3dspeaker_speech_campplus_sv_zh_en_16k-common_advanced.onnx"},
	},
	{
		ID: "pyannote-seg", Name: "pyannote segmentation 3.0",
		Description: "说话人切换检测（精细发言分界）", Languages: "universal",
		License: "MIT", Size: "~7MB", Tier: TierFull,
		Dest: "sherpa-onnx-pyannote-segmentation-3-0",
		Source: Source{Kind: "tar", URL: ghReleases + "/" + segTag +
			"/sherpa-onnx-pyannote-segmentation-3-0.tar.bz2"},
	},
	{
		ID: "m2m100-ct2", Name: "M2M-100 418M 翻译 (CT2)",
		Description: "日→中/韩 翻译", Languages: "ja→zh, ko",
		License: "MIT", Size: "~1GB", Tier: TierFull,
		Dest: "mojicast-m2m100-ct2",
		Source: Source{Kind: "hfrepo", Repo: "ishiki-emo/mojicast-m2m100-ct2"},
	},
	{
		ID: "fugumt-en", Name: "FuguMT 日→英 翻译 (CT2)",
		Description: "日→英 翻译", Languages: "ja→en",
		License: "CC-BY-SA-4.0", Size: "~300MB", Tier: TierFull,
		Dest: "mojicast-fugumt-ja-en-ct2",
		Source: Source{Kind: "hfrepo", Repo: "ishiki-emo/mojicast-fugumt-ja-en-ct2"},
	},
	{
		ID: "parakeet-v2", Name: "Parakeet TDT 0.6B v2 (int8, opt-in)",
		Description: "英语 ASR 可选增强档（v2）", Languages: "en",
		License: "Apache-2.0", Size: "~460MB", Tier: TierOptIn,
		Dest: "sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8",
		Source: Source{Kind: "tar", URL: ghReleases + "/" + asrTag +
			"/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8.tar.bz2"},
	},
	{
		ID: "punct-4class", Name: "4-class 日语标点 (opt-in)",
		Description: "4 分类日语标点恢复（可选）", Languages: "ja",
		License: "MIT", Size: "~40MB", Tier: TierOptIn,
		Dest: "punct-ja-4class-permissive",
		Source: Source{Kind: "hfrepo", Repo: "oboroge0/hayamimi-punct-ja-4class"},
	},
}

// ByID 按 ID 查找模型条目。
func ByID(id string) (Model, bool) {
	for _, m := range Catalog {
		if strings.EqualFold(m.ID, id) {
			return m, true
		}
	}
	return Model{}, false
}

// Human 把模型信息整理成可读的展示用字段。
func (m Model) Human() string {
	return fmt.Sprintf("%-16s %-10s %s", m.Name, m.Languages, m.Description)
}
