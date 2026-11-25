package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"chat2web/internal/llm"
	"chat2web/internal/textutil"
)

// summarizeIncrementalWithLLM 只把「已有摘要 + 新增对话」交给模型，做增量更新。
func (m *Manager) summarizeIncrementalWithLLM(ctx context.Context, exist *SessionDigest, transcript string) *SummaryResult {
	if m.llm == nil || !m.llm.Configured() {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	system := `你是网站开发 Agent 的记忆整理助手。请基于“已有摘要”和“新增对话”做增量更新，输出严格 JSON：
{
  "core_task": "本次会话的核心任务，80 字以内",
  "summary": "本次新增进展的摘要，120 字以内",
  "completed": ["已完成的工作"],
  "conclusions": ["重要结论或设计决策"],
  "open_issues": ["尚未解决的问题"],
  "next_steps": ["下一步任务"],
  "preferences": [{"key": "偏好名称", "value": "偏好内容", "tags": ["检索标签"]}],
  "tech_stack": ["React"],
  "project": {"status": "draft|generating|ready|error", "pages": ["首页"], "notes": "当前状态说明"}
}
要求：
1. 只提取用户明确表达或 Agent 已实际采用的偏好，不臆测；一次性操作不要当作长期偏好。
2. 忽略寒暄、日志、临时调试信息；没有内容时对应数组为空。
3. 不要重复已有摘要中已经记录的相同信息。`

	existing := "（无）"
	if exist != nil {
		existing = textutil.Truncate(exist.Summary+"\n已完成："+strings.Join(exist.Completed, "；")+
			"\n未解决："+strings.Join(exist.OpenIssues, "；")+"\n下一步："+strings.Join(exist.NextSteps, "；"), 1200)
	}
	var result SummaryResult
	user := "已有摘要：\n" + existing + "\n\n新增对话：\n" + transcript
	if err := m.llm.CompleteJSON(cctx, system, user, &result); err != nil {
		return nil
	}
	return &result
}

// SummarizeSession 对会话做增量摘要：只处理上次游标之后的新消息，并按月份归档。
func (m *Manager) SummarizeSession(ctx context.Context, sessionID, projectID, projectName string) (*SummaryResult, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("缺少会话 ID")
	}
	m.mu.RLock()
	var msgs []llm.Message
	if s := m.sessions[sessionID]; s != nil {
		msgs = append([]llm.Message(nil), s.Messages...)
	}
	var exist *SessionDigest
	if d := m.digests[sessionID]; d != nil {
		cp := *d
		exist = &cp
	}
	m.mu.RUnlock()

	if len(msgs) == 0 {
		return nil, fmt.Errorf("会话为空，无需摘要")
	}
	cursor := 0
	if exist != nil && exist.Cursor > 0 && exist.Cursor <= len(msgs) {
		cursor = exist.Cursor
	}
	fresh := msgs[cursor:]
	if len(fresh) == 0 {
		return nil, fmt.Errorf("没有新增对话，跳过摘要")
	}

	transcript := buildTranscript(fresh)
	var result *SummaryResult
	if m.llm != nil && m.llm.Configured() {
		result = m.summarizeIncrementalWithLLM(ctx, exist, transcript)
		if result != nil {
			result.Source = "llm"
		}
	}
	if result == nil {
		result = heuristicDigest(fresh)
		result.Source = "heuristic"
	}
	if strings.TrimSpace(result.Summary) == "" {
		result.Summary = "本次会话围绕网站需求与生成过程展开。"
	}

	if strings.TrimSpace(projectName) == "" && exist != nil {
		projectName = exist.ProjectName
	}

	merged := mergeDigest(exist, result, len(msgs), projectID, projectName)

	m.mu.Lock()
	if exist != nil && strings.EqualFold(filepath.Ext(exist.File), ".json") {
		merged.File = exist.File
	} else {
		abs := m.summaryFile(sessionID)
		rel, err := filepath.Rel(m.dataRoot(), abs)
		if err != nil {
			rel = filepath.Join("sessions", time.Now().Format("2006-01"), safeName(sessionID)+".json")
		}
		merged.File = filepath.ToSlash(rel)
	}
	if err := writeJSON(filepath.Join(m.dataRoot(), filepath.FromSlash(merged.File)), merged); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.digests[sessionID] = merged
	m.refreshIndexLocked()
	m.persistIndexLocked()
	m.mu.Unlock()

	// 偏好写入用户记忆；技术栈写入项目记忆，避免跨项目污染。
	for _, pre := range result.Preferences {
		key := strings.TrimSpace(pre.Key)
		val := strings.TrimSpace(pre.Value)
		if key == "" || val == "" {
			continue
		}
		e := Entry{Kind: "preference", Key: key, Value: val, Tags: pre.Tags, Source: result.Source}
		if key == "tech_stack" {
			e.ProjectID = projectID
			e.Kind = "tech"
		}
		m.UpsertEntry(e)
	}
	m.UpdateProjectMemory(projectID, projectName, "", result.TechStack, result.Project.Pages,
		result.Project.Status, result.Project.Notes, merged.Summary)
	m.applyDigestToProject(projectID, merged)

	result.File = merged.File
	return result, nil
}

