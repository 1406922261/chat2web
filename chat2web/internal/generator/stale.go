package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// TextHit 表示源码中仍然残留旧主题的一条可见文案。
type TextHit struct {
	File string
	Line int
	Text string
}

func (h TextHit) String() string {
	return fmt.Sprintf("%s:%d %s", h.File, h.Line, strings.TrimSpace(h.Text))
}

var siteTopicRe = regexp.MustCompile(`(?m)^\s*export\s+const\s+topic\s*=\s*['"]([^'"]+)['"]`)

// CurrentTopic 读取已生成站点 src/data/site.ts 中声明的主题，用于判断本次需求是否已经改变主题。
func CurrentTopic(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "src", "data", "site.ts"))
	if err != nil {
		return ""
	}
	m := siteTopicRe.FindSubmatch(data)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// FindStaleText 扫描 src 下的 .ts/.tsx 文案，返回仍包含旧主题的行。
// 仅用于检测，不做任何改写；调用方决定是提示模型还是如实报告。
func FindStaleText(dir, stale string, limit int) []TextHit {
	stale = strings.TrimSpace(stale)
	if stale == "" {
		return nil
	}
	if limit <= 0 {
		limit = 20
	}
	var hits []TextHit
	scan := func(p string) bool {
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return true
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			rel = p
		}
		rel = filepath.ToSlash(rel)
		for i, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, stale) {
				continue
			}
			hits = append(hits, TextHit{File: rel, Line: i + 1, Text: strings.TrimSpace(line)})
			if len(hits) >= limit {
				return false
			}
		}
		return true
	}
	root := filepath.Join(dir, "src")
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		switch filepath.Ext(p) {
		case ".ts", ".tsx":
		default:
			return nil
		}
		if !scan(p) {
			return filepath.SkipAll
		}
		return nil
	})
	// index.html 的 <title> 同样会展示给用户，必须一并检查。
	if len(hits) < limit {
		scan(filepath.Join(dir, "index.html"))
	}
	return hits
}
