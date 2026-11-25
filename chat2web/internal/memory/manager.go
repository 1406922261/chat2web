package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"chat2web/internal/llm"
	"chat2web/internal/session"
	"chat2web/internal/textutil"
)

// 记忆类型常量，与 index.json 中的 type 字段一致。
const (
	TypeUser    = "user"
	TypeProject = "project"
	TypeSession = "session"
)

// Entry 是一条可检索的长期记忆条目。
type Entry struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // preference | tech | fact | project
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Tags      []string  `json:"tags"`
	ProjectID string    `json:"project_id,omitempty"`
	Source    string    `json:"source"` // llm | heuristic | manual
	Pinned    bool      `json:"pinned,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type HistoryItem struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Summary string    `json:"summary"`
}

// ProjectMemory 是单个项目的长期记忆，项目之间相互隔离。
type ProjectMemory struct {
	ProjectID    string        `json:"project_id"`
	Name         string        `json:"name"`
	Goal         string        `json:"goal"`
	Description  string        `json:"description"`
	TechStack    []string      `json:"tech_stack"`
	Architecture string        `json:"architecture"`
	Pages        []string      `json:"pages"`
	Status       string        `json:"status"`
	Notes        string        `json:"notes"`
	Completed    []string      `json:"completed"`
	Decisions    []HistoryItem `json:"decisions"`
	OpenIssues   []string      `json:"open_issues"`
	NextSteps    []string      `json:"next_steps"`
	History      []HistoryItem `json:"history"`
	Entries      []Entry       `json:"entries"` // 项目专属记忆条目，与其他项目隔离
	UpdatedAt    time.Time     `json:"updated_at"`
}

// Session 仅在当前进程内保存原始对话，不再持久化每一轮消息。
type Session struct {
	ID        string        `json:"id"`
	ProjectID string        `json:"project_id"`
	Title     string        `json:"title"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Messages  []llm.Message `json:"messages"`
}

// SessionDigest 是持久化的会话摘要（按月归档）。
type SessionDigest struct {
	SessionID   string    `json:"session_id"`
	ProjectID   string    `json:"project_id"`
	ProjectName string    `json:"project_name"`
	UpdatedAt   time.Time `json:"updated_at"`
	Cursor      int       `json:"cursor"` // 已摘要的消息条数，用于增量更新
	Keywords    []string  `json:"keywords"`
	CoreTask    string    `json:"core_task"`
	Completed   []string  `json:"completed"`
	Conclusions []string  `json:"conclusions"`
	OpenIssues  []string  `json:"open_issues"`
	NextSteps   []string  `json:"next_steps"`
	Summary     string    `json:"summary"`
	File        string    `json:"file"`
}

// IndexEntry 是 index.json 中的轻量检索元数据，不包含记忆正文。
type IndexEntry struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	ProjectID string    `json:"project_id,omitempty"`
	File      string    `json:"file"`
	Title     string    `json:"title"`
	Keywords  []string  `json:"keywords"`
	UpdatedAt time.Time `json:"updated_at"`
	SessionID string    `json:"session_id,omitempty"`
	Cursor    int       `json:"cursor,omitempty"`
}

type MemoryIndex struct {
	Version   int          `json:"version"`
	UpdatedAt time.Time    `json:"updated_at"`
	Entries   []IndexEntry `json:"entries"`
}

// SummaryMeta 为兼容旧调用保留，从会话摘要派生。
type SummaryMeta struct {
	File      string    `json:"file"`
	SessionID string    `json:"session_id"`
	ProjectID string    `json:"project_id"`
	CreatedAt time.Time `json:"created_at"`
	Preview   string    `json:"preview"`
}

// SummaryResult 是增量摘要的结构化结果。
type SummaryResult struct {
	Summary     string       `json:"summary"`
	CoreTask    string       `json:"core_task"`
	Completed   []string     `json:"completed"`
	Conclusions []string     `json:"conclusions"`
	OpenIssues  []string     `json:"open_issues"`
	NextSteps   []string     `json:"next_steps"`
	Preferences []Preference `json:"preferences"`
	TechStack   []string     `json:"tech_stack"`
	Project     ProjectState `json:"project"`
	Source      string       `json:"source"`
	File        string       `json:"file,omitempty"`
}

type Preference struct {
	Key   string   `json:"key"`
	Value string   `json:"value"`
	Tags  []string `json:"tags"`
}

type ProjectState struct {
	Status string   `json:"status"`
	Pages  []string `json:"pages"`
	Notes  string   `json:"notes"`
}

// SearchResult 是记忆检索结果，附带正文片段。
type SearchResult struct {
	IndexEntry
	Snippet string `json:"snippet"`
}