func buildTranscript(msgs []llm.Message) string {
	var b strings.Builder
	for _, msg := range msgs {
		if msg.Role == "tool" || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", strings.ToUpper(msg.Role), msg.Content)
	}
	return textutil.Truncate(b.String(), 12000)
}

func mergeDigest(exist *SessionDigest, r *SummaryResult, cursor int, projectID, projectName string) *SessionDigest {
	d := &SessionDigest{ProjectID: projectID, ProjectName: projectName, UpdatedAt: time.Now(), Cursor: cursor}
	if exist != nil {
		cp := *exist
		d = &cp
		d.ProjectID = projectID
		if projectName != "" {
			d.ProjectName = projectName
		}
		d.Cursor = cursor
		d.UpdatedAt = time.Now()
	}
	if strings.TrimSpace(r.CoreTask) != "" {
		d.CoreTask = textutil.Compact(r.CoreTask)
	}
	d.Completed = appendUnique(d.Completed, r.Completed, 12)
	d.Conclusions = appendUnique(d.Conclusions, r.Conclusions, 8)
	if len(r.OpenIssues) > 0 {
		d.OpenIssues = dedupe(r.OpenIssues)
	}
	if len(r.NextSteps) > 0 {
		d.NextSteps = dedupe(r.NextSteps)
	}
	d.Summary = combineSummary(d.Summary, r.Summary)
	kw := append(append([]string{}, d.Keywords...), textutil.Keywords(d.CoreTask+" "+d.Summary)...)
	d.Keywords = dedupe(kw)
	if len(d.Keywords) > 16 {
		d.Keywords = d.Keywords[:16]
	}
	return d
}

func combineSummary(old, fresh string) string {
	old = strings.TrimSpace(old)
	fresh = strings.TrimSpace(fresh)
	switch {
	case old == "":
		return textutil.Truncate(fresh, 400)
	case fresh == "" || strings.Contains(old, fresh):
		return old
	case strings.Contains(fresh, old):
		return textutil.Truncate(fresh, 400)
	}
	combined := textutil.Truncate(old, 220) + "；" + textutil.Truncate(fresh, 160)
	return textutil.Truncate(combined, 400)
}

func appendUnique(dst, add []string, maxLen int) []string {
	for _, v := range add {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		dup := false
		for _, cur := range dst {
			if strings.EqualFold(strings.TrimSpace(cur), v) {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, v)
		}
	}
	if maxLen > 0 && len(dst) > maxLen {
		dst = dst[len(dst)-maxLen:]
	}
	return dst
}

