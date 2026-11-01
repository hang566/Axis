package subtitle

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Track 是面向文件/界面的字幕文档（时间轴编辑器的读写格式）。
type Track struct {
	Cues     []TrackCue `json:"cues"`
	Language string     `json:"language,omitempty"`
	Provider string     `json:"provider,omitempty"`
	Model    string     `json:"model,omitempty"`
	Duration float64    `json:"duration,omitempty"`
}

// TrackCue 是 Track 中的一条字幕，时间以秒表示，便于 JSON 传输。
type TrackCue struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// TrackFromCues 把内部 Cue 列表转成可序列化文档。
func TrackFromCues(cues []Cue) []TrackCue {
	out := make([]TrackCue, 0, len(cues))
	for _, c := range cues {
		out = append(out, TrackCue{
			Start: round3(c.Start.Seconds()),
			End:   round3(c.End.Seconds()),
			Text:  strings.Join(c.Lines, "\n"),
		})
	}
	return out
}

func round3(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000
}

// WriteSRT 输出 SubRip。
func WriteSRT(w *bytes.Buffer, cues []TrackCue) {
	for i, c := range cues {
		fmt.Fprintf(w, "%d\r\n%s --> %s\r\n%s\r\n\r\n",
			i+1, FormatTimestamp(c.Start, ","), FormatTimestamp(c.End, ","), normalizeText(c.Text))
	}
}

// WriteVTT 输出 WebVTT。
func WriteVTT(w *bytes.Buffer, cues []TrackCue) {
	w.WriteString("WEBVTT\n\n")
	for i, c := range cues {
		fmt.Fprintf(w, "%d\n%s --> %s\n%s\n\n",
			i+1, FormatTimestamp(c.Start, "."), FormatTimestamp(c.End, "."), normalizeText(c.Text))
	}
}

// ToBytes 序列化为指定格式（srt / vtt / json）。
func ToBytes(cues []TrackCue, format string) ([]byte, string, error) {
	var buf bytes.Buffer
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "srt", "":
		WriteSRT(&buf, cues)
		return buf.Bytes(), "srt", nil
	case "vtt", "webvtt":
		WriteVTT(&buf, cues)
		return buf.Bytes(), "vtt", nil
	case "json":
		payload := Track{Cues: cues}
		raw, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return nil, "", err
		}
		return append(raw, '\n'), "json", nil
	default:
		return nil, "", fmt.Errorf("不支持的字幕格式 %q", format)
	}
}

// FormatTimestamp 输出 HH:MM:SS,mmm / HH:MM:SS.mmm。
func FormatTimestamp(seconds float64, sep string) string {
	if seconds < 0 || seconds != seconds {
		seconds = 0
	}
	totalMS := int64(seconds*1000 + 0.5)
	h := totalMS / 3600000
	totalMS %= 3600000
	m := totalMS / 60000
	totalMS %= 60000
	s := totalMS / 1000
	ms := totalMS % 1000
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", h, m, s, sep, ms)
}

func normalizeText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// Parse 解析 SRT / VTT / JSON，自动识别格式。
func Parse(data []byte) ([]TrackCue, error) {
	text := strings.TrimPrefix(string(data), "\ufeff")
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, fmt.Errorf("字幕内容为空")
	}
	if strings.HasPrefix(trimmed, "{") {
		if cues, err := parseJSON(trimmed); err == nil {
			return cues, nil
		}
	}
	if strings.HasPrefix(strings.ToUpper(trimmed), "WEBVTT") {
		return parseBlocks(text, true), nil
	}
	return parseBlocks(text, false), nil
}

func parseJSON(text string) ([]TrackCue, error) {
	var track Track
	if err := json.Unmarshal([]byte(text), &track); err == nil && len(track.Cues) > 0 {
		for i := range track.Cues {
			track.Cues[i].Text = normalizeText(track.Cues[i].Text)
		}
		return track.Cues, nil
	}
	var arr []TrackCue
	if err := json.Unmarshal([]byte(text), &arr); err == nil && len(arr) > 0 {
		return arr, nil
	}
	return nil, fmt.Errorf("不是可识别的字幕 JSON")
}

