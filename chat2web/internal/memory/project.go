package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// renderProjectMemory 渲染 data/projects/{id}/memory.md。
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

func (m *Manager) loadProjectsLocked() {
	m.projects = map[string]*ProjectMemory{}
	load := func(f string) {
		data, err := os.ReadFile(f)
		if err != nil {
			return
		}
		pm := parseProjectMemory(string(data))
		if pm.ProjectID == "" {
			pm.ProjectID = strings.TrimSuffix(filepath.Base(f), ".md")
		}
		if pm.UpdatedAt.IsZero() {
			if info, statErr := os.Stat(f); statErr == nil {
				pm.UpdatedAt = info.ModTime()
			}
		}
		m.projects[pm.ProjectID] = pm
	}
	// 唯一位置：data/projects/{id}/memory.md
	entries, err := os.ReadDir(m.projectsRoot())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		load(filepath.Join(m.projectsRoot(), entry.Name(), "memory.md"))
	}
}

// splitProjectEntriesLocked 把 memory.md 中带项目 ID 的条目归入各自项目文件，
// 保证用户记忆只保留跨项目内容，项目记忆彼此隔离。
func (m *Manager) splitProjectEntriesLocked() {
	var global []Entry
	changedUser := false
	changedProjects := map[string]bool{}
	for _, e := range m.entries {
		if e.ProjectID == "" {
			global = append(global, e)
			continue
		}
		pm := m.projects[e.ProjectID]
		if pm == nil {
			pm = &ProjectMemory{ProjectID: e.ProjectID, Name: e.ProjectID}
			m.projects[e.ProjectID] = pm
		}
		if !entryDuplicate(pm.Entries, e) {
			pm.Entries = append(pm.Entries, e)
		}
		changedProjects[e.ProjectID] = true
		changedUser = true
	}
	if !changedUser {
		return
	}
	m.entries = global
	m.persistUserLocked()
	for id := range changedProjects {
		if pm := m.projects[id]; pm != nil {
			m.persistProjectLocked(pm)
		}
	}
}

func entryDuplicate(list []Entry, e Entry) bool {
	for _, cur := range list {
		if cur.ID == e.ID ||
			(cur.Kind == e.Kind && cur.Key == e.Key) ||
			(cur.Kind == e.Kind && strings.EqualFold(cur.Value, e.Value)) {
			return true
		}
	}
	return false
}

func (m *Manager) persistProjectLocked(pm *ProjectMemory) {
	pm.UpdatedAt = time.Now()
	// 只为真实存在的项目目录写记忆，避免普通会话凭空创建项目。
	dir := filepath.Join(m.projectsRoot(), safeName(pm.ProjectID))
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return
	}
	_ = writeText(m.projectFile(pm.ProjectID), renderProjectMemory(pm))
}

func (m *Manager) UpdateProjectMemory(projectID, name, description string, tech, pages []string, status, notes, historySummary string) {
	if strings.TrimSpace(projectID) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pm, ok := m.projects[projectID]
	if !ok {
		pm = &ProjectMemory{ProjectID: projectID}
		m.projects[projectID] = pm
	}
	if name != "" {
		pm.Name = name
	}
	if description != "" {
		pm.Description = description
		if pm.Goal == "" {
			pm.Goal = description
		}
	}
	for _, t := range tech {
		t = strings.TrimSpace(t)
		if t != "" && !contains(pm.TechStack, t) {
			pm.TechStack = append(pm.TechStack, t)
		}
	}
	if len(pages) > 0 {
		pm.Pages = dedupe(pages)
	}
	if status != "" {
		pm.Status = status
	}
	if notes != "" {
		pm.Notes = notes
	}
	if historySummary != "" {
		pm.History = append(pm.History, HistoryItem{Time: time.Now(), Kind: "会话", Summary: historySummary})
		if len(pm.History) > 40 {
			pm.History = pm.History[len(pm.History)-40:]
		}
	}
	m.persistProjectLocked(pm)
	m.refreshIndexLocked()
	m.persistIndexLocked()
}