// heuristicDigest 在未配置模型时用本地规则生成摘要，不伪造 LLM 结果。
func heuristicDigest(msgs []llm.Message) *SummaryResult {
	base := heuristicSummary(msgs)
	res := &SummaryResult{
		Summary:     base.Summary,
		Preferences: base.Preferences,
		TechStack:   base.TechStack,
		Project:     base.Project,
		Source:      "heuristic",
	}
	var userTexts, assistantTexts []string
	for _, msg := range msgs {
		text := textutil.Compact(msg.Content)
		if text == "" {
			continue
		}
		switch msg.Role {
		case "user":
			userTexts = append(userTexts, text)
		case "assistant":
			assistantTexts = append(assistantTexts, text)
		}
	}
	if len(userTexts) > 0 {
		res.CoreTask = textutil.Truncate(userTexts[0], 120)
	}
	for _, text := range assistantTexts {
		res.Completed = append(res.Completed, textutil.Truncate(text, 100))
		if len(res.Completed) >= 6 {
			break
		}
	}
	if len(userTexts) > 1 {
		res.NextSteps = []string{"按最新要求继续完善：" + textutil.Truncate(userTexts[len(userTexts)-1], 80)}
	}
	for _, text := range userTexts {
		low := strings.ToLower(text)
		if strings.Contains(low, "待补充") || strings.Contains(low, "未完成") || strings.Contains(low, "失败") {
			res.OpenIssues = appendUnique(res.OpenIssues, []string{textutil.Truncate(text, 80)}, 3)
		}
	}
	return res
}

var styleWords = map[string]string{
	"深色": "深色风格", "暗色": "深色风格", "暗黑": "深色风格", "dark": "深色风格",
	"浅色": "浅色风格", "简约": "简约风格", "极简": "极简风格", "高级": "高级质感",
	"科技": "科技风格", "赛博": "赛博朋克风格", "可爱": "可爱风格", "商务": "商务风格",
	"复古": "复古风格", "渐变": "渐变配色", "玻璃": "玻璃拟态", "莫兰迪": "莫兰迪配色",
}

var techWords = map[string]string{
	"react": "React", "vue": "Vue", "next.js": "Next.js", "nextjs": "Next.js",
	"typescript": "TypeScript", "javascript": "JavaScript", "tailwind": "Tailwind CSS",
	"node": "Node.js", "golang": "Go", "python": "Python", "mysql": "MySQL",
	"sqlite": "SQLite", "mongodb": "MongoDB", "postgres": "PostgreSQL",
	"supabase": "Supabase", "firebase": "Firebase",
}

var pageHints = []struct {
	kw    string
	label string
}{
	{"首页", "首页"}, {"商品列表", "商品列表"}, {"商品详情", "商品详情"}, {"购物车", "购物车"},
	{"登录", "登录"}, {"注册", "注册"}, {"个人中心", "个人中心"}, {"后台", "管理后台"},
	{"景点", "景点列表"}, {"行程", "行程规划"}, {"预算", "预算管理"}, {"作品", "项目作品"},
	{"技能", "技能展示"}, {"联系", "联系方式"}, {"博客", "博客列表"}, {"文章", "文章详情"},
}

