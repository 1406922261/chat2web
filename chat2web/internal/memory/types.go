package memory

import (
	"time"

	"chat2web/internal/llm"
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