// DirUsage 描述某个临时目录的占用情况。
type DirUsage struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// CleanupReport 是一次自动清理的结果。
type CleanupReport struct {
	DeletedFiles      int      `json:"deleted_files"`
	DeletedBytes      int64    `json:"deleted_bytes"`
	ArchivedFiles     int      `json:"archived_files"`
	ArchivedBytes     int64    `json:"archived_bytes"`
	SkippedReferenced []string `json:"skipped_referenced,omitempty"`
	Details           []string `json:"details,omitempty"`
}

// MemoryPatch 是更新记忆时的可选字段。
type MemoryPatch struct {
	Key   string
	Value string
	Tags  []string
}

// Options 配置临时目录与清理策略。
type Options struct {
	DataRoot       string // data 根目录，索引中的相对路径以此为准
	SessionsRoot   string // data/sessions，会话摘要按月归档
	BackupsRoot    string // data/backups，迁移与清理备份
	RuntimeRoot    string
	ReportsRoot    string
	TasksRoot      string
	ProjectRoot    string // 统一项目目录根 data/projects，项目记忆写入 {id}/memory.md
	PlaywrightDays int
	TaskDays       int
	SessionDays    int // 无项目关联的临时会话保留天数
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
	if o.SessionDays <= 0 {
		o.SessionDays = 7
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
//	data/memory/memory.md             全局长期记忆（跨项目偏好）
//	data/memory/index.json            轻量检索索引（关键词 + 元数据）
//	data/projects/{id}/memory.md      项目长期记忆（跟随项目目录）
//	data/sessions/YYYY-MM/*.json      会话摘要（按月归档，兼容旧 .md 迁移）
//	data/backups/{ts}/                迁移/清理前的备份，永不自动删除
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
	layout   LayoutReport
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

// dataRoot 返回 data 根目录；索引中的文件路径都以它为基准。
func (m *Manager) dataRoot() string {
	if m.opts.DataRoot != "" {
		return m.opts.DataRoot
	}
	return filepath.Dir(m.root)
}

func (m *Manager) sessionsRoot() string {
	if m.opts.SessionsRoot != "" {
		return m.opts.SessionsRoot
	}
	return filepath.Join(m.dataRoot(), "sessions")
}

func (m *Manager) backupsRoot() string {
	if m.opts.BackupsRoot != "" {
		return m.opts.BackupsRoot
	}
	return filepath.Join(m.dataRoot(), "backups")
}

// projectsRoot 返回唯一的项目根目录 data/projects。
func (m *Manager) projectsRoot() string {
	if m.opts.ProjectRoot != "" {
		return m.opts.ProjectRoot
	}
	return filepath.Join(m.dataRoot(), "projects")
}

func (m *Manager) store() *session.Store { return session.NewStore(m.sessionsRoot()) }

// SessionsRoot 返回会话摘要目录（data/sessions）。
func (m *Manager) SessionsRoot() string { return m.sessionsRoot() }

// LastLayoutReport 返回最近一次 data 目录布局迁移结果。
func (m *Manager) LastLayoutReport() LayoutReport {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.layout
}

func (m *Manager) Init() error {
	for _, dir := range []string{
		m.root,
		m.sessionsRoot(),
		m.backupsRoot(),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 先转换旧 JSON/摘要格式（写入 data/sessions），再迁移旧目录布局（data/memory → data/sessions|backups）。
	if err := m.migrateLegacyLocked(); err != nil {
		return err
	}
	m.layout = m.migrateLayoutLocked()
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

func (m *Manager) userFile() string  { return filepath.Join(m.root, "memory.md") }
func (m *Manager) indexFile() string { return filepath.Join(m.root, "index.json") }

// projectFile 固定为项目目录内的 memory.md（data/projects/{id}/memory.md），
// 不再保留 data/memory/projects 这一套重复存储。
func (m *Manager) projectFile(id string) string {
	return filepath.Join(m.projectsRoot(), safeName(id), "memory.md")
}

// indexFileFor 返回记忆文件相对 data 根目录的路径，供 index.json 引用。
func (m *Manager) indexFileFor(path string) string {
	rel, err := filepath.Rel(m.dataRoot(), path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
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
			File:      m.indexFileFor(m.userFile()),
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

// SaveMemory 保存一条长期记忆；相同 kind/key/value 会自动合并，避免重复。
func (m *Manager) SaveMemory(e Entry) (Entry, error) {
	e.Key = strings.TrimSpace(e.Key)
	e.Value = strings.TrimSpace(e.Value)
	if e.Kind == "" {
		e.Kind = "fact"
	}
	if e.Key == "" || e.Value == "" {
		return Entry{}, fmt.Errorf("记忆的 key 和 value 不能为空")
	}
	m.mu.Lock()
	m.upsertLocked(e)
	var saved Entry
	for _, cur := range m.allEntriesLocked() {
		if cur.Kind == e.Kind && cur.ProjectID == e.ProjectID && cur.Key == e.Key {
			saved = cur
			break
		}
	}
	m.mu.Unlock()
	if saved.ID == "" {
		return Entry{}, fmt.Errorf("保存记忆失败")
	}
	return saved, nil
}

// UpdateMemory 按 ID 更新用户长期记忆。
func (m *Manager) UpdateMemory(id string, patch MemoryPatch) error {
	id = strings.TrimPrefix(strings.TrimSpace(id), "entry:")
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.entries {
		if m.entries[i].ID != id {
			continue
		}
		if key := strings.TrimSpace(patch.Key); key != "" {
			m.entries[i].Key = key
		}
		if value := strings.TrimSpace(patch.Value); value != "" {
			m.entries[i].Value = value
		}
		if patch.Tags != nil {
			m.entries[i].Tags = dedupe(patch.Tags)
		}
		m.entries[i].Source = "manual"
		m.entries[i].UpdatedAt = time.Now()
		m.persistUserLocked()
		m.refreshIndexLocked()
		m.persistIndexLocked()
		return nil
	}
	return fmt.Errorf("记忆条目不存在")
}

// DeleteMemory 删除指定用户长期记忆。
func (m *Manager) DeleteMemory(id string) error {
	id = strings.TrimPrefix(strings.TrimSpace(id), "entry:")
	return m.DeleteEntry(id)
}

// GetProjectMemory 获取项目记忆；不存在时返回错误，便于界面提示。
func (m *Manager) GetProjectMemory(projectID string) (*ProjectMemory, error) {
	pm := m.ProjectMemory(projectID)
	if pm == nil {
		return nil, fmt.Errorf("项目记忆不存在：%s", projectID)
	}
	return pm, nil
}

// RebuildIndex 重建轻量检索索引，用于清理后同步关键词与文件位置。
func (m *Manager) RebuildIndex() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshIndexLocked()
	m.persistIndexLocked()
}

// MemoryBackupRoot 返回备份目录，删除操作必须可恢复。
func (m *Manager) MemoryBackupRoot(tag string) string {
	return filepath.Join(m.backupsRoot(), tag)
}

// BackupFile 把记忆文件复制到备份目录。
func (m *Manager) BackupFile(path, backupDir string) error {
	if !exists(path) {
		return nil
	}
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}
	return movePath(path, filepath.Join(backupDir, filepath.Base(path)))
}

// CleanupMemory 清理过期的临时数据：
//   - Playwright 日志：保留最近 3 天
//   - 临时任务/Shell 结果：保留 7 天
//   - 临时报告：保留最近 10 份
//   - 长期记忆与项目产物：永不过期，绝不进入清理范围
//
// 清理前会检查文件是否仍被任务或项目引用，命中则跳过。
func (m *Manager) CleanupMemory() CleanupReport {
	rep := CleanupReport{}
	referenced := m.referencedIDs()
	now := time.Now()
	rt := m.opts.RuntimeRoot

	if rt != "" {
		m.cleanOlderThan(filepath.Join(rt, "playwright"), now.AddDate(0, 0, -m.opts.PlaywrightDays), &rep, referenced)
		m.cleanOlderThan(filepath.Join(rt, "shell"), now.AddDate(0, 0, -m.opts.TaskDays), &rep, referenced)
		m.cleanOlderThan(filepath.Join(rt, "tasks-tmp"), now.AddDate(0, 0, -m.opts.TaskDays), &rep, referenced)
		m.cleanOlderThan(filepath.Join(rt, "screenshots"), now.AddDate(0, 0, -m.opts.PlaywrightDays), &rep, referenced)
		m.cleanOlderThan(filepath.Join(rt, "tmp"), now.AddDate(0, 0, -m.opts.TaskDays), &rep, referenced)
		m.cleanOldReports(filepath.Join(rt, "reports-tmp"), m.opts.ReportsKeep, true, &rep, referenced)
	}
	if m.opts.TasksRoot != "" {
		m.cleanTasks(m.opts.TasksRoot, now.AddDate(0, 0, -m.opts.TaskDays), &rep)
	}
	if m.opts.ReportsRoot != "" {
		m.cleanOldReports(m.opts.ReportsRoot, m.opts.ReportsKeep, false, &rep, referenced)
	}
	m.cleanInactiveSessions(now.AddDate(0, 0, -m.opts.SessionDays), &rep)
	return rep
}

// cleanInactiveSessions 归档长期未活跃的临时会话摘要。
// 关联了项目的会话始终保留；清理结果移入 backup，不直接删除。
func (m *Manager) cleanInactiveSessions(cutoff time.Time, rep *CleanupReport) {
	archiveDir := filepath.Join(m.backupsRoot(), "sessions-"+time.Now().Format("20060102-150405"))
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for id, d := range m.digests {
		if strings.TrimSpace(d.ProjectID) != "" || !d.UpdatedAt.Before(cutoff) {
			continue
		}
		if d.File != "" {
			src := filepath.Join(m.dataRoot(), filepath.FromSlash(d.File))
			if info, err := os.Stat(src); err == nil {
				if movePath(src, filepath.Join(archiveDir, filepath.Base(d.File))) == nil {
					rep.ArchivedFiles++
					rep.ArchivedBytes += info.Size()
					rep.Details = append(rep.Details, "归档不活跃临时会话："+filepath.Base(d.File))
				}
			}
		}
		delete(m.digests, id)
		changed = true
	}
	for id, s := range m.sessions {
		if strings.TrimSpace(s.ProjectID) == "" && s.UpdatedAt.Before(cutoff) {
			delete(m.sessions, id)
			changed = true
		}
	}
	if changed {
		m.refreshIndexLocked()
		m.persistIndexLocked()
	}
}

// CleanupTaskScreenshots 清理本次任务生成的浏览器调试截图。
// 仅处理 runtime 目录下的图片；项目 assets 与 output 中的用户素材/成果不受影响。
func (m *Manager) CleanupTaskScreenshots(since time.Time) CleanupReport {
	var rep CleanupReport
	if m.opts.RuntimeRoot == "" {
		return rep
	}
	dirs := []string{
		filepath.Join(m.opts.RuntimeRoot, "screenshots"),
		filepath.Join(m.opts.RuntimeRoot, "playwright"),
	}
	for _, dir := range dirs {
		if !exists(dir) {
			continue
		}
		_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry == nil || entry.IsDir() || !isImageFile(entry.Name()) {
				return nil
			}
			info, statErr := entry.Info()
			if statErr != nil || info.ModTime().Before(since) {
				return nil
			}
			if os.Remove(path) == nil {
				rep.DeletedFiles++
				rep.DeletedBytes += info.Size()
			}
			return nil
		})
	}
	return rep
}

func isImageFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp":
		return true
	}
	return false
}

