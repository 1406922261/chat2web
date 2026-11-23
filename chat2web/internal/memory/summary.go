package memory

import (
	"context"
	"fmt"
	"path/filepath"
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
	if exist != nil && exist.File != "" {
		merged.File = exist.File
	} else {
		abs := m.summaryFile(sessionID)
		rel, err := filepath.Rel(m.root, abs)
		if err != nil {
			rel = filepath.Join("sessions", time.Now().Format("2006-01"), safeName(sessionID)+".md")
		}
		merged.File = filepath.ToSlash(rel)
	}
	if err := writeText(filepath.Join(m.root, filepath.FromSlash(merged.File)), renderDigest(merged)); err != nil {
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
