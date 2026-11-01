package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Format 描述容器信息。
type Format struct {
	FileName   string            `json:"filename"`
	FormatName string            `json:"formatName"`
	LongName   string            `json:"longName"`
	Duration   float64           `json:"duration"`
	Size       int64             `json:"size"`
	BitRate    int64             `json:"bitRate"`
	ProbeScore int               `json:"probeScore"`
	Tags       map[string]string `json:"tags,omitempty"`
}

// Stream 描述一条流（视频/音频/字幕）。
type Stream struct {
	Index         int               `json:"index"`
	Type          string            `json:"type"`
	Codec         string            `json:"codec"`
	CodecLongName string            `json:"codecLongName"`
	Profile       string            `json:"profile,omitempty"`
	Width         int               `json:"width,omitempty"`
	Height        int               `json:"height,omitempty"`
	FrameRate     float64           `json:"frameRate,omitempty"`
	PixelFormat   string            `json:"pixelFormat,omitempty"`
	BitRate       int64             `json:"bitRate,omitempty"`
	SampleRate    int               `json:"sampleRate,omitempty"`
	Channels      int               `json:"channels,omitempty"`
	ChannelLayout string            `json:"channelLayout,omitempty"`
	Duration      float64           `json:"duration,omitempty"`
	Language      string            `json:"language,omitempty"`
	Title         string            `json:"title,omitempty"`
	SampleFormat  string            `json:"sampleFormat,omitempty"`
	Tags          map[string]string `json:"tags,omitempty"`
}

// ProbeResult 是一次完整探测的结果。
type ProbeResult struct {
	Format  Format   `json:"format"`
	Streams []Stream `json:"streams"`
	// JSON 保留 ffprobe 的原始输出，供「高级信息」查看与复制。
	JSON string `json:"-"`
}

// Duration 返回总时长。
func (p *ProbeResult) Duration() time.Duration {
	if p == nil {
		return 0
	}
	if p.Format.Duration > 0 {
		return time.Duration(p.Format.Duration * float64(time.Second))
	}
	for _, s := range p.Streams {
		if s.Duration > 0 {
			return time.Duration(s.Duration * float64(time.Second))
		}
	}
	return 0
}

// HasAudio 判断是否包含音轨。
func (p *ProbeResult) HasAudio() bool {
	for _, s := range p.Streams {
		if s.Type == "audio" {
			return true
		}
	}
	return false
}

// HasVideo 判断是否包含视频轨。
func (p *ProbeResult) HasVideo() bool {
	for _, s := range p.Streams {
		if s.Type == "video" {
			return true
		}
	}
	return false
}

// FirstStream 返回指定类型的第一条流。
func (p *ProbeResult) FirstStream(kind string) (Stream, bool) {
	for _, s := range p.Streams {
		if s.Type == kind {
			return s, true
		}
	}
	return Stream{}, false
}

// titleFromTags 取容器标题（没有则返回空）。
func (p *ProbeResult) titleFromTags() string {
	for _, key := range []string{"title", "TITLE"} {
		if v := strings.TrimSpace(p.Format.Tags[key]); v != "" {
			return v
		}
	}
	return ""
}

// Title 对外暴露容器标题（没有则返回空）。
func (p *ProbeResult) Title() string {
	if p == nil {
		return ""
	}
	return p.titleFromTags()
}

