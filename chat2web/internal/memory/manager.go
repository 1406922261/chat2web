package memory

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"chat2web/internal/llm"
	"chat2web/internal/textutil"
)

// Options 配置临时目录与清理策略。
type Options struct {
	RuntimeRoot    string
	ReportsRoot    string
	TasksRoot      string
	ProjectRoot    string // 统一项目目录根（workspace/projects），项目记忆写入 {id}/memory.md
	PlaywrightDays int
	TaskDays       int
	ReportsKeep    int
	Budget         int // 每次注入 Prompt 的记忆字符预算
}

func (o *Options) normalize() {
	if o.PlaywrightDays <= 0 {
		o.PlaywrightDays = 3
	}
	if o.TaskDays <= 0 {
		o.TaskDays = 7
	}
	if o.ReportsKeep <= 0 {
		o.ReportsKeep = 10
	}
	if o.Budget <= 0 {
		o.Budget = 2400
	}
}

// Manager 负责分层记忆的持久化、增量摘要、检索、迁移与清理。
// 存储结构：
//
//	memory/MEMORY.md              用户长期记忆
//	memory/projects/{id}.md       项目长期记忆（互相隔离）
//	memory/sessions/YYYY-MM/*.md  会话摘要（按月归档）
//	memory/index.json             轻量检索索引（关键词 + 元数据）
//	memory/backup/{ts}/           迁移/清理前的备份，永不自动删除
type Manager struct {
	root string
	llm  *llm.Client
	opts Options

	mu       sync.RWMutex
	entries  []Entry
	manual   string
	projects map[string]*ProjectMemory
	sessions map[string]*Session
	digests  map[string]*SessionDigest
	index    MemoryIndex
}

func NewManager(root string, client *llm.Client) *Manager {
	return &Manager{
		root:     root,
		llm:      client,
		projects: map[string]*ProjectMemory{},
		sessions: map[string]*Session{},
		digests:  map[string]*SessionDigest{},
	}
}

// Configure 设置临时目录与清理参数（可在 Init 前调用）。
func (m *Manager) Configure(opts Options) {
	opts.normalize()
	m.opts = opts
}

func (m *Manager) Options() Options { return m.opts }

func (m *Manager) Root() string { return m.root }

