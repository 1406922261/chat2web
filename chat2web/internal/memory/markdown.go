package memory

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 记忆文件采用 Markdown + 行内 HTML 注释元数据：
// 人类可直接阅读/编辑，程序也能稳定解析，避免额外的结构化文件。

func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func sanitizeAttr(s string) string {
	s = singleLine(s)
	s = strings.ReplaceAll(s, ";", "；")
	s = strings.ReplaceAll(s, ",", "，")
	return s
}

func parseAttrs(line string) map[string]string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "<!-- memory:")
	line = strings.TrimSuffix(line, "-->")
	out := map[string]string{}
	for _, part := range strings.Split(line, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

func renderEntryLine(e Entry) string {
	return renderEntryLineQualified(e, e.ProjectID != "")
}

func renderEntryLineQualified(e Entry, qualified bool) string {
	key := singleLine(e.Key)
	if qualified && e.ProjectID != "" {
		key = "[" + e.ProjectID + "] " + key
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- **%s**：%s\n", key, singleLine(e.Value))
	parts := []string{
		"id=" + e.ID,
		"kind=" + e.Kind,
		"tags=" + strings.Join(e.Tags, "，"),
		"source=" + e.Source,
	}
	if e.Pinned {
		parts = append(parts, "pinned=1")
	}
	parts = append(parts, "updated="+e.UpdatedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "  <!-- memory:%s -->\n", strings.Join(parts, ";"))
	return b.String()
}

func sectionKind(title string) string {
	switch strings.TrimSpace(title) {
	case "偏好", "设计偏好":
		return "preference"
	case "技术栈":
		return "tech"
	case "其他事实", "事实":
		return "fact"
	case "项目相关记忆", "项目":
		return "project"
	}
	return ""
}

func kindTitle(kind string) string {
	switch kind {
	case "preference":
		return "偏好"
	case "tech":
		return "技术栈"
	case "fact":
		return "其他事实"
	case "project":
		return "项目相关记忆"
	}
	return "其他事实"
}

// renderUserMemory 渲染 memory/MEMORY.md。
func renderUserMemory(entries []Entry, manual string, updated time.Time) string {
	var b strings.Builder
	b.WriteString("# 用户长期记忆\n\n")
	b.WriteString("> 由 Chat2Web 自动维护：会话结束时会增量更新，避免重复记录。\n")
	b.WriteString("> 可直接编辑「手动备注」；其余条目由系统生成，建议通过记忆管理菜单修改。\n")
	fmt.Fprintf(&b, "> 更新时间：%s\n\n", updated.Format("2006-01-02 15:04:05"))

	order := []string{"preference", "tech", "fact", "project"}
	has := map[string]bool{}
	for _, e := range entries {
		has[e.Kind] = true
	}
	for _, kind := range order {
		if !has[kind] {
			continue
		}
		fmt.Fprintf(&b, "## %s\n", kindTitle(kind))
		for _, e := range entries {
			if e.Kind == kind {
				b.WriteString(renderEntryLine(e))
			}
		}
		b.WriteString("\n")
	}
	if len(entries) == 0 {
		b.WriteString("## 偏好\n- 暂无记录，等待首次会话后自动提取。\n\n")
	}
	b.WriteString("## 手动备注\n\n")
	if strings.TrimSpace(manual) != "" {
		b.WriteString(strings.TrimSpace(manual))
		b.WriteString("\n")
	} else {
		b.WriteString("（可在此处补充希望 Agent 长期记住的内容）\n")
	}
	return b.String()
}

// parseUserMemory 解析 MEMORY.md，返回条目与手动备注。
func parseUserMemory(content string) ([]Entry, string) {
	var entries []Entry
	lines := strings.Split(content, "\n")
	kind := ""
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "## ") {
			title := strings.TrimPrefix(line, "## ")
			if strings.TrimSpace(title) == "手动备注" {
				manual := strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
				if strings.HasPrefix(manual, "（可在此处") {
					manual = ""
				}
				return entries, manual
			}
			kind = sectionKind(title)
			continue
		}
		if !strings.HasPrefix(line, "- **") || kind == "" {
			continue
		}
		rest := strings.TrimPrefix(line, "- **")
		idx := strings.Index(rest, "**：")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(rest[:idx])
		value := strings.TrimSpace(rest[idx+len("**："):])
		e := Entry{Kind: kind, Key: key, Value: value, Tags: []string{}}
		if strings.HasPrefix(key, "[") {
			if end := strings.Index(key, "]"); end > 0 {
				e.ProjectID = strings.TrimSpace(key[1:end])
				e.Key = strings.TrimSpace(key[end+1:])
			}
		}
		if i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if strings.HasPrefix(next, "<!-- memory:") {
				attrs := parseAttrs(next)
				if v := attrs["id"]; v != "" {
					e.ID = v
				}
				if v := attrs["kind"]; v != "" {
					e.Kind = v
				}
				if v := attrs["source"]; v != "" {
					e.Source = v
				}
				if v := attrs["tags"]; v != "" {
					e.Tags = splitTags(v)
				}
				e.Pinned = attrs["pinned"] == "1"
				if v := attrs["updated"]; v != "" {
					if t, err := time.Parse(time.RFC3339, v); err == nil {
						e.UpdatedAt = t
					}
				}
				i++
			}
		}
		if e.ID == "" {
			e.ID = randomID("m")
		}
		if e.Source == "" {
			e.Source = "manual"
		}
		if e.UpdatedAt.IsZero() {
			e.UpdatedAt = time.Now()
		}
		entries = append(entries, e)
	}
	return entries, ""
}