func heuristicSummary(msgs []llm.Message) *SummaryResult {
	var userTexts []string
	for _, msg := range msgs {
		if msg.Role == "user" && strings.TrimSpace(msg.Content) != "" {
			userTexts = append(userTexts, msg.Content)
		}
	}
	all := strings.ToLower(strings.Join(userTexts, " "))

	res := &SummaryResult{Source: "heuristic"}
	if len(userTexts) > 0 {
		first := textutil.Truncate(textutil.Compact(userTexts[0]), 120)
		last := textutil.Truncate(textutil.Compact(userTexts[len(userTexts)-1]), 120)
		if len(userTexts) == 1 {
			res.Summary = "用户提出网站开发需求：" + first
		} else {
			res.Summary = "用户初始需求：" + first + "；最新进展：" + last
		}
	} else {
		res.Summary = "本次会话未产生有效需求描述。"
	}

	var styles []string
	for kw, label := range styleWords {
		if strings.Contains(all, kw) && !contains(styles, label) {
			styles = append(styles, label)
		}
	}
	if len(styles) > 0 {
		res.Preferences = append(res.Preferences, Preference{
			Key:   "design_style",
			Value: strings.Join(styles, "、"),
			Tags:  styles,
		})
	}

	var tech []string
	for kw, label := range techWords {
		if strings.Contains(all, kw) && !contains(tech, label) {
			tech = append(tech, label)
		}
	}
	if len(tech) > 0 {
		res.TechStack = tech
		res.Preferences = append(res.Preferences, Preference{
			Key:   "tech_stack",
			Value: strings.Join(tech, ", "),
			Tags:  tech,
		})
	}

	var pages []string
	for _, hint := range pageHints {
		if strings.Contains(strings.Join(userTexts, " "), hint.kw) && !contains(pages, hint.label) {
			pages = append(pages, hint.label)
		}
	}
	res.Project = ProjectState{Status: "ready", Pages: pages, Notes: "由本地规则从对话中提取（未调用 LLM 摘要）。"}
	return res
}

// loadDigestsLocked 读取按月归档的会话摘要；旧的 Markdown 摘要会转换为 JSON 并归档。
func (m *Manager) loadDigestsLocked() {
	m.digests = map[string]*SessionDigest{}
	for _, f := range m.store().List() {
		d, err := m.readDigestFile(f)
		if err != nil || d == nil || d.SessionID == "" {
			continue
		}
		if d.UpdatedAt.IsZero() {
			if info, statErr := os.Stat(f); statErr == nil {
				d.UpdatedAt = info.ModTime()
			}
		}
		if old, ok := m.digests[d.SessionID]; !ok || old.UpdatedAt.Before(d.UpdatedAt) {
			m.digests[d.SessionID] = d
		}
	}
}

// readDigestFile 读取单个会话摘要：优先 JSON；遇旧的 Markdown 摘要时解析后写成同名 JSON，
// 再把旧文件移入 data/backups/legacy-session-md-{ts}/（保留相对路径）。转换失败时保留旧文件。
func (m *Manager) readDigestFile(path string) (*SessionDigest, error) {
	rel := m.indexFileFor(path)
	if strings.EqualFold(filepath.Ext(path), ".json") {
		var d SessionDigest
		if err := readJSON(path, &d); err != nil {
			return nil, err
		}
		d.File = rel
		return &d, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	d := parseDigest(string(data), rel)
	target := strings.TrimSuffix(path, filepath.Ext(path)) + ".json"
	if exists(target) {
		var existing SessionDigest
		if err := readJSON(target, &existing); err != nil {
			return d, nil
		}
		existing.File = m.indexFileFor(target)
		_ = m.archiveLegacyDigest(path)
		return &existing, nil
	}
	d.File = m.indexFileFor(target)
	if err := writeJSON(target, d); err != nil {
		// JSON 写入失败时保留 Markdown，继续使用解析结果。
		d.File = rel
		return d, nil
	}
	_ = m.archiveLegacyDigest(path)
	return d, nil
}

// archiveLegacyDigest 把旧的 Markdown 摘要按相对路径移入备份目录，绝不删除。
func (m *Manager) archiveLegacyDigest(path string) error {
	rel, err := filepath.Rel(m.sessionsRoot(), path)
	if err != nil {
		rel = filepath.Base(path)
	}
	dir := filepath.Join(m.backupsRoot(), "legacy-session-md-"+time.Now().Format("20060102-150405"))
	return movePath(path, filepath.Join(dir, rel))
}

func (m *Manager) readDigestLocked(d *SessionDigest) string {
	if d == nil || d.File == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(m.dataRoot(), filepath.FromSlash(d.File)))
	if err != nil {
		return ""
	}
	return string(data)
}

func (m *Manager) summaryFile(sessionID string) string {
	return m.store().DigestPath(sessionID, time.Now())
}

// renderDigest 渲染 sessions/YYYY-MM/{session}.md（旧版 Markdown 摘要格式）。
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