// ffprobeRaw 对应 ffprobe -print_format json -show_format -show_streams。
type ffprobeRaw struct {
	Streams []struct {
		Index         int               `json:"index"`
		CodecType     string            `json:"codec_type"`
		CodecName     string            `json:"codec_name"`
		CodecLongName string            `json:"codec_long_name"`
		Profile       string            `json:"profile"`
		Width         int               `json:"width"`
		Height        int               `json:"height"`
		RFrameRate    string            `json:"r_frame_rate"`
		AvgFrameRate  string            `json:"avg_frame_rate"`
		PixFmt        string            `json:"pix_fmt"`
		BitRate       flexInt           `json:"bit_rate"`
		SampleRate    flexInt           `json:"sample_rate"`
		Channels      int               `json:"channels"`
		ChannelLayout string            `json:"channel_layout"`
		Duration      flexFloat         `json:"duration"`
		SampleFmt     string            `json:"sample_fmt"`
		Tags          map[string]string `json:"tags"`
	} `json:"streams"`
	Format struct {
		FileName   string            `json:"filename"`
		FormatName string            `json:"format_name"`
		LongName   string            `json:"format_long_name"`
		Duration   flexFloat         `json:"duration"`
		Size       flexInt           `json:"size"`
		BitRate    flexInt           `json:"bit_rate"`
		ProbeScore int               `json:"probe_score"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
}

type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "N/A" || s == "null" {
		*f = 0
		return nil
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = flexInt(v)
		return nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		*f = flexInt(v)
		return nil
	}
	*f = 0
	return nil
}

type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "N/A" || s == "null" {
		*f = 0
		return nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		*f = flexFloat(v)
		return nil
	}
	*f = 0
	return nil
}

func parseRatio(s string) float64 {
	if s == "" || s == "0/0" {
		return 0
	}
	if strings.Contains(s, "/") {
		parts := strings.SplitN(s, "/", 2)
		a, err1 := strconv.ParseFloat(parts[0], 64)
		b, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 == nil && err2 == nil && b != 0 {
			return a / b
		}
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// Probe 调用 ffprobe 获取完整媒体信息。
func (m *Media) Probe(ctx context.Context, input string) (*ProbeResult, error) {
	ffprobe := m.FFprobe()
	if ffprobe == "" {
		// 没有 ffprobe 时退化为 ffmpeg 解析时长。
		dur, err := m.Duration(ctx, input)
		if err != nil {
			return nil, err
		}
		return &ProbeResult{
			Format: Format{Duration: dur.Seconds()},
		}, nil
	}
	if err := osStat(input); err != nil {
		return nil, fmt.Errorf("media: 无法读取文件：%w", err)
	}
	args := []string{
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		input,
	}
	cmd := exec.CommandContext(ctx, ffprobe, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("media: 解析媒体信息失败（文件可能已损坏或格式不受支持）：%w", err)
	}
	return parseProbeJSON(out)
}

func parseProbeJSON(raw []byte) (*ProbeResult, error) {
	var parsed ffprobeRaw
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("media: 解析 ffprobe 输出失败：%w", err)
	}
	res := &ProbeResult{
		Format: Format{
			FileName:   parsed.Format.FileName,
			FormatName: parsed.Format.FormatName,
			LongName:   parsed.Format.LongName,
			Duration:   float64(parsed.Format.Duration),
			Size:       int64(parsed.Format.Size),
			BitRate:    int64(parsed.Format.BitRate),
			ProbeScore: parsed.Format.ProbeScore,
			Tags:       parsed.Format.Tags,
		},
		JSON: string(raw),
	}
	for _, s := range parsed.Streams {
		st := Stream{
			Index:         s.Index,
			Type:          s.CodecType,
			Codec:         s.CodecName,
			CodecLongName: s.CodecLongName,
			Profile:       s.Profile,
			Width:         s.Width,
			Height:        s.Height,
			FrameRate:     parseRatio(s.RFrameRate),
			PixelFormat:   s.PixFmt,
			BitRate:       int64(s.BitRate),
			SampleRate:    int(s.SampleRate),
			Channels:      s.Channels,
			ChannelLayout: s.ChannelLayout,
			Duration:      float64(s.Duration),
			SampleFormat:  s.SampleFmt,
			Tags:          s.Tags,
		}
		if st.FrameRate == 0 {
			st.FrameRate = parseRatio(s.AvgFrameRate)
		}
		if s.Tags != nil {
			st.Language = s.Tags["language"]
			st.Title = s.Tags["title"]
		}
		res.Streams = append(res.Streams, st)
	}
	// 容器没有时长时用最长的流补上。
	if res.Format.Duration <= 0 {
		for _, s := range res.Streams {
			if s.Duration > res.Format.Duration {
				res.Format.Duration = s.Duration
			}
		}
	}
	return res, nil
}

// Duration 只取媒体总时长（轻量，用于进度分母）。
func (m *Media) Duration(ctx context.Context, input string) (time.Duration, error) {
	ffprobe := m.FFprobe()
	if ffprobe != "" {
		args := []string{
			"-v", "error",
			"-show_entries", "format=duration",
			"-of", "default=nw=1:nk=1",
			input,
		}
		out, err := exec.CommandContext(ctx, ffprobe, args...).Output()
		if err == nil {
			if v, perr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); perr == nil && v > 0 {
				return time.Duration(v * float64(time.Second)), nil
			}
		}
	}
	// 退化：ffmpeg -i 的 Duration 行。
	if err := m.Check(); err != nil {
		return 0, err
	}
	cmd := exec.CommandContext(ctx, m.FFmpeg(), "-hide_banner", "-i", input)
	out, _ := cmd.CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, "Duration:"); i >= 0 {
			field := strings.Fields(line[i+len("Duration:"):])
			if len(field) == 0 {
				continue
			}
			tv := strings.TrimSuffix(field[0], ",")
			if d, err := time.ParseDuration(tv + "s"); err == nil {
				if strings.Count(tv, ":") == 2 {
					parts := strings.Split(tv, ":")
					d = parseClock(parts)
				}
				return d, nil
			}
		}
	}
	return 0, fmt.Errorf("media: 无法获取媒体时长")
}

func parseClock(parts []string) time.Duration {
	var secs float64
	for _, p := range parts {
		v, _ := strconv.ParseFloat(p, 64)
		secs = secs*60 + v
	}
	return time.Duration(secs * float64(time.Second))
}