// cleanTasks 清理已完成且超过保留期的任务状态；执行中的任务始终保留。
func (m *Manager) cleanTasks(dir string, cutoff time.Time, rep *CleanupReport) {
	if !exists(dir) {
		return
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		var st struct {
			Status    string    `json:"status"`
			UpdatedAt time.Time `json:"updated_at"`
		}
		if readJSON(f, &st) != nil || strings.EqualFold(st.Status, "running") {
			continue
		}
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		when := st.UpdatedAt
		if when.IsZero() {
			when = info.ModTime()
		}
		if !when.Before(cutoff) {
			continue
		}
		if os.Remove(f) == nil {
			rep.DeletedFiles++
			rep.DeletedBytes += info.Size()
		}
	}
}

// referencedIDs 收集仍在执行中的任务 ID；已完成/已暂停的任务不再阻止清理。
func (m *Manager) referencedIDs() []string {
	ids := map[string]bool{}
	if strings.TrimSpace(m.opts.TasksRoot) != "" {
		files, _ := filepath.Glob(filepath.Join(m.opts.TasksRoot, "*.json"))
		for _, f := range files {
			var st struct {
				ProjectID string `json:"project_id"`
				Status    string `json:"status"`
			}
			if readJSON(f, &st) == nil && strings.EqualFold(st.Status, "running") {
				id := st.ProjectID
				if id == "" {
					id = strings.TrimSuffix(filepath.Base(f), ".json")
				}
				ids[id] = true
			}
		}
	}
	var out []string
	for id := range ids {
		if len(id) >= 8 {
			out = append(out, id)
		}
	}
	return out
}

