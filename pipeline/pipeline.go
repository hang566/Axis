// Package pipeline 编排「音视频 → 16k 单声道 WAV → 分段 → 识别 → 字幕」。
//
// 长音频的分段点会尽量落在静音处（ffmpeg silencedetect），避免把一句话从中间
// 切开；每段的识别进度会折算到整条时间轴上，界面因此能看到平滑的进度。
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/local/stt-sub/adapters"
	"github.com/local/stt-sub/data"
	"github.com/local/stt-sub/media"
	"github.com/local/stt-sub/subtitle"
)

// Request 描述一次转写。
type Request struct {
	InputPath  string        // 输入音视频
	TempDir    string        // 临时目录（任务独享，便于清理）
	Format     string        // 字幕输出格式 srt / vtt / json
	SampleRate int           // 目标采样率
	Channels   int           // 目标声道
	ChunkSeconds time.Duration // 单段最长时长，0 表示不切
	Transcribe adapters.Options
	Sub      subtitle.Options
}

// Result 是一次转写的产出。
type Result struct {
	Duration   time.Duration
	Segments   []adapters.Segment
	Cues       []subtitle.TrackCue
	Format     string
	Bytes      []byte
	Language   string
	Model      string
	ChunkCount int
	Elapsed    time.Duration
	// 空语音时返回 true（不是错误）。
	Silent bool
}

// Progress 接收 (overall 0-1, phase 描述, step 信息)。
type Progress func(overall float64, phase string)

// Runner 执行转写流水线。
type Runner struct {
	media *media.Media
	cfg   Config
}

// Config 是流水线需要的配置。
type Config struct {
	FFmpegPath  string
	SampleRate  int
	Channels    int
	ChunkSeconds time.Duration
	Subtitle    subtitle.Options
}

// New 创建流水线。
func New(m *media.Media, cfg Config) *Runner {
	if cfg.SampleRate <= 0 {
		cfg.SampleRate = 16000
	}
	if cfg.Channels <= 0 {
		cfg.Channels = 1
	}
	return &Runner{media: m, cfg: cfg}
}

// chunkPlan 描述一段待识别音频。
type chunkPlan struct {
	path   string
	start  time.Duration
	dur    time.Duration
	index  int
	total  int
}

// Run 执行完整转写。cancel 时返回 context 错误。
func (r *Runner) Run(ctx context.Context, tr adapters.Transcriber, req Request, onProgress Progress) (*Result, error) {
	start := time.Now()
	if tr == nil {
		return nil, errors.New("没有可用的识别引擎")
	}
	if strings.TrimSpace(req.InputPath) == "" {
		return nil, errors.New("缺少输入文件")
	}
	if st, err := os.Stat(req.InputPath); err != nil || st.IsDir() {
		return nil, fmt.Errorf("输入文件不可访问：%s", req.InputPath)
	}
	tempDir := req.TempDir
	if tempDir == "" {
		dir, err := os.MkdirTemp("", "stt-")
		if err != nil {
			return nil, err
		}
		tempDir = dir
		defer os.RemoveAll(tempDir)
	}
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return nil, err
	}

	report := func(pct float64, phase string) {
		if onProgress != nil {
			onProgress(clamp01(pct), phase)
		}
	}

	// 1. 探测：先确认有音轨，给出明确提示而不是 ffmpeg 的原始报错。
	report(0.01, "检查媒体信息")
	probe, err := r.media.Probe(ctx, req.InputPath)
	if err != nil {
		return nil, err
	}
	if !probe.HasAudio() {
		return nil, errors.New("这个文件没有音轨（可能是纯静音视频或无声素材），无法识别语音")
	}
	duration := probe.Duration()
	if duration <= 0 {
		return nil, errors.New("无法读取媒体时长，文件可能已损坏")
	}

	// 2. 抽取整条音轨（16k 单声道 WAV）。
	report(0.03, "提取音频")
	wav := filepath.Join(tempDir, "audio.wav")
	if err := r.media.ExtractAudio(ctx, req.InputPath, wav, firstInt(req.SampleRate, r.cfg.SampleRate), firstInt(req.Channels, r.cfg.Channels),
		func(frac float64) { report(0.03+0.09*frac, "提取音频") }); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	audioDuration, err := WAVDuration(wav)
	if err != nil {
		audioDuration = duration
	}

	// 3. 规划分段（尽量切在静音处）。
	chunkSeconds := req.ChunkSeconds
	if chunkSeconds <= 0 {
		chunkSeconds = r.cfg.ChunkSeconds
	}
	plan, err := r.planChunks(ctx, wav, tempDir, audioDuration, chunkSeconds, report)
	if err != nil {
		return nil, err
	}

	// 4. 逐段识别，进度按段时长加权折算。
	report(0.14, "开始识别")
	segments := make([]adapters.Segment, 0, 256)
	var detectedLang, modelName string
	weight := 0.78
	for _, c := range plan {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		base := 0.14 + weight*float64(c.index)/float64(len(plan))
		span := weight / float64(len(plan))
		label := fmt.Sprintf("识别第 %d/%d 段", c.index+1, len(plan))
		if len(plan) == 1 {
			label = "识别中"
		}
		opts := req.Transcribe
		segs, info, err := tr.Transcribe(c.path, opts, func(frac float64) {
			report(base+span*clamp01(frac), label)
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("%s 失败：%w", label, err)
		}
		if info.DetectedLanguage != "" && detectedLang == "" {
			detectedLang = info.DetectedLanguage
		}
		if info.Model != "" {
			modelName = info.Model
		}
		for _, s := range segs {
			// 模型返回的是段内相对时间，这里统一叠加段偏移。
			segments = append(segments, adapters.Segment{
				Start: s.Start + c.start,
				End:   s.End + c.start,
				Text:  s.Text,
			})
		}
		report(base+span, label)
	}

	segments = finalizeSegments(segments, audioDuration)
	result := &Result{
		Duration:   audioDuration,
		Segments:   segments,
		Language:   detectedLang,
		Model:      modelName,
		ChunkCount: len(plan),
		Format:     normalizeFormat(req.Format),
	}
	if len(segments) == 0 {
		result.Silent = true
		result.Cues = nil
		res := &Result{}
		*res = *result
		res.Bytes, _, _ = subtitle.ToBytes(nil, result.Format)
		result = res
		result.Elapsed = time.Since(start)
		return result, nil
	}

	// 5. 生成字幕。
	report(0.94, "生成字幕")
	subOpts := req.Sub
	if subOpts == (subtitle.Options{}) {
		subOpts = r.cfg.Subtitle
	}
	cues := subtitle.BuildCues(toSubtitleSegments(segments), subOpts)
	result.Cues = subtitle.TrackFromCues(cues)
	body, format, err := subtitle.ToBytes(result.Cues, result.Format)
	if err != nil {
		return nil, err
	}
	result.Bytes = body
	result.Format = format
	result.Elapsed = time.Since(start)
	report(1, "完成")
	return result, nil
}