// parseDigest 解析旧版 Markdown 会话摘要，用于一次性迁移到 JSON。
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
		name := filepathBase(file)
		d.SessionID = strings.TrimSuffix(strings.TrimSuffix(name, ".md"), ".json")
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

// ConsolidateSessions 按项目合并重复会话摘要：每个项目只保留一份最新摘要，
// 其余摘要文件移入 backup/sessions-{ts}/，不直接删除。
// 直接扫描文件而不是内存索引，避免同会话 ID 的 legacy 文件互相覆盖。
func (m *Manager) ConsolidateSessions() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	type digestFile struct {
		digest *SessionDigest
		path   string
	}
	files := m.store().List()
	groups := map[string][]digestFile{}
	for _, f := range files {
		d, err := m.readDigestFile(f)
		if err != nil || d == nil {
			continue
		}
		if d.UpdatedAt.IsZero() {
			if info, statErr := os.Stat(f); statErr == nil {
				d.UpdatedAt = info.ModTime()
			}
		}
		key := d.ProjectID
		if key == "" {
			key = d.SessionID
		}
		groups[key] = append(groups[key], digestFile{digest: d, path: f})
	}

	backupDir := filepath.Join(m.backupsRoot(), "sessions-"+time.Now().Format("20060102-150405"))
	moved := 0
	changed := false

	for projectID, list := range groups {
		if len(list) < 2 {
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].digest.UpdatedAt.After(list[j].digest.UpdatedAt) })
		merged := *list[0].digest
		for _, item := range list[1:] {
			d := item.digest
			merged.Completed = mergeUnique(merged.Completed, d.Completed)
			merged.Conclusions = mergeUnique(merged.Conclusions, d.Conclusions)
			merged.OpenIssues = mergeUnique(merged.OpenIssues, d.OpenIssues)
			merged.NextSteps = mergeUnique(merged.NextSteps, d.NextSteps)
			merged.Keywords = mergeUnique(merged.Keywords, d.Keywords)
			if strings.TrimSpace(d.Summary) != "" && len([]rune(d.Summary)) > len([]rune(merged.Summary)) {
				merged.Summary = d.Summary
			}
			if strings.TrimSpace(merged.CoreTask) == "" && strings.TrimSpace(d.CoreTask) != "" {
				merged.CoreTask = d.CoreTask
			}
			if merged.ProjectName == "" {
				merged.ProjectName = d.ProjectName
			}
		}
		merged.Completed = trimList(merged.Completed, 12)
		merged.Keywords = trimList(merged.Keywords, 16)
		if merged.UpdatedAt.IsZero() {
			merged.UpdatedAt = time.Now()
		}
		name := "session-" + safeName(projectID)
		if projectID == "" {
			name = safeName(joinedSessionName(merged.SessionID, projectID))
			merged.UpdatedAt = time.Now()
		}
		target := filepath.Join(m.sessionsRoot(), merged.UpdatedAt.Format("2006-01"), name+".json")
		merged.File = m.indexFileFor(target)
		if err := writeJSON(target, &merged); err != nil {
			return moved, err
		}
		for _, item := range list {
			if samePath(item.path, target) {
				continue
			}
			if exists(item.path) {
				if movePath(item.path, filepath.Join(backupDir, filepath.Base(item.path))) == nil {
					moved++
				}
			}
		}
		changed = true
	}
	if changed {
		m.loadDigestsLocked()
		m.refreshIndexLocked()
		m.persistIndexLocked()
	}
	return moved, nil
}

func joinedSessionName(sessionID, projectID string) string {
	if strings.TrimSpace(sessionID) != "" {
		return sessionID
	}
	if projectID != "" {
		return "session-" + projectID
	}
	return "session-" + time.Now().Format("20060102-150405")
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return strings.EqualFold(aa, bb)
}

func mergeUnique(dst, src []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range dst {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range src {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func trimList(list []string, max int) []string {
	if len(list) <= max {
		return list
	}
	return list[len(list)-max:]
}