func isReferenced(path string, ids []string) bool {
	base := filepath.Base(path)
	for _, id := range ids {
		if strings.Contains(base, id) {
			return true
		}
	}
	return false
}

func (m *Manager) cleanOlderThan(dir string, cutoff time.Time, rep *CleanupReport, referenced []string) {
	if !exists(dir) {
		return
	}
	var emptyDirs []string
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil {
			return nil
		}
		if entry.IsDir() {
			emptyDirs = append(emptyDirs, path)
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil || !info.ModTime().Before(cutoff) {
			return nil
		}
		if isReferenced(path, referenced) {
			rep.SkippedReferenced = append(rep.SkippedReferenced, path)
			return nil
		}
		if os.Remove(path) == nil {
			rep.DeletedFiles++
			rep.DeletedBytes += info.Size()
		}
		return nil
	})
	// 后序删除空目录（保留根目录）。
	for i := len(emptyDirs) - 1; i >= 0; i-- {
		if emptyDirs[i] == dir {
			continue
		}
		_ = os.Remove(emptyDirs[i])
	}
}

func (m *Manager) cleanOldReports(dir string, keep int, temp bool, rep *CleanupReport, referenced []string) {
	if !exists(dir) || keep <= 0 {
		return
	}
	type fileInfo struct {
		path string
		mod  time.Time
		size int64
	}
	var files []fileInfo
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		files = append(files, fileInfo{path: path, mod: info.ModTime(), size: info.Size()})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	if len(files) <= keep {
		return
	}
	archiveDir := filepath.Join(m.backupsRoot(), "reports-"+time.Now().Format("20060102-150405"))
	for _, f := range files[keep:] {
		if isReferenced(f.path, referenced) {
			rep.SkippedReferenced = append(rep.SkippedReferenced, f.path)
			continue
		}
		if temp {
			if os.Remove(f.path) == nil {
				rep.DeletedFiles++
				rep.DeletedBytes += f.size
			}
			continue
		}
		if movePath(f.path, filepath.Join(archiveDir, filepath.Base(f.path))) == nil {
			rep.ArchivedFiles++
			rep.ArchivedBytes += f.size
			rep.Details = append(rep.Details, "已归档旧报告："+filepath.Base(f.path))
		}
	}
}