// parseEntryBullets 解析项目记忆中的「项目相关记忆」条目。
func parseEntryBullets(body, projectID string) []Entry {
	var entries []Entry
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "- **") {
			continue
		}
		rest := strings.TrimPrefix(line, "- **")
		idx := strings.Index(rest, "**：")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(rest[:idx])
		value := strings.TrimSpace(rest[idx+len("**："):])
		e := Entry{Kind: "preference", Key: key, Value: value, Tags: []string{}, ProjectID: projectID}
		if i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if strings.HasPrefix(next, "<!-- memory:") {
				attrs := parseAttrs(next)
				if v := attrs["id"]; v != "" {
					e.ID = v
				}
				if v := attrs["kind"]; v != "" {
					e.Kind = v
				}
				if v := attrs["source"]; v != "" {
					e.Source = v
				}
				if v := attrs["tags"]; v != "" {
					e.Tags = splitTags(v)
				}
				e.Pinned = attrs["pinned"] == "1"
				if v := attrs["updated"]; v != "" {
					if t, err := time.Parse(time.RFC3339, v); err == nil {
						e.UpdatedAt = t
					}
				}
				i++
			}
		}
		if e.ID == "" {
			e.ID = randomID("m")
		}
		if e.Source == "" {
			e.Source = "manual"
		}
		if e.UpdatedAt.IsZero() {
			e.UpdatedAt = time.Now()
		}
		entries = append(entries, e)
	}
	return entries
}