func (m *Manager) UpdateProjectState(projectID, name, description string, tech, pages []string, status, notes string) {
	m.UpdateProjectMemory(projectID, name, description, tech, pages, status, notes, "")
}

// UpdateProjectSections 供记忆管理界面编辑项目的目标/架构/下一步与未解决问题。
func (m *Manager) UpdateProjectSections(projectID, goal, architecture string, openIssues, nextSteps []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	pm, ok := m.projects[projectID]
	if !ok {
		return fmt.Errorf("项目记忆不存在")
	}
	if goal != "" {
		pm.Goal = goal
	}
	if architecture != "" {
		pm.Architecture = architecture
	}
	if openIssues != nil {
		pm.OpenIssues = openIssues
	}
	if nextSteps != nil {
		pm.NextSteps = nextSteps
	}
	m.persistProjectLocked(pm)
	m.refreshIndexLocked()
	m.persistIndexLocked()
	return nil
}

func (m *Manager) ProjectMemoryText(projectID string) (string, error) {
	data, err := os.ReadFile(m.projectFile(projectID))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (m *Manager) applyDigestToProject(projectID string, d *SessionDigest) {
	if strings.TrimSpace(projectID) == "" || d == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pm, ok := m.projects[projectID]
	if !ok {
		return
	}
	pm.Completed = appendUnique(pm.Completed, d.Completed, 12)
	if len(d.OpenIssues) > 0 {
		pm.OpenIssues = dedupe(d.OpenIssues)
	}
	if len(d.NextSteps) > 0 {
		pm.NextSteps = dedupe(d.NextSteps)
	}
	for _, c := range d.Conclusions {
		if historyContains(pm.Decisions, c) {
			continue
		}
		pm.Decisions = append(pm.Decisions, HistoryItem{Time: time.Now(), Kind: "结论", Summary: c})
	}
	if len(pm.Decisions) > 20 {
		pm.Decisions = pm.Decisions[len(pm.Decisions)-20:]
	}
	m.persistProjectLocked(pm)
	m.refreshIndexLocked()
	m.persistIndexLocked()
}

func historyContains(list []HistoryItem, summary string) bool {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item.Summary), strings.TrimSpace(summary)) {
			return true
		}
	}
	return false
}

// ForgetProject 在删除项目后移除其项目记忆与索引引用；会话摘要作为历史保留。
// 项目目录（含 memory.md）已由项目管理器移入回收站，可随项目恢复。
func (m *Manager) ForgetProject(projectID string) {
	if strings.TrimSpace(projectID) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.projects, projectID)
	m.refreshIndexLocked()
	m.persistIndexLocked()
}

// MoveEntryToProject 把用户记忆中的项目专属条目迁移到对应项目记忆，避免跨项目污染。
func (m *Manager) MoveEntryToProject(entryID, projectID string) error {
	entryID = strings.TrimPrefix(strings.TrimSpace(entryID), "entry:")
	m.mu.Lock()
	defer m.mu.Unlock()
	var moved *Entry
	var kept []Entry
	for _, e := range m.entries {
		if e.ID == entryID {
			cp := e
			moved = &cp
			continue
		}
		kept = append(kept, e)
	}
	if moved == nil {
		return fmt.Errorf("记忆条目不存在：%s", entryID)
	}
	m.entries = kept
	moved.ProjectID = projectID
	pm := m.projects[projectID]
	if pm == nil {
		pm = &ProjectMemory{ProjectID: projectID, Name: projectID}
		m.projects[projectID] = pm
	}
	if !entryDuplicate(pm.Entries, *moved) {
		pm.Entries = append(pm.Entries, *moved)
	}
	m.persistUserLocked()
	m.persistProjectLocked(pm)
	m.refreshIndexLocked()
	m.persistIndexLocked()
	return nil
}
