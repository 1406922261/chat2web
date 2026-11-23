package memory

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

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
	return rep
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
	archiveDir := filepath.Join(m.root, "backup", "reports-"+time.Now().Format("20060102-150405"))
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