func splitTags(s string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '，' || r == '、'
	}) {
		t = strings.TrimSpace(t)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// renderProjectMemory 渲染 memory/projects/{id}.md。
func renderProjectMemory(pm *ProjectMemory) string {
	var b strings.Builder
	name := pm.Name
	if strings.TrimSpace(name) == "" {
		name = pm.ProjectID
	}
	fmt.Fprintf(&b, "# 项目记忆：%s\n\n", name)
	fmt.Fprintf(&b, "- 项目ID：%s\n", pm.ProjectID)
	fmt.Fprintf(&b, "- 更新时间：%s\n\n", pm.UpdatedAt.Format("2006-01-02 15:04:05"))

	goal := pm.Goal
	if strings.TrimSpace(goal) == "" {
		goal = pm.Description
	}
	fmt.Fprintf(&b, "## 项目目标\n%s\n\n", orDash(goal))
	fmt.Fprintf(&b, "## 技术栈\n%s\n\n", bullets(pm.TechStack))
	fmt.Fprintf(&b, "## 项目架构\n%s\n\n", orDash(pm.Architecture))
	fmt.Fprintf(&b, "## 已完成功能\n%s\n\n", bullets(pm.Completed))
	b.WriteString("## 当前开发进度\n")
	fmt.Fprintf(&b, "- 状态：%s\n", orDash(pm.Status))
	fmt.Fprintf(&b, "- 页面：%s\n\n", orDash(strings.Join(pm.Pages, "、")))
	fmt.Fprintf(&b, "## 重要设计决策\n%s\n\n", historyBullets(pm.Decisions))
	fmt.Fprintf(&b, "## 未解决的问题\n%s\n\n", bullets(pm.OpenIssues))
	fmt.Fprintf(&b, "## 下一步计划\n%s\n\n", bullets(pm.NextSteps))
	b.WriteString("## 项目相关记忆\n")
	if len(pm.Entries) == 0 {
		b.WriteString("（暂无）\n\n")
	} else {
		for _, e := range pm.Entries {
			b.WriteString(renderEntryLineQualified(e, false))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## 变更历史\n%s\n\n", historyBullets(pm.History))
	return b.String()
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（暂无）"
	}
	return strings.TrimSpace(s)
}

func bullets(list []string) string {
	if len(list) == 0 {
		return "（暂无）"
	}
	var b strings.Builder
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		fmt.Fprintf(&b, "- %s\n", singleLine(item))
	}
	if b.Len() == 0 {
		return "（暂无）"
	}
	return strings.TrimRight(b.String(), "\n")
}

func historyBullets(list []HistoryItem) string {
	if len(list) == 0 {
		return "（暂无）"
	}
	var b strings.Builder
	for _, item := range list {
		fmt.Fprintf(&b, "- [%s][%s] %s\n", item.Time.Format("2006-01-02 15:04"), item.Kind, singleLine(item.Summary))
	}
	return strings.TrimRight(b.String(), "\n")
}

// parseProjectMemory 解析 projects/{id}.md。
func parseProjectMemory(content string) *ProjectMemory {
	pm := &ProjectMemory{}
	lines := strings.Split(content, "\n")
	section := ""
	var buf []string
	flush := func() {
		body := strings.TrimSpace(strings.Join(buf, "\n"))
		buf = nil
		switch section {
		case "项目目标":
			pm.Goal = cleanDash(body)
		case "技术栈":
			pm.TechStack = parseBullets(body)
		case "项目架构":
			pm.Architecture = cleanDash(body)
		case "已完成功能":
			pm.Completed = parseBullets(body)
		case "当前开发进度":
			for _, line := range strings.Split(body, "\n") {
				line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
				if v, ok := strings.CutPrefix(line, "状态："); ok {
					pm.Status = strings.TrimSpace(v)
				}
				if v, ok := strings.CutPrefix(line, "页面："); ok {
					pm.Pages = splitTags(v)
				}
			}
		case "重要设计决策":
			pm.Decisions = parseHistoryBullets(body)
		case "未解决的问题":
			pm.OpenIssues = parseBullets(body)
		case "下一步计划":
			pm.NextSteps = parseBullets(body)
		case "项目相关记忆":
			pm.Entries = parseEntryBullets(body, pm.ProjectID)
		case "变更历史":
			pm.History = parseHistoryBullets(body)
		}
	}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "# ") {
			if name, ok := strings.CutPrefix(line, "# 项目记忆："); ok {
				pm.Name = strings.TrimSpace(name)
			}
			continue
		}
		if strings.HasPrefix(line, "- 项目ID：") {
			pm.ProjectID = strings.TrimSpace(strings.TrimPrefix(line, "- 项目ID："))
			continue
		}
		if strings.HasPrefix(line, "- 更新时间：") {
			if t, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(strings.TrimPrefix(line, "- 更新时间："))); err == nil {
				pm.UpdatedAt = t
			}
			continue
		}
		if strings.HasPrefix(line, "## ") {
			flush()
			section = strings.TrimPrefix(line, "## ")
			continue
		}
		if section != "" {
			buf = append(buf, raw)
		}
	}
	flush()
	if pm.Description == "" {
		pm.Description = pm.Goal
	}
	return pm
}

func cleanDash(s string) string {
	if s == "（暂无）" {
		return ""
	}
	return s
}

func parseBullets(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "（暂无）" {
			continue
		}
		out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "- ")))
	}
	return out
}