// planChunks 决定如何切分音频；优先在静音处下刀。
func (r *Runner) planChunks(ctx context.Context, wav, tempDir string, total, chunkSeconds time.Duration, report func(float64, string)) ([]chunkPlan, error) {
	whole := []chunkPlan{{path: wav, start: 0, dur: total, index: 0, total: 1}}
	if chunkSeconds <= 0 || total <= chunkSeconds*3/2 {
		return whole, nil
	}

	report(0.13, "寻找合适的分段点")
	silences := r.media.SilenceRanges(ctx, wav, -35, 400*time.Millisecond)

	var cuts []time.Duration
	// 用一次扫描找出靠近每个目标切点的静音中心。
	for target := chunkSeconds; target < total-chunkSeconds/3; target += chunkSeconds {
		best := time.Duration(-1)
		bestDist := time.Duration(1<<62 - 1)
		for _, rng := range silences {
			mid := time.Duration((rng[0]+rng[1])/2*float64(time.Second))
			// 静音区间必须完整落在 [target-60s, target+60s] 内。
			if time.Duration(rng[0]*float64(time.Second)) < target-60*time.Second ||
				time.Duration(rng[1]*float64(time.Second)) > target+60*time.Second {
				continue
			}
			dist := mid - target
			if dist < 0 {
				dist = -dist
			}
			if dist < bestDist {
				bestDist = dist
				best = mid
			}
		}
		if best > 0 {
			cuts = append(cuts, best)
		} else {
			cuts = append(cuts, target)
		}
	}

	// 去掉过近的切点。
	plan := make([]chunkPlan, 0, len(cuts)+1)
	prev := time.Duration(0)
	for _, cut := range cuts {
		if cut-prev < 30*time.Second {
			continue
		}
		plan = append(plan, chunkPlan{start: prev, dur: cut - prev})
		prev = cut
	}
	if total-prev > 0 {
		plan = append(plan, chunkPlan{start: prev, dur: total - prev})
	}
	if len(plan) <= 1 {
		return whole, nil
	}

	// 真正切出文件；每段都重新编码成标准 PCM WAV，保证模型输入一致。
	for i := range plan {
		path := filepath.Join(tempDir, fmt.Sprintf("chunk_%03d.wav", i+1))
		if err := r.media.ExtractRange(ctx, wav, path, plan[i].start, plan[i].dur, 16000); err != nil {
			return nil, err
		}
		plan[i].path = path
		plan[i].index = i
		plan[i].total = len(plan)
	}
	report(0.14, fmt.Sprintf("已切分为 %d 段", len(plan)))
	return plan, nil
}

func finalizeSegments(segments []adapters.Segment, total time.Duration) []adapters.Segment {
	out := make([]adapters.Segment, 0, len(segments))
	for _, s := range segments {
		text := strings.TrimSpace(s.Text)
		if text == "" {
			continue
		}
		if s.Start < 0 {
			s.Start = 0
		}
		out = append(out, adapters.Segment{Start: s.Start, End: s.End, Text: text})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })

	// 补全缺失/非法的结束时间。
	for i := range out {
		if out[i].End > out[i].Start {
			continue
		}
		switch {
		case i+1 < len(out) && out[i+1].Start > out[i].Start:
			out[i].End = out[i+1].Start
		case total > out[i].Start:
			out[i].End = total
		default:
			out[i].End = out[i].Start + 500*time.Millisecond
		}
	}
	// 裁剪超出音频末尾的时间。
	for i := range out {
		if total > 0 && out[i].End > total {
			out[i].End = total
			if out[i].End <= out[i].Start {
				out[i].End = out[i].Start + 200*time.Millisecond
			}
		}
	}
	return out
}

func toSubtitleSegments(segments []adapters.Segment) []subtitle.Segment {
	out := make([]subtitle.Segment, 0, len(segments))
	for _, s := range segments {
		out = append(out, subtitle.Segment{Start: s.Start, End: s.End, Text: s.Text})
	}
	return out
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func firstInt(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

func normalizeFormat(f string) string {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "vtt", "webvtt":
		return "vtt"
	case "json":
		return "json"
	default:
		return "srt"
	}
}

var _ = data.NewID
