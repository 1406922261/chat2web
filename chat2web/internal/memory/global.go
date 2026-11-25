package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"chat2web/internal/textutil"
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

// renderUserMemory 渲染 memory/memory.md。
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

// parseUserMemory 解析 memory.md，返回条目与手动备注。
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

func (m *Manager) loadUserLocked() {
	m.entries = nil
	m.manual = ""
	data, err := os.ReadFile(m.userFile())
	if err == nil {
		m.entries, m.manual = parseUserMemory(string(data))
	}
	if m.entries == nil {
		m.entries = []Entry{}
	}
	if _, err := os.Stat(m.userFile()); err != nil {
		_ = writeText(m.userFile(), renderUserMemory(m.entries, m.manual, time.Now()))
	}
}

func (m *Manager) UserMemoryText() string { return m.Profile() }

func (m *Manager) ReadMemoryFile(name string) (string, error) {
	clean := filepath.Clean(strings.ReplaceAll(name, "\\", "/"))
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", fmt.Errorf("非法路径")
	}
	// 兼容旧调用：裸文件名指向全局记忆目录。
	switch clean {
	case "memory.md", "MEMORY.md", "index.json", "user_profile.md", "long_term.json":
		clean = "memory/" + clean
	}
	allowed := clean == "memory/memory.md" || clean == "memory/MEMORY.md" || clean == "memory/index.json" ||
		clean == "memory/user_profile.md" || clean == "memory/long_term.json" ||
		strings.HasPrefix(clean, "projects/") || strings.HasPrefix(clean, "sessions/")
	if !allowed {
		return "", fmt.Errorf("不允许读取该记忆文件")
	}
	data, err := os.ReadFile(filepath.Join(m.dataRoot(), filepath.FromSlash(clean)))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type scoredEntry struct {
	e Entry
	s int
}

type scoredDigest struct {
	d *SessionDigest
	s int
}

// Retrieve 按需检索记忆并限制注入预算：用户记忆 ≤800 字、项目记忆 ≤1000 字、
// 会话摘要最多 2 条（各 180 字），总量不超过配置预算。不调用大模型。
func (m *Manager) Retrieve(query, projectID string) string {
	m.mu.RLock()
	entries := make([]Entry, len(m.entries))
	copy(entries, m.entries)
	digests := make([]*SessionDigest, 0, len(m.digests))
	for _, d := range m.digests {
		cp := *d
		digests = append(digests, &cp)
	}
	var pm *ProjectMemory
	if p, ok := m.projects[projectID]; ok {
		cp := *p
		pm = &cp
	}
	budget := m.opts.Budget
	m.mu.RUnlock()
	if budget <= 0 {
		budget = 2400
	}

	qk := textutil.Keywords(query)
	score := func(text string) int {
		low := strings.ToLower(text)
		n := 0
		for _, k := range qk {
			if strings.Contains(low, k) {
				n++
			}
		}
		return n
	}

	// m.entries 只包含全局用户记忆；项目条目从当前项目记忆中单独读取，实现隔离。
	var global, proj []scoredEntry
	for _, e := range entries {
		global = append(global, scoredEntry{e: e, s: score(e.Key + " " + e.Value + " " + strings.Join(e.Tags, " "))})
	}
	if pm != nil {
		for _, e := range pm.Entries {
			proj = append(proj, scoredEntry{e: e, s: score(e.Key + " " + e.Value + " " + strings.Join(e.Tags, " "))})
		}
	}
	sortScored := func(list []scoredEntry) {
		sort.Slice(list, func(i, j int) bool {
			if list[i].s != list[j].s {
				return list[i].s > list[j].s
			}
			return list[i].e.UpdatedAt.After(list[j].e.UpdatedAt)
		})
	}
	sortScored(global)
	sortScored(proj)

	// 用户记忆：优先关键词命中，其次最近 3 条；上限 8 条 / 800 字。
	globalLimit := 8
	globalBudget := 800
	var userLines []string
	used := 0
	for i, item := range global {
		if len(userLines) >= globalLimit {
			break
		}
		if item.s == 0 && i >= 3 && !item.e.Pinned {
			continue
		}
		line := fmt.Sprintf("- %s：%s\n", item.e.Key, item.e.Value)
		if used+runeLen(line) > globalBudget {
			break
		}
		used += runeLen(line)
		userLines = append(userLines, line)
	}

	var out strings.Builder
	out.WriteString("## 长期记忆（按需检索）\n")
	if len(userLines) > 0 {
		out.WriteString("\n### 用户长期偏好\n")
		for _, line := range userLines {
			out.WriteString(line)
		}
	}

	// 项目记忆：仅加载当前项目，保证项目隔离。
	if pm != nil {
		out.WriteString("\n### 当前项目记忆\n")
		projText := renderProjectContext(pm, projectID)
		out.WriteString(textutil.Truncate(projText, 1000))
		out.WriteString("\n")
	}
	if len(proj) > 0 {
		out.WriteString("\n### 本项目相关记忆\n")
		limit := min(4, len(proj))
		for i := 0; i < limit; i++ {
			fmt.Fprintf(&out, "- %s：%s\n", proj[i].e.Key, proj[i].e.Value)
		}
	}

	// 会话摘要：最多 2 条，每条 180 字；优先关键词命中，其次项目匹配。
	var matched []scoredDigest
	for _, d := range digests {
		s := score(d.CoreTask + " " + d.Summary + " " + strings.Join(d.Keywords, " "))
		if projectID != "" && d.ProjectID == projectID {
			s += 2
		}
		if s > 0 {
			matched = append(matched, scoredDigest{d: d, s: s})
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].s != matched[j].s {
			return matched[i].s > matched[j].s
		}
		return matched[i].d.UpdatedAt.After(matched[j].d.UpdatedAt)
	})
	if len(matched) > 2 {
		matched = matched[:2]
	}
	if len(matched) > 0 {
		out.WriteString("\n### 相关历史会话摘要\n")
		for _, item := range matched {
			text := item.d.Summary
			if strings.TrimSpace(text) == "" {
				text = item.d.CoreTask
			}
			fmt.Fprintf(&out, "- [%s] %s\n", item.d.UpdatedAt.Format("01-02 15:04"), textutil.Truncate(text, 180))
		}
	}

	result := strings.TrimSpace(out.String())
	if result == "## 长期记忆（按需检索）" {
		return "（暂无相关长期记忆，本次作为新需求处理）"
	}
	return textutil.Truncate(result, budget)
}