func parseBlocks(text string, vtt bool) []TrackCue {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	blocks := strings.Split(text, "\n\n")
	out := make([]TrackCue, 0, len(blocks))
	for _, block := range blocks {
		lines := strings.Split(block, "\n")
		timing := -1
		for i, line := range lines {
			if strings.Contains(line, "-->") {
				timing = i
				break
			}
		}
		if timing < 0 {
			continue
		}
		left, right, ok := strings.Cut(lines[timing], "-->")
		if !ok {
			continue
		}
		start, ok1 := parseTime(strings.TrimSpace(left))
		endRaw := strings.TrimSpace(right)
		if i := strings.IndexAny(endRaw, " \t"); i >= 0 {
			endRaw = endRaw[:i] // VTT 的 cue 设置
		}
		end, ok2 := parseTime(endRaw)
		if !ok1 || !ok2 {
			continue
		}
		body := strings.TrimSpace(strings.Join(lines[timing+1:], "\n"))
		if body == "" {
			continue
		}
		out = append(out, TrackCue{Start: round3(start), End: round3(end), Text: body})
	}
	return out
}

func parseTime(s string) (float64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	if s == "" {
		return 0, false
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, false
	}
	var secs float64
	for _, part := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return 0, false
		}
		secs = secs*60 + v
	}
	return secs, true
}

// ---- 字幕编辑操作（供编辑器批量处理） ----

// ShiftCues 整体平移时间轴（秒），并裁剪到 0 以上。
func ShiftCues(cues []TrackCue, delta float64) []TrackCue {
	out := make([]TrackCue, 0, len(cues))
	for _, c := range cues {
		start := c.Start + delta
		end := c.End + delta
		if start < 0 {
			start = 0
		}
		if end <= start {
			end = start + 0.3
		}
		c.Start, c.End = round3(start), round3(end)
		out = append(out, c)
	}
	return out
}

// ScaleCues 按比例缩放时间轴（以锚点为基准）。
func ScaleCues(cues []TrackCue, anchor, factor float64) []TrackCue {
	if factor <= 0 {
		return cues
	}
	out := make([]TrackCue, 0, len(cues))
	for _, c := range cues {
		start := anchor + (c.Start-anchor)*factor
		end := anchor + (c.End-anchor)*factor
		if start < 0 {
			start = 0
		}
		if end <= start {
			end = start + 0.3
		}
		c.Start, c.End = round3(start), round3(end)
		out = append(out, c)
	}
	return out
}

// ReplaceText 批量替换文本（可区分大小写）。
func ReplaceText(cues []TrackCue, from, to string, caseSensitive bool) ([]TrackCue, int) {
	if from == "" {
		return cues, 0
	}
	n := 0
	out := make([]TrackCue, 0, len(cues))
	for _, c := range cues {
		before := c.Text
		if caseSensitive {
			c.Text = strings.ReplaceAll(c.Text, from, to)
		} else {
			c.Text = replaceFold(c.Text, from, to)
		}
		if c.Text != before {
			n++
		}
		out = append(out, c)
	}
	return out, n
}

func replaceFold(s, from, to string) string {
	lowerS, lowerFrom := strings.ToLower(s), strings.ToLower(from)
	var b strings.Builder
	for {
		idx := strings.Index(lowerS, lowerFrom)
		if idx < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:idx])
		b.WriteString(to)
		s = s[idx+len(from):]
		lowerS = lowerS[idx+len(from):]
	}
	return b.String()
}

// MergeOverlaps 消除相邻字幕重叠（保留最小间隔）。
func MergeOverlaps(cues []TrackCue, gap float64) []TrackCue {
	if gap <= 0 {
		gap = 0.02
	}
	out := make([]TrackCue, 0, len(cues))
	var prevEnd float64 = -1
	for _, c := range cues {
		if c.Start < prevEnd {
			c.Start = prevEnd + gap
			if c.End <= c.Start {
				c.End = c.Start + 0.3
			}
		}
		out = append(out, c)
		prevEnd = c.End
	}
	return out
}

// Stats 汇总字幕统计信息。
func Stats(cues []TrackCue) (chars int, duration float64) {
	for _, c := range cues {
		chars += len([]rune(strings.ReplaceAll(c.Text, "\n", "")))
	}
	if len(cues) > 0 {
		duration = cues[len(cues)-1].End - cues[0].Start
	}
	return
}

// ReadSRTText 用于旧版兼容：把 []byte 解析成 Track。
func ReadSRTText(data []byte) (*Track, error) {
	cues, err := Parse(data)
	if err != nil {
		return nil, err
	}
	return &Track{Cues: cues}, nil
}

// ScanLines 是内部工具，保留便于测试。
func ScanLines(data []byte) []string {
	sc := bufio.NewScanner(bytes.NewReader(data))
	var out []string
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

var _ = time.Second
