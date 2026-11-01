package subtitle

import (
	"strings"
	"testing"
	"time"
)

func TestParseAndSerializeSRT(t *testing.T) {
	src := "1\r\n00:00:01,000 --> 00:00:02,500\r\n第一句字幕\r\n\r\n" +
		"2\r\n00:00:03,000 --> 00:00:04,000\r\n第二句\r\n换行内容\r\n\r\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("期望 2 条字幕，实际 %d", len(cues))
	}
	if cues[0].Start != 1 || cues[0].End != 2.5 {
		t.Fatalf("时间解析错误：%+v", cues[0])
	}
	if !strings.Contains(cues[1].Text, "\n") {
		t.Fatalf("多行文本丢失：%q", cues[1].Text)
	}

	body, ext, err := ToBytes(cues, "srt")
	if err != nil || ext != "srt" {
		t.Fatalf("序列化失败：%v %s", err, ext)
	}
	if !strings.Contains(string(body), "00:00:01,000 --> 00:00:02,500") {
		t.Fatalf("时间戳格式错误：%s", body)
	}
}

func TestParseWebVTT(t *testing.T) {
	src := "WEBVTT\n\nNOTE this is a note\n\n1\n00:01.000 --> 00:02.000 align:start\nHello world\n"
	cues, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(cues) != 1 || cues[0].Text != "Hello world" {
		t.Fatalf("VTT 解析错误：%+v", cues)
	}
	if cues[0].Start != 1 {
		t.Fatalf("VTT 时间错误：%v", cues[0].Start)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	cues := []TrackCue{{Start: 0, End: 1.2, Text: "你好"}, {Start: 1.2, End: 2.4, Text: "世界"}}
	body, ext, err := ToBytes(cues, "json")
	if err != nil || ext != "json" {
		t.Fatalf("序列化失败：%v", err)
	}
	back, err := Parse(body)
	if err != nil || len(back) != 2 {
		t.Fatalf("回读失败：%v %+v", err, back)
	}
}

func TestBuildCuesMergesFragments(t *testing.T) {
	segs := []Segment{
		{Start: 0, End: 600 * time.Millisecond, Text: "今天"},
		{Start: 600 * time.Millisecond, End: 1200 * time.Millisecond, Text: "我们"},
		{Start: 1200 * time.Millisecond, End: 2 * time.Second, Text: "聊聊字幕。"},
		{Start: 3 * time.Second, End: 5 * time.Second, Text: "第二句话比较长一些，需要折行处理看看效果。"},
	}
	opt := Options{MaxLineLength: 12, MaxLines: 2, MaxCharsPerCue: 24, MaxDuration: 4 * time.Second, MinDuration: 700 * time.Millisecond, MaxPause: 1200 * time.Millisecond, SentenceMerge: true}
	cues := BuildCues(segs, opt)
	if len(cues) < 2 {
		t.Fatalf("期望至少 2 条字幕，实际 %d：%+v", len(cues), cues)
	}
	if cues[0].End <= cues[0].Start {
		t.Fatalf("时间轴异常：%+v", cues[0])
	}
	for _, c := range cues {
		if len(c.Lines) > 2 {
			t.Fatalf("超过最大行数：%+v", c)
		}
	}
}

func TestShiftAndReplace(t *testing.T) {
	cues := []TrackCue{{Start: 1, End: 2, Text: "Hello 世界"}}
	shifted := ShiftCues(cues, -5)
	if shifted[0].Start != 0 {
		t.Fatalf("负时间未裁剪：%+v", shifted[0])
	}
	replaced, n := ReplaceText(cues, "hello", "hi", false)
	if n != 1 || !strings.Contains(replaced[0].Text, "hi") {
		t.Fatalf("替换失败：%+v n=%d", replaced[0], n)
	}
}
