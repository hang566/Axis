package media

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func osStat(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("%s 是目录", path)
	}
	if st.Size() == 0 {
		return fmt.Errorf("%s 是空文件", path)
	}
	return nil
}

// TranscodeRequest 描述一次转码/剪辑请求。
type TranscodeRequest struct {
	Input  string
	Output string

	VideoCodec string // libx264 / libx265 / copy / 空=libx264
	AudioCodec string // aac / copy / none / 空=aac
	Preset     string // 编码预设
	CRF        int    // 质量（0 = 不指定）
	Bitrate    string // 目标码率，如 "2M"
	AudioBitrate string // 音频码率，如 "128k"

	Resolution string  // "1280x720" 或 "720"（按高度等比）
	ScaleW     int     // 直接指定宽度
	ScaleH     int     // 直接指定高度
	FrameRate  float64 // 帧率

	SampleRate int // 音频采样率
	Channels   int // 声道数

	Start time.Duration // 剪辑起点
	End   time.Duration // 剪辑终点

	Format    string   // 容器格式
	ExtraArgs []string // 额外参数
	Total     time.Duration // 用于进度计算（由调用方探测后填入）
}

// BuildArgs 生成 ffmpeg 参数（也用于「命令预览」）。
func BuildArgs(req TranscodeRequest, forPreview bool) []string {
	var args []string
	if forPreview {
		args = []string{"-hide_banner"}
	} else {
		args = []string{"-nostdin", "-y", "-hide_banner", "-loglevel", "error"}
	}
	if req.Start > 0 {
		args = append(args, "-ss", trimSeconds(req.Start.Seconds()))
	}
	if req.End > 0 && req.End > req.Start {
		args = append(args, "-to", trimSeconds(req.End.Seconds()))
	}
	args = append(args, "-i", req.Input)

	// 视频
	switch {
	case req.VideoCodec == "copy":
		args = append(args, "-c:v", "copy")
	case req.VideoCodec == "none":
		args = append(args, "-vn")
	default:
		vcodec := req.VideoCodec
		if vcodec == "" {
			vcodec = "libx264"
		}
		args = append(args, "-c:v", vcodec)
		if req.Preset != "" {
			args = append(args, "-preset", req.Preset)
		}
		if req.Bitrate != "" && (strings.HasPrefix(vcodec, "libx264") || strings.HasPrefix(vcodec, "libx265")) {
			args = append(args, "-b:v", req.Bitrate)
		} else if req.CRF > 0 {
			args = append(args, "-crf", strconv.Itoa(req.CRF))
		}
		// 兼容性：H.264 用 yuv420p，保证浏览器/电视都能播。
		if strings.HasPrefix(vcodec, "libx264") || strings.HasPrefix(vcodec, "libx265") {
			args = append(args, "-pix_fmt", "yuv420p")
			if strings.HasPrefix(vcodec, "libx265") {
				args = append(args, "-tag:v", "hvc1")
			}
		}
	}

	// 音频
	switch {
	case req.AudioCodec == "copy":
		args = append(args, "-c:a", "copy")
	case req.AudioCodec == "none":
		args = append(args, "-an")
	default:
		acodec := req.AudioCodec
		if acodec == "" {
			acodec = "aac"
		}
		args = append(args, "-c:a", acodec)
		if req.AudioBitrate != "" {
			args = append(args, "-b:a", req.AudioBitrate)
		}
	}

	// 滤镜
	var vf []string
	if req.ScaleW > 0 || req.ScaleH > 0 {
		w, h := req.ScaleW, req.ScaleH
		if w <= 0 {
			w = -2
		}
		if h <= 0 {
			h = -2
		}
		vf = append(vf, fmt.Sprintf("scale=%d:%d", w, h))
	} else if r := normalizeResolution(req.Resolution); r != "" {
		vf = append(vf, "scale="+r)
	}
	if req.FrameRate > 0 {
		vf = append(vf, "fps="+strconv.FormatFloat(req.FrameRate, 'f', -1, 64))
	}
	if len(vf) > 0 && req.VideoCodec != "copy" && req.VideoCodec != "none" {
		args = append(args, "-vf", strings.Join(vf, ","))
	}
	if req.SampleRate > 0 {
		args = append(args, "-ar", strconv.Itoa(req.SampleRate))
	}
	if req.Channels > 0 {
		args = append(args, "-ac", strconv.Itoa(req.Channels))
	}
	if req.Format != "" {
		args = append(args, "-f", req.Format)
	}
	args = append(args, req.ExtraArgs...)
	args = append(args, req.Output)
	return args
}

// Transcode 执行转码；onProgress 收到 0-1 的节奏进度。
func (m *Media) Transcode(ctx context.Context, req TranscodeRequest, onProgress func(frac float64)) error {
	if strings.TrimSpace(req.Input) == "" || strings.TrimSpace(req.Output) == "" {
		return fmt.Errorf("media: 转码需要指定输入与输出")
	}
	if req.Total <= 0 {
		if d, err := m.Duration(ctx, req.Input); err == nil {
			req.Total = d
		}
	}
	total := req.Total
	if req.Start > 0 || req.End > 0 {
		// 剪辑区间长度才是进度分母。
		start := req.Start
		end := req.End
		if end <= 0 {
			end = total
		}
		if end > start {
			total = end - start
		}
	}
	var cb func(float64, time.Duration, string)
	if onProgress != nil {
		cb = func(f float64, _ time.Duration, _ string) { onProgress(f) }
	}
	return m.run(ctx, total, cb, BuildArgs(req, false))
}

// PreviewCommand 返回可直接显示给用户的 ffmpeg 命令（不含 -progress 前缀）。
func PreviewCommand(req TranscodeRequest) string {
	args := BuildArgs(req, true)
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		if strings.ContainsAny(a, " \t") {
			quoted = append(quoted, `"`+a+`"`)
		} else {
			quoted = append(quoted, a)
		}
	}
	return "ffmpeg " + strings.Join(quoted, " ")
}

func trimSeconds(v float64) string {
	return strconv.FormatFloat(v, 'f', 3, 64)
}

func normalizeResolution(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.ContainsAny(s, "xX*:") {
		parts := strings.FieldsFunc(s, func(r rune) bool { return r == 'x' || r == 'X' || r == '*' || r == ':' })
		if len(parts) == 2 {
			return parts[0] + ":" + parts[1]
		}
		return ""
	}
	h := strings.TrimRight(strings.TrimSpace(strings.ToLower(s)), "p")
	if _, err := strconv.Atoi(h); err == nil {
		return "-2:" + h
	}
	return ""
}