func parseHistoryBullets(body string) []HistoryItem {
	var out []HistoryItem
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if !strings.HasPrefix(line, "[") {
			continue
		}
		end := strings.Index(line, "]")
		if end < 0 {
			continue
		}
		ts := line[1:end]
		rest := line[end+1:]
		kind := ""
		if strings.HasPrefix(rest, "[") {
			if k := strings.Index(rest, "]"); k > 0 {
				kind = rest[1:k]
				rest = rest[k+1:]
			}
		}
		t, _ := time.Parse("2006-01-02 15:04", ts)
		out = append(out, HistoryItem{Time: t, Kind: strings.TrimSpace(kind), Summary: strings.TrimSpace(rest)})
	}
	return out
}

// renderDigest 渲染 sessions/YYYY-MM/{session}.md。
func renderDigest(d *SessionDigest) string {
	var b strings.Builder
	b.WriteString("# 会话摘要\n\n")
	fmt.Fprintf(&b, "- 会话：%s\n", d.SessionID)
	fmt.Fprintf(&b, "- 项目ID：%s\n", d.ProjectID)
	if d.ProjectName != "" {
		fmt.Fprintf(&b, "- 项目：%s\n", d.ProjectName)
	}
	fmt.Fprintf(&b, "- 更新时间：%s\n", d.UpdatedAt.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- 消息游标：%d\n", d.Cursor)
	if len(d.Keywords) > 0 {
		fmt.Fprintf(&b, "- 关键词：%s\n", strings.Join(d.Keywords, ", "))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "## 核心任务\n%s\n\n", orDash(d.CoreTask))
	fmt.Fprintf(&b, "## 已完成工作\n%s\n\n", bullets(d.Completed))
	fmt.Fprintf(&b, "## 重要结论\n%s\n\n", bullets(d.Conclusions))
	fmt.Fprintf(&b, "## 未解决问题\n%s\n\n", bullets(d.OpenIssues))
	fmt.Fprintf(&b, "## 下一步\n%s\n\n", bullets(d.NextSteps))
	fmt.Fprintf(&b, "## 摘要\n%s\n", orDash(d.Summary))
	return b.String()
}

func parseDigest(content, file string) *SessionDigest {
	d := &SessionDigest{File: file}
	lines := strings.Split(content, "\n")
	section := ""
	var buf []string
	flush := func() {
		body := strings.TrimSpace(strings.Join(buf, "\n"))
		buf = nil
		switch section {
		case "核心任务":
			d.CoreTask = cleanDash(body)
		case "已完成工作":
			d.Completed = parseBullets(body)
		case "重要结论":
			d.Conclusions = parseBullets(body)
		case "未解决问题":
			d.OpenIssues = parseBullets(body)
		case "下一步":
			d.NextSteps = parseBullets(body)
		case "摘要":
			d.Summary = cleanDash(body)
		}
	}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "- 会话："):
			d.SessionID = strings.TrimSpace(strings.TrimPrefix(line, "- 会话："))
		case strings.HasPrefix(line, "- 项目ID："):
			d.ProjectID = strings.TrimSpace(strings.TrimPrefix(line, "- 项目ID："))
		case strings.HasPrefix(line, "- 项目："):
			d.ProjectName = strings.TrimSpace(strings.TrimPrefix(line, "- 项目："))
		case strings.HasPrefix(line, "- 更新时间："):
			if t, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(strings.TrimPrefix(line, "- 更新时间："))); err == nil {
				d.UpdatedAt = t
			}
		case strings.HasPrefix(line, "- 消息游标："):
			d.Cursor, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "- 消息游标：")))
		case strings.HasPrefix(line, "- 关键词："):
			d.Keywords = splitTags(strings.TrimSpace(strings.TrimPrefix(line, "- 关键词：")))
		case strings.HasPrefix(line, "## "):
			flush()
			section = strings.TrimPrefix(line, "## ")
		default:
			if section != "" {
				buf = append(buf, raw)
			}
		}
	}
	flush()
	if d.SessionID == "" {
		d.SessionID = strings.TrimSuffix(filepathBase(file), ".md")
	}
	return d
}

func filepathBase(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
