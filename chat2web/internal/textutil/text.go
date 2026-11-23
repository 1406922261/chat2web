package textutil

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var (
	latinRe = regexp.MustCompile(`[a-zA-Z0-9][a-zA-Z0-9+#._-]*`)
	cjkRe   = regexp.MustCompile(`\p{Han}+`)
)

// Keywords 提取用于检索/匹配的关键词：英文单词 + 中文二元组。
func Keywords(s string) []string {
	s = strings.ToLower(s)
	seen := map[string]bool{}
	var out []string
	add := func(w string) {
		if len([]rune(w)) < 2 || seen[w] {
			return
		}
		seen[w] = true
		out = append(out, w)
	}
	for _, w := range latinRe.FindAllString(s, -1) {
		add(strings.Trim(w, "._-"))
	}
	for _, run := range cjkRe.FindAllString(s, -1) {
		r := []rune(run)
		if len(r) == 1 {
			add(string(r))
			continue
		}
		for i := 0; i+1 < len(r); i++ {
			add(string(r[i : i+2]))
		}
	}
	return out
}

// KeywordsLimited 在 Keywords 基础上过滤停用词并按重要度截断，
// 用于记忆索引与项目匹配，避免二字碎词无限膨胀。
func KeywordsLimited(s string, max int) []string {
	if max <= 0 {
		max = 12
	}
	low := strings.ToLower(s)
	type scored struct {
		word  string
		score int
	}
	seen := map[string]bool{}
	var list []scored
	for _, w := range Keywords(s) {
		if seen[w] || keywordStopwords[w] || isDigits(w) {
			continue
		}
		seen[w] = true
		n := strings.Count(low, w)
		if n == 0 {
			n = 1
		}
		list = append(list, scored{word: w, score: n * len([]rune(w))})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].word < list[j].word
	})
	if len(list) > max {
		list = list[:max]
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, item.word)
	}
	return out
}

var keywordStopwords = map[string]bool{
	"我们": true, "你们": true, "他们": true, "这个": true, "那个": true,
	"什么": true, "怎么": true, "可以": true, "需要": true, "希望": true,
	"以及": true, "或者": true, "如果": true, "因为": true, "所以": true,
	"但是": true, "而且": true, "进行": true, "通过": true, "提供": true,
	"一个": true, "一下": true, "一些": true, "自己": true, "就是": true,
	"不是": true, "没有": true, "还有": true, "能够": true, "时候": true,
	"现在": true, "今天": true, "明天": true, "昨天": true, "同时": true,
	"其中": true, "每个": true, "所有": true, "已经": true, "正在": true,
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// Truncate 按字符截断，保持 UTF-8 完整。
func Truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "\n...(内容已截断)"
}

// Compact 压缩空白字符，用于日志和预览。
func Compact(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// HasAny 判断文本是否包含任一关键词（忽略大小写）。
func HasAny(text string, words ...string) bool {
	t := strings.ToLower(text)
	for _, w := range words {
		if w != "" && strings.Contains(t, strings.ToLower(w)) {
			return true
		}
	}
	return false
}

// TitleFromMessage 从用户输入中提取简短标题。
func TitleFromMessage(msg string) string {
	clean := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, msg))
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return "未命名网站项目"
	}
	r := []rune(clean)
	if len(r) > 22 {
		return string(r[:22]) + "…"
	}
	return clean
}
