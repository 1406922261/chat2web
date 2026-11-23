package project

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"chat2web/internal/textutil"
)

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

// Save 把项目标记为正式保存，永久保留。
func (m *Manager) Save(id string) (*Project, error) {
	return m.UpdateFunc(id, func(p *Project) {
		now := time.Now()
		p.Lifecycle = LifecycleSaved
		p.SavedAt = &now
		p.ArchivedAt = nil
		p.PrevLifecycle = ""
		p.History = append(p.History, HistoryItem{Time: now, Kind: "保存", Summary: "项目已保存为正式项目"})
	})
}

// MarkActive 把临时项目标记为开发中；已保存项目保持 saved。
func (m *Manager) MarkActive(id string) {
	_, _ = m.UpdateFunc(id, func(p *Project) {
		if p.Lifecycle == LifecycleTemporary {
			p.Lifecycle = LifecycleActive
		}
	})
}

// Touch 记录最近打开时间。
func (m *Manager) Touch(id string) {
	_, _ = m.UpdateFunc(id, func(p *Project) {
		now := time.Now()
		p.LastOpenedAt = &now
	})
}

// Archive 归档项目：移出活动列表、清除开发服务器地址、移除可重建的 node_modules。
func (m *Manager) Archive(id string) (*Project, error) {
	p, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	if p.Lifecycle != LifecycleArchived {
		p.PrevLifecycle = p.Lifecycle
	}
	now := time.Now()
	p.Lifecycle = LifecycleArchived
	p.ArchivedAt = &now
	p.DevURL = ""
	removeNodeModules(m.SiteDir(id))
	p.History = append(p.History, HistoryItem{Time: now, Kind: "归档", Summary: "项目已归档，源码与记忆保留"})
	if err := m.Update(p); err != nil {
		return nil, err
	}
	return p, nil
}

// Unarchive 恢复归档项目到归档前的生命周期。
func (m *Manager) Unarchive(id string) (*Project, error) {
	p, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	lc := p.PrevLifecycle
	if lc == "" || lc == LifecycleArchived || lc == LifecycleDeleted {
		lc = LifecycleSaved
	}
	p.Lifecycle = lc
	p.PrevLifecycle = ""
	p.ArchivedAt = nil
	p.History = append(p.History, HistoryItem{Time: time.Now(), Kind: "恢复", Summary: "项目已从归档恢复"})
	if err := m.Update(p); err != nil {
		return nil, err
	}
	return p, nil
}

// ByLifecycle 返回指定生命周期的项目（不含已删除）。
func (m *Manager) ByLifecycle(lc string) []*Project {
	var out []*Project
	for _, p := range m.List(false) {
		if p.Lifecycle == lc {
			out = append(out, p)
		}
	}
	return out
}

// FormalProjects 正式项目：用户明确保存的项目。
func (m *Manager) FormalProjects() []*Project { return m.ByLifecycle(LifecycleSaved) }

