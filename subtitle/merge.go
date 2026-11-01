package subtitle

import (
	"sort"
	"strings"
	"unicode"
)

// clauseBreakMinChars 是「在逗号/顿号处断句」所需的最少字符数。
const clauseBreakMinChars = 12

// WrapText 把文本折成不超过 maxLen 的行，最多 maxLines 行。
// 拉丁语按单词边界断行，CJK 无空格文本按字符数硬断行。
func WrapText(text string, maxLen, maxLines int) []string {
	if maxLen <= 0 {
		maxLen = 42
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return nil
	}

	var lines []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			lines = append(lines, string(cur))
			cur = nil
		}
	}
	for _, word := range strings.Split(text, " ") {
		w := []rune(word)
		if len(cur) > 0 && len(cur)+1+len(w) > maxLen {
			flush()
		}
		if len(cur) == 0 {
			for len(w) > maxLen {
				lines = append(lines, string(w[:maxLen]))
				w = w[maxLen:]
			}
			cur = w
			continue
		}
		cur = append(cur, ' ')
		cur = append(cur, w...)
	}
	flush()

	if maxLines > 0 && len(lines) > maxLines {
		rest := strings.Join(lines[maxLines-1:], " ")
		lines = append(lines[:maxLines-1], rest)
	}
	return lines
}

// JoinText 合并两段文本，按语种决定是否补空格。
func JoinText(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if needSpace(lastRune(a), firstRune(b)) {
		return a + " " + b
	}
	return a + b
}

func needSpace(left, right rune) bool {
	if unicode.IsSpace(left) || unicode.IsSpace(right) {
		return true
	}
	latin := func(r rune) bool { return unicode.Is(unicode.Latin, r) || unicode.IsDigit(r) }
	cjk := IsCJK
	switch {
	case latin(left) && latin(right):
		return true
	case latin(left) && cjk(right), cjk(left) && latin(right):
		return true
	default:
		return false
	}
}

// IsCJK 判断是否为中日韩字符。
func IsCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// EndsSentence 判断是否以句末标点结尾。
func EndsSentence(s string) bool {
	s = strings.TrimRight(s, " \t\"'」』”’）)》]】…")
	if s == "" {
		return false
	}
	switch lastRune(s) {
	case '。', '！', '？', '!', '?', '；', ';', '…', '．':
		return true
	case '.':
		rs := []rune(s)
		if len(rs) < 3 {
			return false
		}
		return !unicode.IsDigit(rs[len(rs)-2])
	default:
		return false
	}
}

// EndsClause 判断是否以逗号等次级标点结尾。
func EndsClause(s string) bool {
	s = strings.TrimRight(s, " \t\"'」』”’）)》]】")
	if s == "" {
		return false
	}
	switch lastRune(s) {
	case '，', ',', '、', '：', ':', '；', ';':
		return true
	default:
		return false
	}
}

var punctuationSet = map[rune]bool{
	'，': true, '。': true, '！': true, '？': true, '、': true, '；': true, '：': true,
	',': true, '.': true, '!': true, '?': true, ';': true, ':': true,
	'…': true, '“': true, '”': true, '‘': true, '’': true,
	'（': true, '）': true, '(': true, ')': true,
	'《': true, '》': true, '〈': true, '〉': true, '<': true, '>': true,
	'—': true, '～': true, '·': true,
}

// StripPunctuation 去掉标点（保留小数点）。
func StripPunctuation(s string) string {
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range rs {
		if r == '.' && i > 0 && i+1 < len(rs) && unicode.IsDigit(rs[i-1]) && unicode.IsDigit(rs[i+1]) {
			b.WriteRune(r)
			continue
		}
		if punctuationSet[r] {
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// TrimSpace 去首尾空白（保留内部换行）。
func TrimSpace(s string) string { return strings.TrimSpace(s) }

// RuneCount 返回字符数。
func RuneCount(s string) int { return len([]rune(s)) }

// SortCues 按开始时间稳定排序。
func SortCues(cues []Cue) {
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].Start < cues[j].Start })
}

func sortByStart(segs []Segment) {
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].Start < segs[j].Start })
}

func lastRune(s string) rune {
	rs := []rune(s)
	if len(rs) == 0 {
		return 0
	}
	return rs[len(rs)-1]
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}
