package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

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

// RebuildIndex 重建轻量检索索引，用于清理后同步关键词与文件位置。
func (m *Manager) RebuildIndex() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshIndexLocked()
	m.persistIndexLocked()
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
	files, _ := filepath.Glob(filepath.Join(m.root, "sessions", "*", "*.md"))
	groups := map[string][]digestFile{}
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

	backupDir := filepath.Join(m.root, "backup", "sessions-"+time.Now().Format("20060102-150405"))
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
		target := filepath.Join(m.root, "sessions", merged.UpdatedAt.Format("2006-01"), name+".md")
		rel, err := filepath.Rel(m.root, target)
		if err != nil {
			rel = target
		}
		merged.File = filepath.ToSlash(rel)
		if err := writeText(target, renderDigest(&merged)); err != nil {
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

// MemoryBackupRoot 返回备份目录，删除操作必须可恢复。
func (m *Manager) MemoryBackupRoot(tag string) string {
	return filepath.Join(m.root, "backup", tag)
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