// RuntimeUsage 统计临时目录占用，供记忆管理界面展示。
func (m *Manager) RuntimeUsage() []DirUsage {
	var dirs []string
	if m.opts.RuntimeRoot != "" {
		dirs = append(dirs,
			filepath.Join(m.opts.RuntimeRoot, "playwright"),
			filepath.Join(m.opts.RuntimeRoot, "shell"),
			filepath.Join(m.opts.RuntimeRoot, "screenshots"),
			filepath.Join(m.opts.RuntimeRoot, "tmp"),
			filepath.Join(m.opts.RuntimeRoot, "reports-tmp"),
			filepath.Join(m.opts.RuntimeRoot, "tasks-tmp"),
		)
	}
	if m.opts.TasksRoot != "" {
		dirs = append(dirs, m.opts.TasksRoot)
	}
	if m.opts.ReportsRoot != "" {
		dirs = append(dirs, m.opts.ReportsRoot)
	}
	out := make([]DirUsage, 0, len(dirs))
	for _, dir := range dirs {
		usage := DirUsage{Path: dir}
		_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry == nil || entry.IsDir() {
				return nil
			}
			if info, statErr := entry.Info(); statErr == nil {
				usage.Files++
				usage.Bytes += info.Size()
			}
			return nil
		})
		out = append(out, usage)
	}
	return out
}

// LayoutReport 是一次 data 目录布局迁移的结果。
//
// 旧布局把会话摘要与备份放在 data/memory 下；新布局统一为：
//
//	data/memory     全局记忆与索引
//	data/sessions   会话摘要（按月归档）
//	data/backups    迁移与清理备份
type LayoutReport struct {
	BackupDir      string
	MovedSessions  int
	MovedBackups   int
	MovedSummaries int
	MovedReports   int
	MovedRuntime   int
	MovedTasks     int
	Conflicts      int
	RemovedDirs    []string
	Details        []string
	Errors         []string
}

// Empty 表示没有任何需要迁移的旧数据。
func (r LayoutReport) Empty() bool {
	return r.MovedSessions == 0 && r.MovedBackups == 0 && r.MovedSummaries == 0 &&
		r.MovedReports == 0 && r.MovedRuntime == 0 && r.MovedTasks == 0 &&
		r.Conflicts == 0 && len(r.RemovedDirs) == 0 && len(r.Errors) == 0
}