// TemporaryProjects 临时项目：临时生成或尚未保存的开发中项目。
func (m *Manager) TemporaryProjects() []*Project {
	out := m.ByLifecycle(LifecycleTemporary)
	out = append(out, m.ByLifecycle(LifecycleActive)...)
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// ArchivedProjects 已归档项目。
func (m *Manager) ArchivedProjects() []*Project { return m.ByLifecycle(LifecycleArchived) }

// SelectableProjects 返回可继续修改的项目（正式 + 临时 + 开发中）。
func (m *Manager) SelectableProjects() []*Project {
	out := m.FormalProjects()
	out = append(out, m.TemporaryProjects()...)
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// DeletedProjects 回收站中的项目。
func (m *Manager) DeletedProjects() []*Project {
	var out []*Project
	for _, p := range m.List(true) {
		if p.Deleted {
			out = append(out, p)
		}
	}
	return out
}

// StaleTemporary 返回超过保留期未使用的临时项目，用于提醒清理。
func (m *Manager) StaleTemporary(days int) []*Project {
	if days <= 0 {
		days = 7
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	var out []*Project
	for _, p := range m.TemporaryProjects() {
		if p.UpdatedAt.Before(cutoff) {
			out = append(out, p)
		}
	}
	return out
}

// PurgeTemporary 清理过期临时项目；dryRun 时只返回清单。项目目录移入回收站，可恢复。
func (m *Manager) PurgeTemporary(days int, dryRun bool) ([]string, error) {
	stale := m.StaleTemporary(days)
	ids := make([]string, 0, len(stale))
	for _, p := range stale {
		ids = append(ids, p.ID)
		if dryRun {
			continue
		}
		if err := m.Delete(p.ID); err != nil {
			return ids, err
		}
	}
	return ids, nil
}

// Match 根据聊天内容匹配已有项目，避免同一网页反复创建新项目。
func (m *Manager) Match(chat string) *Project {
	query := textutil.KeywordsLimited(chat, 16)
	if len(query) == 0 {
		return nil
	}
	lowChat := strings.ToLower(chat)
	var best *Project
	bestScore := 0
	for _, p := range m.SelectableProjects() {
		var recent []string
		for i := len(p.History) - 1; i >= 0 && len(recent) < 3; i-- {
			recent = append(recent, p.History[i].Summary)
		}
		text := strings.ToLower(p.Name + " " + p.Description + " " + strings.Join(p.Pages, " ") + " " + strings.Join(recent, " "))
		score := 0
		for _, k := range query {
			if strings.Contains(text, k) {
				score++
			}
		}
		if name := strings.ToLower(strings.TrimSpace(p.Name)); len([]rune(name)) >= 3 && strings.Contains(lowChat, name) {
			score += 3
		}
		if score > bestScore {
			best, bestScore = p, score
		}
	}
	if bestScore < 2 {
		return nil
	}
	return best
}

// PublishOutput 把 source/dist 的最终构建复制到 output/，作为正式成果产物。
func (m *Manager) PublishOutput(id string) (string, error) {
	src := filepath.Join(m.SiteDir(id), "dist")
	if _, err := os.Stat(src); err != nil {
		return "", nil
	}
	dst := m.OutputDir(id)
	if err := os.RemoveAll(dst); err != nil {
		return "", err
	}
	if err := copyDir(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// MigrationReport 是一次目录布局迁移的结果。
type MigrationReport struct {
	Projects     int
	MovedSource  int
	MovedMemory  int
	CopiedOutput int
	Errors       []string
}

// MigrateLayout 把第一轮改造前的目录迁移为统一项目结构：
//
//	workspace/projects/{id}/project.json memory.md source assets output
//
// 只做移动/复制，不删除任何项目。
func (m *Manager) MigrateLayout(legacySiteRoot, legacyMemoryRoot string) MigrationReport {
	rep := MigrationReport{}
	files, _ := filepath.Glob(filepath.Join(m.dataRoot, "projects", "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var p Project
		if err := json.Unmarshal(data, &p); err != nil {
			continue
		}
		if p.Deleted {
			continue
		}
		rep.Projects++
		m.normalize(&p)
		if err := m.ensureLayout(p.ID); err != nil {
			rep.Errors = append(rep.Errors, p.ID+": "+err.Error())
			continue
		}
		changed := false

		legacySite := filepath.Join(legacySiteRoot, p.ID)
		if !dirHasEntries(m.SiteDir(p.ID)) && dirHasEntries(legacySite) {
			if err := moveDir(legacySite, m.SiteDir(p.ID)); err != nil {
				rep.Errors = append(rep.Errors, p.ID+" 迁移源码失败: "+err.Error())
			} else {
				rep.MovedSource++
				changed = true
			}
		}

		if legacyMemoryRoot != "" {
			legacyMem := filepath.Join(legacyMemoryRoot, safeFileName(p.ID)+".md")
			target := m.MemoryFile(p.ID)
			if _, err := os.Stat(target); err != nil && fileExists(legacyMem) {
				if err := os.Rename(legacyMem, target); err == nil {
					rep.MovedMemory++
					changed = true
				}
			}
		}

		dist := filepath.Join(m.SiteDir(p.ID), "dist")
		if dirHasEntries(dist) && !dirHasEntries(m.OutputDir(p.ID)) {
			if err := copyDir(dist, m.OutputDir(p.ID)); err == nil {
				rep.CopiedOutput++
				changed = true
			}
		}

		if p.Lifecycle == "" {
			p.Lifecycle = LifecycleSaved
			changed = true
		}
		if changed || !fileExists(filepath.Join(m.ProjectDir(p.ID), "project.json")) {
			if err := m.Update(&p); err != nil {
				rep.Errors = append(rep.Errors, p.ID+" 写入 project.json 失败: "+err.Error())
			}
		}
	}
	return rep
}

func safeFileName(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		return r
	}, s)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirHasEntries(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) > 0
}

func removeNodeModules(dir string) {
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil || !entry.IsDir() {
			return nil
		}
		if entry.Name() == "node_modules" {
			_ = os.RemoveAll(path)
			return filepath.SkipDir
		}
		return nil
	})
}

// moveDir 优先重命名，失败时复制后删除，保证跨盘也能迁移。
func moveDir(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyDir(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyOneFile(src, dst)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyDir(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyOneFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

// Describe 返回单行项目描述，用于菜单展示。
func (p *Project) Describe() string {
	return fmt.Sprintf("%s [%s] %s", p.Name, LifecycleLabel(p.Lifecycle), p.UpdatedAt.Format("2006-01-02 15:04"))
}

// ProjectDirForDisplay 返回项目根目录（source 的上一级）。
func (p *Project) ProjectDirForDisplay() string {
	return filepath.Dir(p.Dir)
}
