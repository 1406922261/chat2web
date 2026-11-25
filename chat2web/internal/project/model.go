package project

import (
	"fmt"
	"path/filepath"
	"time"
)

type HistoryItem struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Summary string    `json:"summary"`
}

// 项目生命周期：
//   - temporary：临时生成，默认状态，不进入正式项目列表
//   - active：用户正在继续修改
//   - saved：用户明确保存，永久保留
//   - archived：用户主动归档，保留源码与记忆但不出现在活动列表
const (
	LifecycleTemporary = "temporary"
	LifecycleActive    = "active"
	LifecycleSaved     = "saved"
	LifecycleArchived  = "archived"
	LifecycleDeleted   = "deleted"
)

type Project struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	TechStack     []string      `json:"tech_stack"`
	Status        string        `json:"status"`
	Pages         []string      `json:"pages"`
	DevURL        string        `json:"dev_url"`
	DevPort       int           `json:"dev_port"`
	SessionID     string        `json:"session_id"`
	Lifecycle     string        `json:"lifecycle"`
	PrevLifecycle string        `json:"prev_lifecycle,omitempty"`
	SavedAt       *time.Time    `json:"saved_at,omitempty"`
	ArchivedAt    *time.Time    `json:"archived_at,omitempty"`
	LastOpenedAt  *time.Time    `json:"last_opened_at,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	History       []HistoryItem `json:"history"`
	Deleted       bool          `json:"deleted"`
	Dir           string        `json:"dir"`
}

type FileNode struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	IsDir    bool       `json:"is_dir"`
	Size     int64      `json:"size"`
	Children []FileNode `json:"children,omitempty"`
}

type Version struct {
	File      string    `json:"file"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
	Size      int64     `json:"size"`
}

// MigrationReport 是一次目录布局迁移的结果。
type MigrationReport struct {
	Projects       int
	MovedSource    int
	MovedAssets    int
	MovedMemory    int
	CopiedOutput   int
	MergedMetadata int
	MovedFiles     int
	BackupDir      string
	Details        []string
	Errors         []string
}

// LifecycleLabel 返回生命周期中文名，供终端展示。
func LifecycleLabel(lc string) string {
	switch lc {
	case LifecycleTemporary:
		return "临时"
	case LifecycleActive:
		return "开发中"
	case LifecycleSaved:
		return "已保存"
	case LifecycleArchived:
		return "已归档"
	case LifecycleDeleted:
		return "已删除"
	}
	return lc
}

// Describe 返回单行项目描述，用于菜单展示。
func (p *Project) Describe() string {
	return fmt.Sprintf("%s [%s] %s", p.Name, LifecycleLabel(p.Lifecycle), p.UpdatedAt.Format("2006-01-02 15:04"))
}

// ProjectDirForDisplay 返回项目根目录（source 的上一级）。
func (p *Project) ProjectDirForDisplay() string {
	return filepath.Dir(p.Dir)
}