func (m *Manager) Init() error {
	for _, dir := range []string{
		m.root,
		filepath.Join(m.root, "projects"),
		filepath.Join(m.root, "sessions"),
		filepath.Join(m.root, "backup"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.migrateLegacyLocked(); err != nil {
		return err
	}
	m.loadIndexLocked()
	m.loadUserLocked()
	m.loadProjectsLocked()
	m.splitProjectEntriesLocked()
	m.loadDigestsLocked()
	m.refreshIndexLocked()
	m.persistIndexLocked()
	return nil
}

func (m *Manager) Reload() error { return m.Init() }

func (m *Manager) userFile() string  { return filepath.Join(m.root, "MEMORY.md") }
func (m *Manager) indexFile() string { return filepath.Join(m.root, "index.json") }

// projectFile 优先使用项目目录内的 memory.md，使记忆与项目绑定；
// 尚未迁移的项目退回旧的 memory/projects/{id}.md。
func (m *Manager) projectFile(id string) string {
	if m.opts.ProjectRoot != "" {
		dir := filepath.Join(m.opts.ProjectRoot, safeName(id))
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return filepath.Join(dir, "memory.md")
		}
	}
	return filepath.Join(m.root, "projects", safeName(id)+".md")
}

// indexFileFor 返回记忆文件相对记忆根目录的路径，供 index.json 引用。
func (m *Manager) indexFileFor(path string) string {
	rel, err := filepath.Rel(m.root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
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

// splitProjectEntriesLocked 把 MEMORY.md 中带项目 ID 的条目归入各自项目文件，
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
	// 旧位置：memory/projects/*.md
	files, _ := filepath.Glob(filepath.Join(m.root, "projects", "*.md"))
	for _, f := range files {
		load(f)
	}
	// 新位置：workspace/projects/{id}/memory.md，同名时覆盖旧文件。
	if m.opts.ProjectRoot != "" {
		entries, err := os.ReadDir(m.opts.ProjectRoot)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			load(filepath.Join(m.opts.ProjectRoot, entry.Name(), "memory.md"))
		}
	}
}

func (m *Manager) loadDigestsLocked() {
	m.digests = map[string]*SessionDigest{}
	files, _ := filepath.Glob(filepath.Join(m.root, "sessions", "*", "*.md"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		rel, relErr := filepath.Rel(m.root, f)
		if relErr != nil {
			rel = f
		}
		d := parseDigest(string(data), filepath.ToSlash(rel))
		if d.SessionID == "" {
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

func (m *Manager) loadIndexLocked() {
	m.index = MemoryIndex{Version: 1}
	_ = readJSON(m.indexFile(), &m.index)
	if m.index.Entries == nil {
		m.index.Entries = []IndexEntry{}
	}
}

// refreshIndexLocked 从当前内存数据重建轻量索引，保留会话摘要游标。
func (m *Manager) refreshIndexLocked() {
	oldCursor := map[string]int{}
	for _, e := range m.index.Entries {
		if e.SessionID != "" {
			oldCursor[e.SessionID] = e.Cursor
		}
	}
	var entries []IndexEntry
	for _, e := range m.entries {
		entries = append(entries, IndexEntry{
			ID:        "entry:" + e.ID,
			Type:      TypeUser,
			ProjectID: e.ProjectID,
			File:      "MEMORY.md",
			Title:     e.Key,
			Keywords:  limitKeywords(append(append([]string{}, e.Tags...), textutil.KeywordsLimited(e.Key+" "+e.Value, 8)...), 12),
			UpdatedAt: e.UpdatedAt,
		})
	}
	for _, pm := range m.projects {
		file := m.indexFileFor(m.projectFile(pm.ProjectID))
		kw := textutil.KeywordsLimited(pm.Name+" "+pm.Goal+" "+pm.Architecture+" "+pm.Notes, 10)
		kw = append(kw, pm.TechStack...)
		kw = append(kw, textutil.KeywordsLimited(strings.Join(pm.NextSteps, " "), 6)...)
		entries = append(entries, IndexEntry{
			ID:        "project:" + pm.ProjectID,
			Type:      TypeProject,
			ProjectID: pm.ProjectID,
			File:      file,
			Title:     pm.Name,
			Keywords:  limitKeywords(dedupe(kw), 16),
			UpdatedAt: pm.UpdatedAt,
		})
		for _, e := range pm.Entries {
			entries = append(entries, IndexEntry{
				ID:        "entry:" + e.ID,
				Type:      TypeUser,
				ProjectID: pm.ProjectID,
				File:      file,
				Title:     e.Key,
				Keywords:  limitKeywords(append(append([]string{}, e.Tags...), textutil.KeywordsLimited(e.Key+" "+e.Value, 8)...), 12),
				UpdatedAt: e.UpdatedAt,
			})
		}
	}
	for _, d := range m.digests {
		cursor := d.Cursor
		if v, ok := oldCursor[d.SessionID]; ok && v > cursor {
			cursor = v
		}
		title := d.CoreTask
		if strings.TrimSpace(title) == "" {
			title = d.ProjectName
		}
		kw := append([]string{}, d.Keywords...)
		kw = append(kw, textutil.KeywordsLimited(d.Summary+" "+d.CoreTask, 8)...)
		entries = append(entries, IndexEntry{
			ID:        "session:" + d.SessionID,
			Type:      TypeSession,
			ProjectID: d.ProjectID,
			File:      d.File,
			Title:     textutil.Truncate(title, 40),
			Keywords:  limitKeywords(dedupe(kw), 12),
			UpdatedAt: d.UpdatedAt,
			SessionID: d.SessionID,
			Cursor:    cursor,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].UpdatedAt.After(entries[j].UpdatedAt) })
	m.index.Entries = entries
	m.index.Version = 1
	m.index.UpdatedAt = time.Now()
}

func (m *Manager) persistIndexLocked() {
	_ = writeJSON(m.indexFile(), m.index)
}

func (m *Manager) persistUserLocked() {
	_ = writeText(m.userFile(), renderUserMemory(m.entries, m.manual, time.Now()))
}

func (m *Manager) persistProjectLocked(pm *ProjectMemory) {
	pm.UpdatedAt = time.Now()
	_ = writeText(m.projectFile(pm.ProjectID), renderProjectMemory(pm))
}

func (m *Manager) allEntriesLocked() []Entry {
	out := make([]Entry, 0, len(m.entries))
	for _, e := range m.entries {
		if e.ProjectID == "" {
			out = append(out, e)
		}
	}
	for _, pm := range m.projects {
		out = append(out, pm.Entries...)
	}
	return out
}

func (m *Manager) Entries() []Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := m.allEntriesLocked()
	for i := range out {
		if out[i].Tags == nil {
			out[i].Tags = []string{}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// UserEntries 仅返回跨项目的用户长期记忆。
func (m *Manager) UserEntries() []Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Entry, 0, len(m.entries))
	for _, e := range m.entries {
		if e.ProjectID == "" {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func (m *Manager) Profile() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return renderUserMemory(m.entries, m.manual, time.Now())
}

func (m *Manager) SaveProfile(text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	manual := text
	if idx := strings.Index(text, "## 手动备注"); idx >= 0 {
		manual = text[idx+len("## 手动备注"):]
	}
	m.manual = strings.TrimSpace(manual)
	m.persistUserLocked()
	m.refreshIndexLocked()
	m.persistIndexLocked()
	return nil
}

func (m *Manager) UpsertEntry(e Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upsertLocked(e)
}

func (m *Manager) upsertLocked(e Entry) {
	e.Key = strings.TrimSpace(e.Key)
	e.Value = strings.TrimSpace(e.Value)
	if e.Key == "" || e.Value == "" {
		return
	}
	if e.ID == "" {
		e.ID = randomID("m")
	}
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = time.Now()
	}
	if e.Tags == nil {
		e.Tags = []string{}
	}
	if e.Source == "" {
		e.Source = "manual"
	}
	// 项目专属条目写入项目文件，避免进入用户记忆或污染其他项目。
	if e.ProjectID != "" {
		pm := m.projects[e.ProjectID]
		if pm == nil {
			pm = &ProjectMemory{ProjectID: e.ProjectID, Name: e.ProjectID}
			m.projects[e.ProjectID] = pm
		}
		for i := range pm.Entries {
			cur := &pm.Entries[i]
			if cur.Kind == e.Kind && (cur.Key == e.Key || strings.EqualFold(cur.Value, e.Value)) {
				cur.Key = e.Key
				cur.Value = e.Value
				cur.Tags = dedupe(append(cur.Tags, e.Tags...))
				cur.Source = e.Source
				cur.Pinned = cur.Pinned || e.Pinned
				cur.UpdatedAt = e.UpdatedAt
				m.persistProjectLocked(pm)
				m.refreshIndexLocked()
				m.persistIndexLocked()
				return
			}
		}
		pm.Entries = append(pm.Entries, e)
		m.persistProjectLocked(pm)
		m.refreshIndexLocked()
		m.persistIndexLocked()
		return
	}
	for i := range m.entries {
		cur := &m.entries[i]
		if cur.Kind == e.Kind && (cur.Key == e.Key || strings.EqualFold(cur.Value, e.Value)) {
			cur.Key = e.Key
			cur.Value = e.Value
			cur.Tags = dedupe(append(cur.Tags, e.Tags...))
			cur.Source = e.Source
			cur.Pinned = cur.Pinned || e.Pinned
			cur.UpdatedAt = e.UpdatedAt
			m.persistUserLocked()
			m.refreshIndexLocked()
			m.persistIndexLocked()
			return
		}
	}
	m.entries = append(m.entries, e)
	m.persistUserLocked()
	m.refreshIndexLocked()
	m.persistIndexLocked()
}

func (m *Manager) DeleteEntry(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := false
	var kept []Entry
	for _, e := range m.entries {
		if e.ID == id {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	if removed {
		m.entries = kept
		m.persistUserLocked()
	}
	for _, pm := range m.projects {
		var keptProj []Entry
		changed := false
		for _, e := range pm.Entries {
			if e.ID == id {
				removed = true
				changed = true
				continue
			}
			keptProj = append(keptProj, e)
		}
		if changed {
			pm.Entries = keptProj
			m.persistProjectLocked(pm)
		}
	}
	if !removed {
		return fmt.Errorf("记忆条目不存在")
	}
	m.refreshIndexLocked()
	m.persistIndexLocked()
	return nil
}

func (m *Manager) UpdateEntry(id, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.entries {
		if m.entries[i].ID == id {
			m.entries[i].Value = strings.TrimSpace(value)
			m.entries[i].UpdatedAt = time.Now()
			m.entries[i].Source = "manual"
			m.persistUserLocked()
			m.refreshIndexLocked()
			m.persistIndexLocked()
			return nil
		}
	}
	for _, pm := range m.projects {
		for i := range pm.Entries {
			if pm.Entries[i].ID != id {
				continue
			}
			pm.Entries[i].Value = strings.TrimSpace(value)
			pm.Entries[i].UpdatedAt = time.Now()
			pm.Entries[i].Source = "manual"
			m.persistProjectLocked(pm)
			m.refreshIndexLocked()
			m.persistIndexLocked()
			return nil
		}
	}
	return fmt.Errorf("记忆条目不存在")
}

func (m *Manager) Session(id, projectID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s, ok := m.sessions[id]; ok {
		return s
	}
	return &Session{ID: id, ProjectID: projectID}
}

// AppendMessages 只在当前进程内保留原始对话，不写入长期记忆。
func (m *Manager) AppendMessages(sessionID, projectID string, msgs ...llm.Message) {
	if sessionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		s = &Session{ID: sessionID, ProjectID: projectID, CreatedAt: time.Now(), Title: "网站开发会话"}
		m.sessions[sessionID] = s
	}
	if s.ProjectID == "" {
		s.ProjectID = projectID
	}
	s.Messages = append(s.Messages, msgs...)
	if len(s.Messages) > 400 {
		s.Messages = s.Messages[len(s.Messages)-400:]
	}
	s.UpdatedAt = time.Now()
}

func (m *Manager) SessionMessages(sessionID string) []llm.Message {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return nil
	}
	out := make([]llm.Message, len(s.Messages))
	copy(out, s.Messages)
	return out
}

func (m *Manager) Sessions() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// Summaries 兼容旧接口，从会话摘要派生。
func (m *Manager) Summaries() []SummaryMeta {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]SummaryMeta, 0, len(m.digests))
	for _, d := range m.digests {
		out = append(out, SummaryMeta{
			File:      d.File,
			SessionID: d.SessionID,
			ProjectID: d.ProjectID,
			CreatedAt: d.UpdatedAt,
			Preview:   textutil.Compact(textutil.Truncate(d.Summary, 200)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (m *Manager) Digests() []SessionDigest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]SessionDigest, 0, len(m.digests))
	for _, d := range m.digests {
		cp := *d
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func (m *Manager) ProjectMemory(projectID string) *ProjectMemory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pm, ok := m.projects[projectID]
	if !ok {
		return nil
	}
	cp := *pm
	return &cp
}

func (m *Manager) Projects() []*ProjectMemory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*ProjectMemory, 0, len(m.projects))
	for _, pm := range m.projects {
		cp := *pm
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
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

func (m *Manager) ReadMemoryFile(name string) (string, error) {
	clean := filepath.Clean(strings.ReplaceAll(name, "\\", "/"))
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", fmt.Errorf("非法路径")
	}
	allowed := clean == "MEMORY.md" || clean == "index.json" ||
		strings.HasPrefix(clean, "projects/") || strings.HasPrefix(clean, "sessions/") ||
		clean == "user_profile.md" || clean == "long_term.json"
	if !allowed {
		return "", fmt.Errorf("不允许读取该记忆文件")
	}
	data, err := os.ReadFile(filepath.Join(m.root, filepath.FromSlash(clean)))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (m *Manager) UserMemoryText() string { return m.Profile() }

func (m *Manager) ProjectMemoryText(projectID string) (string, error) {
	data, err := os.ReadFile(m.projectFile(projectID))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (m *Manager) readDigestLocked(d *SessionDigest) string {
	if d == nil || d.File == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(m.root, filepath.FromSlash(d.File)))
	if err != nil {
		return ""
	}
	return string(data)
}

func (m *Manager) summaryFile(sessionID string) string {
	return filepath.Join(m.root, "sessions", time.Now().Format("2006-01"), safeName(sessionID)+".md")
}

func safeName(s string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
	out := re.ReplaceAllString(s, "_")
	out = strings.Trim(out, "_.")
	if out == "" {
		out = randomID("x")
	}
	return out
}

func randomID(prefix string) string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return prefix + time.Now().Format("20060102150405") + hex.EncodeToString(buf)
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func dedupe(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range list {
		v = strings.TrimSpace(v)
		if v == "" || seen[strings.ToLower(v)] {
			continue
		}
		seen[strings.ToLower(v)] = true
		out = append(out, v)
	}
	return out
}
