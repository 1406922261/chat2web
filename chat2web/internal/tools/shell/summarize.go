package shell

import (
	"fmt"
	"regexp"
	"strings"
)

// errorLineRe 匹配构建/安装日志中的关键错误行，避免把完整日志回填给模型。
var errorLineRe = regexp.MustCompile(`(?i)(error|failed|cannot|not found|TS\d{4}|ELIFECYCLE|ERR!)`)

// Summarize 把命令输出压缩为：退出码 + 关键错误行 + 末尾若干行。
// 完整日志仍保留在 Result.Stdout/Stderr 中，仅用于回填 LLM 时摘要。
func Summarize(res Result, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 1500
	}
	var b strings.Builder
	fmt.Fprintf(&b, "exit=%d duration=%dms timeout=%v allowed=%v", res.ExitCode, res.DurationMs, res.TimedOut, res.Allowed)
	if res.Blocked != "" {
		fmt.Fprintf(&b, " blocked=%s", res.Blocked)
	}
	if res.Error != "" {
		fmt.Fprintf(&b, " error=%s", res.Error)
	}
	b.WriteString("\n")

	combined := res.Stderr + "\n" + res.Stdout
	lines := splitLines(combined)

	var key []string
	seen := map[string]bool{}
	for _, line := range lines {
		if !errorLineRe.MatchString(line) {
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		key = append(key, clip(line, 300))
		if len(key) >= 10 {
			break
		}
	}
	if len(key) > 0 {
		b.WriteString("关键错误：\n")
		for _, line := range key {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	tail := lines
	if len(tail) > 15 {
		tail = tail[len(tail)-15:]
	}
	var tailOut []string
	for _, line := range tail {
		t := strings.TrimSpace(line)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		tailOut = append(tailOut, t)
	}
	if len(tailOut) > 0 {
		b.WriteString("末尾输出：\n")
		for _, line := range tailOut {
			b.WriteString("  ")
			b.WriteString(clip(line, 300))
			b.WriteString("\n")
		}
	}

	out := strings.TrimRight(b.String(), "\n")
	runes := []rune(out)
	if len(runes) > maxChars {
		out = string(runes[:maxChars]) + "\n…（已截断）"
	}
	return out
}

// SummarizeText 对已经是字符串形式的命令输出做同样压缩（用于工具结果回填）。
func SummarizeText(text string, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 1500
	}
	lines := splitLines(text)
	var b strings.Builder
	seen := map[string]bool{}
	count := 0
	for _, line := range lines {
		if !errorLineRe.MatchString(line) {
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		b.WriteString("  ")
		b.WriteString(clip(line, 300))
		b.WriteString("\n")
		count++
		if count >= 10 {
			break
		}
	}
	if count > 0 {
		b.WriteString("末尾输出：\n")
	}
	tail := lines
	if len(tail) > 15 {
		tail = tail[len(tail)-15:]
	}
	for _, line := range tail {
		t := strings.TrimSpace(line)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		b.WriteString("  ")
		b.WriteString(clip(t, 300))
		b.WriteString("\n")
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		out = clip(strings.TrimSpace(text), maxChars)
	}
	runes := []rune(out)
	if len(runes) > maxChars {
		out = string(runes[:maxChars]) + "\n…（已截断）"
	}
	return out
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	raw := strings.Split(s, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func clip(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