func renderProjectContext(pm *ProjectMemory, projectID string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- 项目：%s（%s）\n", orDefault(pm.Name, projectID), pm.Status)
	if goal := orDefault(pm.Goal, pm.Description); goal != "" {
		fmt.Fprintf(&b, "- 目标：%s\n", textutil.Truncate(goal, 200))
	}
	if len(pm.TechStack) > 0 {
		fmt.Fprintf(&b, "- 技术栈：%s\n", strings.Join(pm.TechStack, ", "))
	}
	if len(pm.Pages) > 0 {
		fmt.Fprintf(&b, "- 页面：%s\n", strings.Join(pm.Pages, "、"))
	}
	if len(pm.Completed) > 0 {
		fmt.Fprintf(&b, "- 已完成：%s\n", strings.Join(lastN(pm.Completed, 3), "；"))
	}
	if len(pm.OpenIssues) > 0 {
		fmt.Fprintf(&b, "- 待解决：%s\n", strings.Join(lastN(pm.OpenIssues, 3), "；"))
	}
	if len(pm.NextSteps) > 0 {
		fmt.Fprintf(&b, "- 下一步：%s\n", strings.Join(lastN(pm.NextSteps, 3), "；"))
	}
	if len(pm.Decisions) > 0 {
		d := pm.Decisions[len(pm.Decisions)-1]
		fmt.Fprintf(&b, "- 最近决策：%s\n", textutil.Truncate(d.Summary, 160))
	}
	if len(pm.History) > 0 {
		h := pm.History[len(pm.History)-1]
		fmt.Fprintf(&b, "- 最近进展：%s\n", textutil.Truncate(h.Summary, 160))
	}
	return b.String()
}

// SearchMemory 用关键词与元数据过滤检索记忆，不触发大模型，也不加载全部正文。
func (m *Manager) SearchMemory(query string, limit int) []SearchResult {
	m.mu.RLock()
	idx := make([]IndexEntry, len(m.index.Entries))
	copy(idx, m.index.Entries)
	m.mu.RUnlock()
	if limit <= 0 {
		limit = 10
	}
	qk := textutil.Keywords(query)
	if len(qk) == 0 {
		out := make([]SearchResult, 0, min(limit, len(idx)))
		for i := 0; i < len(idx) && i < limit; i++ {
			out = append(out, SearchResult{IndexEntry: idx[i], Snippet: m.snippet(idx[i], nil)})
		}
		return out
	}
	type hit struct {
		entry IndexEntry
		s     int
	}
	var hits []hit
	for _, e := range idx {
		text := strings.ToLower(e.Title + " " + strings.Join(e.Keywords, " ") + " " + e.ProjectID)
		s := 0
		for _, k := range qk {
			if strings.Contains(text, k) {
				s++
			}
		}
		if s > 0 {
			hits = append(hits, hit{entry: e, s: s})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].s != hits[j].s {
			return hits[i].s > hits[j].s
		}
		return hits[i].entry.UpdatedAt.After(hits[j].entry.UpdatedAt)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]SearchResult, 0, len(hits))
	for _, h := range hits {
		out = append(out, SearchResult{IndexEntry: h.entry, Snippet: m.snippet(h.entry, qk)})
	}
	return out
}

func (m *Manager) snippet(e IndexEntry, keywords []string) string {
	if e.File == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(m.dataRoot(), filepath.FromSlash(e.File)))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if len(keywords) == 0 {
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, ">") {
				return textutil.Truncate(textutil.Compact(line), 160)
			}
		}
		return ""
	}
	for _, line := range lines {
		low := strings.ToLower(line)
		for _, k := range keywords {
			if strings.Contains(low, k) {
				return textutil.Truncate(textutil.Compact(strings.TrimSpace(line)), 160)
			}
		}
	}
	return ""
}

func runeLen(s string) int { return len([]rune(s)) }

func lastN(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	return list[len(list)-n:]
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
