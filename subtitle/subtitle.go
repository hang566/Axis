// Package subtitle 负责把识别片段合并成合规字幕，并提供 SRT / VTT / JSON
// 的解析与序列化（时间轴编辑器在浏览器端也会用同一套 JSON 结构）。
package subtitle

import "time"

// Cue 是一条字幕（时间轴 + 多行文本）。
type Cue struct {
	Start time.Duration `json:"-"`
	End   time.Duration `json:"-"`
	Lines []string      `json:"lines"`
}

// Text 把多行拼成带换行的字符串。
func (c Cue) Text() string {
	out := ""
	for i, line := range c.Lines {
		if i > 0 {
			out += "\n"
		}
		out += line
	}
	return out
}

// Plain 返回用空格连接的单行文本。
func (c Cue) Plain() string {
	out := ""
	for i, line := range c.Lines {
		if i > 0 {
			out += " "
		}
		out += line
	}
	return out
}

// Options 控制合并与折行。
type Options struct {
	MaxLineLength    int
	MaxLines         int
	MaxCharsPerCue   int
	MaxDuration      time.Duration
	MinDuration      time.Duration
	MaxPause         time.Duration
	SentenceMerge    bool
	StripPunctuation bool
}

// DefaultOptions 返回常见字幕规范默认值。
func DefaultOptions() Options {
	return Options{
		MaxLineLength:  42,
		MaxLines:       2,
		MaxCharsPerCue: 84,
		MaxDuration:    6 * time.Second,
		MinDuration:    700 * time.Millisecond,
		MaxPause:       1500 * time.Millisecond,
		SentenceMerge:  true,
	}
}

func (o Options) normalize() Options {
	def := DefaultOptions()
	if o.MaxLineLength <= 0 {
		o.MaxLineLength = def.MaxLineLength
	}
	if o.MaxLines <= 0 {
		o.MaxLines = def.MaxLines
	}
	if o.MaxCharsPerCue <= 0 {
		o.MaxCharsPerCue = o.MaxLineLength * o.MaxLines
	}
	if o.MaxDuration <= 0 {
		o.MaxDuration = def.MaxDuration
	}
	if o.MinDuration <= 0 {
		o.MinDuration = def.MinDuration
	}
	if o.MaxPause <= 0 {
		o.MaxPause = def.MaxPause
	}
	if o.MaxDuration <= o.MinDuration {
		o.MaxDuration = o.MinDuration * 4
	}
	return o
}

// Segment 是字幕层的输入片段（与 adapters.Segment 对应）。
type Segment struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

// BuildCues 把识别片段合并为合规字幕。
func BuildCues(segments []Segment, opt Options) []Cue {
	opt = opt.normalize()
	segs := sanitize(segments)
	if len(segs) == 0 {
		return nil
	}

	groups := groupSegments(segs, opt)
	groups = mergeTooShort(groups, opt)

	cues := make([]Cue, 0, len(groups))
	for _, g := range groups {
		text := g.Text
		if opt.StripPunctuation {
			text = StripPunctuation(text)
		}
		lines := WrapText(text, opt.MaxLineLength, opt.MaxLines)
		if len(lines) == 0 {
			continue
		}
		cues = append(cues, Cue{Start: g.Start, End: g.End, Lines: lines})
	}
	return NormalizeTiming(cues)
}

func sanitize(segments []Segment) []Segment {
	out := make([]Segment, 0, len(segments))
	for _, s := range segments {
		text := TrimSpace(s.Text)
		if text == "" {
			continue
		}
		if s.Start < 0 {
			s.Start = 0
		}
		out = append(out, Segment{Start: s.Start, End: s.End, Text: text})
	}
	sortByStart(out)
	return out
}

// groupSegments 按标点/停顿/时长把片段合并成句子。
func groupSegments(segs []Segment, opt Options) []Segment {
	groups := make([]Segment, 0, len(segs))
	cur := segs[0]
	pending := false

	for _, next := range segs[1:] {
		if pending {
			cur = next
			pending = false
			continue
		}
		pause := next.Start - cur.End
		if opt.SentenceMerge {
			chars := RuneCount(cur.Text)
			if (EndsSentence(cur.Text) && (cur.End-cur.Start >= opt.MinDuration || chars >= opt.MaxCharsPerCue*2/3)) ||
				(chars >= clauseBreakMinChars && EndsClause(cur.Text)) {
				groups = append(groups, cur)
				cur = next
				continue
			}
		}
		tooLong := cur.End-cur.Start >= opt.MaxDuration || next.End-cur.Start > opt.MaxDuration
		tooMany := RuneCount(cur.Text) >= opt.MaxCharsPerCue
		tooFar := pause > opt.MaxPause

		if !tooLong && !tooMany && !tooFar {
			cur.Text = JoinText(cur.Text, next.Text)
			if next.End > cur.End {
				cur.End = next.End
			}
			if opt.SentenceMerge {
				chars := RuneCount(cur.Text)
				if (EndsSentence(cur.Text) && (cur.End-cur.Start >= opt.MinDuration || chars >= opt.MaxCharsPerCue*2/3)) ||
					(chars >= clauseBreakMinChars && EndsClause(cur.Text)) {
					groups = append(groups, cur)
					pending = true
				}
			}
			continue
		}
		groups = append(groups, cur)
		cur = next
	}
	if TrimSpace(cur.Text) != "" {
		groups = append(groups, cur)
	}
	return groups
}

// mergeTooShort 合并过短字幕（合并后不超过上限）。
func mergeTooShort(groups []Segment, opt Options) []Segment {
	if len(groups) < 2 || opt.MinDuration <= 0 {
		return groups
	}
	out := make([]Segment, 0, len(groups))
	for _, g := range groups {
		if n := len(out); n > 0 {
			prev := out[n-1]
			if prev.End-prev.Start < opt.MinDuration {
				mergedDuration := g.End - prev.Start
				mergedChars := RuneCount(prev.Text) + RuneCount(g.Text) + 1
				if mergedDuration <= opt.MaxDuration && mergedChars <= opt.MaxCharsPerCue {
					out[n-1] = Segment{Start: prev.Start, End: g.End, Text: JoinText(prev.Text, g.Text)}
					continue
				}
			}
		}
		out = append(out, g)
	}
	return out
}

// NormalizeTiming 修正零时长、负值与重叠。
func NormalizeTiming(cues []Cue) []Cue {
	const minGap = 40 * time.Millisecond
	out := make([]Cue, 0, len(cues))
	prevEnd := time.Duration(-1)
	for _, cue := range cues {
		if cue.Start < 0 {
			cue.Start = 0
		}
		if prevEnd >= 0 && cue.Start < prevEnd {
			cue.Start = prevEnd
		}
		if cue.End <= cue.Start {
			cue.End = cue.Start + minGap
		}
		out = append(out, cue)
		prevEnd = cue.End
	}
	return out
}
