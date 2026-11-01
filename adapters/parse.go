package adapters

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// ---- 通用解析：把各种 STT 输出转成 []Segment ----

// TimeUnit 描述时间戳单位。
type TimeUnit int

const (
	UnitSecond TimeUnit = iota
	UnitMillisecond
)

// ParseTimeUnit 解析配置里的 time_unit。
func ParseTimeUnit(s string) TimeUnit {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ms", "milli", "millis", "millisecond", "milliseconds":
		return UnitMillisecond
	default:
		return UnitSecond
	}
}

// DecodeText 把可能是 GBK/GB18030 的输出转成 UTF-8。
// Windows 上 Python/命令行工具重定向输出常按本地代码页编码，直接当 UTF-8 会乱码。
func DecodeText(raw []byte) string {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(raw) {
		return string(raw)
	}
	decoded, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), raw)
	if err == nil && utf8.Valid(decoded) {
		return string(decoded)
	}
	return strings.ToValidUTF8(string(raw), "")
}

func durationFrom(v float64, unit TimeUnit) time.Duration {
	if unit == UnitMillisecond {
		return time.Duration(v * float64(time.Millisecond))
	}
	return time.Duration(v * float64(time.Second))
}

// ParseTranscription 宽松解析 JSON 结果：
//
//	{"segments":[{start,end,text}]} / {"data":[...]} / 顶层数组 / {"text":"..."}
func ParseTranscription(data []byte, unit TimeUnit) ([]Segment, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("识别结果为空")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &probe); err == nil {
		for _, key := range []string{"segments", "data", "results", "result", "utterances", "transcripts", "chunks"} {
			raw, ok := probe[key]
			if !ok {
				continue
			}
			var items []json.RawMessage
			if err := json.Unmarshal(raw, &items); err != nil {
				continue
			}
			if segs, err := parseSegmentList(items, unit); err == nil && len(segs) > 0 {
				return segs, nil
			}
		}
		for _, key := range []string{"text", "transcript", "transcription", "content"} {
			raw, ok := probe[key]
			if !ok {
				continue
			}
			var s string
			if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
				return []Segment{{Text: strings.TrimSpace(s)}}, nil
			}
		}
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err == nil {
		if segs, err := parseSegmentList(items, unit); err == nil && len(segs) > 0 {
			return segs, nil
		}
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil && strings.TrimSpace(text) != "" {
		return []Segment{{Text: strings.TrimSpace(text)}}, nil
	}
	return nil, fmt.Errorf("无法解析识别结果：%s", Clip(string(trimmed), 200))
}

func parseSegmentList(items []json.RawMessage, unit TimeUnit) ([]Segment, error) {
	out := make([]Segment, 0, len(items))
	for _, raw := range items {
		if seg, ok := parseSegmentItem(raw, unit); ok {
			out = append(out, seg)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("列表中没有有效片段")
	}
	return out, nil
}

func parseSegmentItem(raw json.RawMessage, unit TimeUnit) (Segment, bool) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return Segment{}, false
		}
		return Segment{Text: text}, true
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		text = strings.TrimSpace(pickString(obj, "text", "content", "sentence", "transcript", "word", "txt"))
		if text == "" {
			return Segment{}, false
		}
		var seg Segment
		if v, ok := pickFloat(obj, "start", "begin", "from", "start_time", "startTime", "st"); ok {
			seg.Start = durationFrom(v, unit)
		}
		if v, ok := pickFloat(obj, "end", "to", "stop", "end_time", "endTime", "et"); ok {
			seg.End = durationFrom(v, unit)
		} else if v, ok := pickFloat(obj, "duration", "dur"); ok {
			seg.End = seg.Start + durationFrom(v, unit)
		}
		seg.Text = text
		return seg, true
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) >= 2 {
		var seg Segment
		if v, ok := decodeNumber(arr[0]); ok {
			seg.Start = durationFrom(v, unit)
		}
		if v, ok := decodeNumber(arr[1]); ok {
			seg.End = durationFrom(v, unit)
		}
		if len(arr) >= 3 {
			text = strings.TrimSpace(pickRawString(arr[len(arr)-1]))
		}
		if text == "" {
			return Segment{}, false
		}
		seg.Text = text
		return seg, true
	}
	return Segment{}, false
}

func pickRawString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	if v, ok := decodeNumber(raw); ok {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func pickFloat(obj map[string]json.RawMessage, keys ...string) (float64, bool) {
	for _, key := range keys {
		if raw, ok := obj[key]; ok {
			if v, ok := decodeNumber(raw); ok {
				return v, true
			}
		}
	}
	return 0, false
}

func pickString(obj map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		if raw, ok := obj[key]; ok {
			if s := pickRawString(raw); s != "" {
				return s
			}
		}
	}
	return ""
}

func decodeNumber(raw json.RawMessage) (float64, bool) {
	var num float64
	if err := json.Unmarshal(raw, &num); err == nil {
		return num, true
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		str = strings.TrimSpace(str)
		if str == "" {
			return 0, false
		}
		if v, err := strconv.ParseFloat(str, 64); err == nil {
			return v, true
		}
		if d, err := parseClockTime(str); err == nil {
			return d.Seconds(), true
		}
	}
	return 0, false
}

func parseClockTime(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	if s == "" {
		return 0, fmt.Errorf("空时间")
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("无法解析时间 %q", s)
	}
	var secs float64
	for _, part := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return 0, fmt.Errorf("无法解析时间 %q", s)
		}
		secs = secs*60 + v
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// ParseSRT 解析 SRT 文本。
func ParseSRT(data []byte) ([]Segment, error) {
	text := DecodeText(data)
	segments := make([]Segment, 0, 64)
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var start, end time.Duration
	var sawTiming bool
	var lines []string
	expectIndex := true
	flush := func() {
		body := strings.TrimSpace(strings.Join(lines, "\n"))
		lines = nil
		if !sawTiming || body == "" {
			return
		}
		segments = append(segments, Segment{Start: start, End: end, Text: body})
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			flush()
			expectIndex = true
			continue
		}
		if s, e, ok := parseTimeRange(line); ok {
			flush()
			sawTiming = true
			start, end = s, e
			expectIndex = false
			continue
		}
		if expectIndex {
			if _, err := strconv.Atoi(line); err == nil {
				continue
			}
			expectIndex = false
		}
		lines = append(lines, line)
	}
	flush()
	if len(segments) == 0 {
		return nil, fmt.Errorf("没有解析到 SRT 字幕块")
	}
	return segments, nil
}

func parseTimeRange(line string) (time.Duration, time.Duration, bool) {
	idx := strings.Index(line, "-->")
	if idx < 0 {
		return 0, 0, false
	}
	left := strings.TrimSpace(line[:idx])
	right := strings.TrimSpace(line[idx+3:])
	if i := strings.IndexAny(right, " \t"); i >= 0 {
		right = strings.TrimSpace(right[:i])
	}
	s, err := parseClockTime(left)
	if err != nil {
		return 0, 0, false
	}
	e, err := parseClockTime(right)
	if err != nil {
		return 0, 0, false
	}
	return s, e, true
}

// ParseAny 依次尝试 JSON → SRT → 纯文本。
func ParseAny(data []byte, unit TimeUnit) ([]Segment, error) {
	if segs, err := ParseTranscription(data, unit); err == nil {
		return segs, nil
	}
	if segs, err := ParseSRT(data); err == nil {
		return segs, nil
	}
	text := strings.TrimSpace(DecodeText(data))
	if text == "" {
		return nil, fmt.Errorf("识别结果为空")
	}
	return []Segment{{Text: text}}, nil
}

var _ = io.EOF