// migrateLayoutLocked 把 data/memory 下的旧目录安全迁移到 data/sessions 与 data/backups。
// 迁移原则：
//   - 只移动文件，冲突时保留目标文件、旧副本移入 layout-{ts}/，绝不覆盖；
//   - 迁移后校验：旧目录只要还有文件就跳过清理，不做递归删除；
//   - 仅在旧目录确认为空时移除该目录。
func (m *Manager) migrateLayoutLocked() LayoutReport {
	rep := LayoutReport{
		BackupDir: filepath.Join(m.backupsRoot(), "layout-"+time.Now().Format("20060102-150405")),
	}
	legacySessions := filepath.Join(m.root, "sessions")
	legacyBackup := filepath.Join(m.root, "backup")
	legacySummaries := filepath.Join(m.root, "summaries")

	if !sameDir(legacySessions, m.sessionsRoot()) {
		rep.MovedSessions = m.mergeTree(legacySessions, m.sessionsRoot(), rep.BackupDir, &rep, "sessions")
	}
	if !sameDir(legacyBackup, m.backupsRoot()) {
		rep.MovedBackups = m.mergeTree(legacyBackup, m.backupsRoot(), rep.BackupDir, &rep, "backups")
	}
	// 旧 summaries 目录不再参与检索，整体归档到 data/backups/legacy-summaries。
	if exists(legacySummaries) {
		target := filepath.Join(m.backupsRoot(), "legacy-summaries")
		rep.MovedSummaries = m.mergeTree(legacySummaries, target, rep.BackupDir, &rep, "summaries")
	}

	// data 顶层的旧临时目录 → 新位置：reports→exports/reports，runtime→logs/runtime，
	// tasks→sessions/tasks。迁移后顶层只保留 memory/projects/sessions/logs/exports/backups。
	tmpMoves := []struct {
		from string
		to   string
		tag  string
		dst  *int
	}{
		{filepath.Join(m.dataRoot(), "reports"), m.opts.ReportsRoot, "reports", &rep.MovedReports},
		{filepath.Join(m.dataRoot(), "runtime"), m.opts.RuntimeRoot, "runtime", &rep.MovedRuntime},
		{filepath.Join(m.dataRoot(), "tasks"), m.opts.TasksRoot, "tasks", &rep.MovedTasks},
	}
	legacyTmp := make([]string, 0, len(tmpMoves))
	for _, mv := range tmpMoves {
		if mv.to == "" || sameDir(mv.from, mv.to) {
			continue
		}
		legacyTmp = append(legacyTmp, mv.from)
		*mv.dst = m.mergeTree(mv.from, mv.to, rep.BackupDir, &rep, mv.tag)
	}

	cleanupDirs := append([]string{legacySessions, legacyBackup, legacySummaries}, legacyTmp...)
	for _, dir := range cleanupDirs {
		if !exists(dir) {
			continue
		}
		if left := countFiles(dir); left > 0 {
			rep.Errors = append(rep.Errors, filepath.ToSlash(dir)+": 仍有 "+itoa(left)+" 个文件未迁移，已保留原目录")
			continue
		}
		if removeEmptyTree(dir) {
			rep.RemovedDirs = append(rep.RemovedDirs, dir)
		}
	}
	return rep
}

// mergeTree 把 src 下的内容合并到 dst，同名冲突保留目标文件并把旧副本移入备份。
// 目标不存在时优先整目录/整文件移动，避免逐文件搬迁大量历史备份。
func (m *Manager) mergeTree(src, dst, backupDir string, rep *LayoutReport, tag string) int {
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		return 0
	}
	moved := 0
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0
	}
	for _, entry := range entries {
		moved += m.mergePath(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name()),
			backupDir, tag, entry.Name(), rep)
	}
	return moved
}

func (m *Manager) mergePath(src, dst, backupDir, tag, rel string, rep *LayoutReport) int {
	fi, err := os.Stat(src)
	if err != nil {
		return 0
	}
	if !exists(dst) {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			rep.Errors = append(rep.Errors, filepath.ToSlash(src)+": "+err.Error())
			return 0
		}
		if err := movePath(src, dst); err != nil {
			rep.Errors = append(rep.Errors, filepath.ToSlash(src)+": "+err.Error())
			return 0
		}
		if fi.IsDir() {
			return countFiles(dst)
		}
		return 1
	}
	if !fi.IsDir() {
		rep.Conflicts++
		// 同大小视为重复副本，直接归档；不同大小保留较新的目标文件。
		backup := filepath.Join(backupDir, "conflicts", tag, rel)
		if ti, statErr := os.Stat(dst); statErr == nil && ti.Size() == fi.Size() {
			backup = filepath.Join(backupDir, "duplicates", tag, rel)
		}
		if err := movePath(src, backup); err != nil {
			rep.Errors = append(rep.Errors, filepath.ToSlash(src)+": "+err.Error())
			return 0
		}
		rep.Details = append(rep.Details, "同名文件已备份："+filepath.ToSlash(rel))
		return 0
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0
	}
	moved := 0
	for _, entry := range entries {
		moved += m.mergePath(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name()),
			backupDir, tag, filepath.Join(rel, entry.Name()), rep)
	}
	// 子目录内容已全部迁出时删除该空目录；非空目录保留。
	_ = os.Remove(src)
	return moved
}

func countFiles(root string) int {
	n := 0
	_ = filepath.Walk(root, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi != nil && !fi.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// removeEmptyTree 自底向上删除空目录；遇到非空目录立即停止，保证不丢数据。
func removeEmptyTree(root string) bool {
	var dirs []string
	_ = filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err == nil && fi != nil && fi.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Remove(dirs[i]); err != nil {
			return false
		}
	}
	return true
}

func sameDir(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return strings.EqualFold(aa, bb)
}

type legacyLongTerm struct {
	Entries []Entry `json:"entries"`
}

type legacyProject struct {
	ProjectID   string        `json:"project_id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	TechStack   []string      `json:"tech_stack"`
	Pages       []string      `json:"pages"`
	Status      string        `json:"status"`
	Notes       string        `json:"notes"`
	History     []HistoryItem `json:"history"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

type legacySession struct {
	ID        string        `json:"id"`
	ProjectID string        `json:"project_id"`
	Title     string        `json:"title"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Messages  []llm.Message `json:"messages"`
}

// migrateLegacyLocked 把旧版 JSON/原始会话迁移到分层 Markdown 结构。
// 原则：先写新文件，成功后再把旧文件移动到 backups/{ts}/，绝不直接删除。
func (m *Manager) migrateLegacyLocked() error {
	ts := time.Now().Format("20060102-150405")
	backupRoot := filepath.Join(m.backupsRoot(), ts)
	moveToBackup := func(p string) {
		if !exists(p) {
			return
		}
		rel, err := filepath.Rel(m.root, p)
		if err != nil {
			rel = filepath.Base(p)
		}
		_ = movePath(p, filepath.Join(backupRoot, rel))
	}

	// 1) 用户长期记忆：long_term.json + user_profile.md → memory.md
	userPath := m.userFile()
	// 旧版全局记忆文件名 MEMORY.md → memory.md；已存在新文件时旧文件只做备份。
	// Windows 文件系统不区分大小写，必须按真实目录项名称精确匹配。
	if legacyUser := existingExactName(m.root, "MEMORY.md"); legacyUser != "" {
		if existingExactName(m.root, "memory.md") == "" {
			_ = movePath(legacyUser, userPath)
		} else {
			moveToBackup(legacyUser)
		}
	}
	legacyLong := filepath.Join(m.root, "long_term.json")
	legacyProfile := filepath.Join(m.root, "user_profile.md")
	if !exists(userPath) && (exists(legacyLong) || exists(legacyProfile)) {
		var lt legacyLongTerm
		_ = readJSON(legacyLong, &lt)
		manual := ""
		if data, err := os.ReadFile(legacyProfile); err == nil {
			manual = extractManualNotes(string(data))
		}
		now := time.Now()
		for i := range lt.Entries {
			if lt.Entries[i].ID == "" {
				lt.Entries[i].ID = randomID("m")
			}
			if lt.Entries[i].Source == "" {
				lt.Entries[i].Source = "migrated"
			}
			if lt.Entries[i].UpdatedAt.IsZero() {
				lt.Entries[i].UpdatedAt = now
			}
		}
		if err := writeText(userPath, renderUserMemory(lt.Entries, manual, now)); err == nil {
			moveToBackup(legacyLong)
			moveToBackup(legacyProfile)
		}
	}

	// 2) 项目记忆：projects/*.json → 项目目录 memory.md（或旧位置 projects/{id}.md）
	files, _ := filepath.Glob(filepath.Join(m.root, "projects", "*.json"))
	for _, f := range files {
		var lp legacyProject
		if err := readJSON(f, &lp); err != nil || strings.TrimSpace(lp.ProjectID) == "" {
			continue
		}
		// 配置了统一项目根时，只回填真实存在的项目目录，避免凭空创建项目。
		if m.opts.ProjectRoot != "" {
			dir := filepath.Join(m.opts.ProjectRoot, safeName(lp.ProjectID))
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				moveToBackup(f)
				continue
			}
		}
		target := m.projectFile(lp.ProjectID)
		if !exists(target) {
			pm := &ProjectMemory{
				ProjectID:   lp.ProjectID,
				Name:        lp.Name,
				Goal:        lp.Description,
				Description: lp.Description,
				TechStack:   lp.TechStack,
				Pages:       lp.Pages,
				Status:      lp.Status,
				Notes:       lp.Notes,
				History:     lp.History,
				UpdatedAt:   lp.UpdatedAt,
			}
			if pm.UpdatedAt.IsZero() {
				pm.UpdatedAt = time.Now()
			}
			if err := writeText(target, renderProjectMemory(pm)); err != nil {
				continue
			}
		}
		moveToBackup(f)
	}

	// 3) 会话：sessions/*.json → data/sessions/{month}/{id}.json（本地启发式摘要）
	// 兼容两种来源：新会话目录与尚未迁移的 data/memory/sessions。
	// 只处理根目录下含 messages 的原始对话转录，不能把按月归档的摘要 JSON 当作转录。
	for _, dir := range []string{m.sessionsRoot(), filepath.Join(m.root, "sessions")} {
		files, _ = filepath.Glob(filepath.Join(dir, "*.json"))
		for _, f := range files {
			var ls legacySession
			if err := readJSON(f, &ls); err != nil || strings.TrimSpace(ls.ID) == "" || ls.Messages == nil {
				continue
			}
			when := ls.UpdatedAt
			if when.IsZero() {
				when = time.Now()
			}
			target := m.store().DigestPath(ls.ID, when)
			if !exists(target) {
				result := heuristicDigest(ls.Messages)
				d := mergeDigest(nil, result, len(ls.Messages), ls.ProjectID, ls.Title)
				rel, _ := filepath.Rel(m.dataRoot(), target)
				d.File = filepath.ToSlash(rel)
				d.UpdatedAt = when
				if err := writeJSON(target, d); err != nil {
					continue
				}
			}
			moveToBackup(f)
		}
	}

	// 4) 旧摘要：summaries/*.md → data/sessions/{month}/legacy-*.md
	files, _ = filepath.Glob(filepath.Join(m.root, "summaries", "*.md"))
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		target := filepath.Join(m.sessionsRoot(), info.ModTime().Format("2006-01"), "legacy-"+filepath.Base(f))
		if !exists(target) {
			data, readErr := os.ReadFile(f)
			if readErr != nil {
				continue
			}
			if err := writeText(target, string(data)); err != nil {
				continue
			}
		}
		moveToBackup(f)
	}

	// 5) Playwright 调试日志：根目录与 cmd/ 下的 .playwright-mcp → runtime/playwright
	m.migratePlaywrightLogs(ts)
	// 6) 散落在数据目录根部的脚本/测试日志 → runtime/shell，交由 7 天保留策略清理
	m.migrateLooseLogs(ts)
	return nil
}

func (m *Manager) migrateLooseLogs(ts string) {
	if strings.TrimSpace(m.opts.RuntimeRoot) == "" {
		return
	}
	dataRoot := m.dataRoot()
	files, _ := filepath.Glob(filepath.Join(dataRoot, "*.log"))
	for _, f := range files {
		target := filepath.Join(m.opts.RuntimeRoot, "shell", "legacy-"+ts+"-"+filepath.Base(f))
		_ = movePath(f, target)
	}
}

// existingExactName 按真实文件名（区分大小写）查找目录项，避免 Windows
// 大小写不敏感导致 memory.md 被误判为旧版 MEMORY.md。
func existingExactName(dir, name string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() && entry.Name() == name {
			return filepath.Join(dir, entry.Name())
		}
	}
	return ""
}

func extractManualNotes(content string) string {
	idx := strings.Index(content, "## 手动备注")
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(content[idx+len("## 手动备注"):])
}

func (m *Manager) migratePlaywrightLogs(ts string) {
	if strings.TrimSpace(m.opts.RuntimeRoot) == "" {
		return
	}
	dataRoot := m.dataRoot()
	projectRoot := dataRoot
	if strings.EqualFold(filepath.Base(dataRoot), "data") {
		projectRoot = filepath.Dir(dataRoot)
	}
	var candidates []string
	for _, dir := range []string{
		filepath.Join(projectRoot, ".playwright-mcp"),
		filepath.Join(projectRoot, "cmd", ".playwright-mcp"),
		filepath.Join(dataRoot, ".playwright-mcp"),
	} {
		if exists(dir) {
			candidates = append(candidates, dir)
		}
	}
	for i, dir := range candidates {
		target := filepath.Join(m.opts.RuntimeRoot, "playwright", "legacy-"+ts+"-"+itoa(i))
		if err := movePath(dir, target); err != nil {
			continue
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}

func readJSON(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return os.ErrNotExist
	}
	return json.Unmarshal(data, out)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeText(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// limitKeywords 截断索引关键词数量，控制 index.json 体积。
func limitKeywords(words []string, max int) []string {
	if max <= 0 {
		max = 12
	}
	if len(words) > max {
		return words[:max]
	}
	return words
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyTree(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// movePath 优先重命名，跨目录失败时退化为复制后删除，保证不丢数据。
func movePath(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyTree(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
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
