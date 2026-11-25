package project

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"chat2web/internal/textutil"
)

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

// MigrateLayout 把旧布局统一迁移到 data/projects/{id}/：
//
//	{assets, source, output, memory.md, project.json}
//
// 迁移原则：
//   - 迁移前把旧元数据与记忆复制到 data/backups/migrate-{ts}/；
//   - 只移动/合并，不删除真实文件；冲突时保留较新版本，旧版本移入备份；
//   - 无法确认归属的文件原样保留；
//   - 迁移完成后才清理空的旧目录。
func (m *Manager) MigrateLayout(legacySiteRoot, legacyProjectRoot, legacyMemoryRoot string) MigrationReport {
	rep := MigrationReport{
		BackupDir: filepath.Join(m.dataRoot, "backups", "migrate-"+time.Now().Format("20060102-150405")),
	}
	ids := m.legacyProjectIDs(legacySiteRoot, legacyProjectRoot, legacyMemoryRoot)
	if len(ids) == 0 {
		return rep
	}
	for _, id := range ids {
		rep.Projects++
		m.migrateProject(id, legacySiteRoot, legacyProjectRoot, legacyMemoryRoot, &rep)
	}
	m.cleanupLegacy(legacyProjectRoot, legacyMemoryRoot, &rep)
	return rep
}

// legacyProjectIDs 收集所有旧位置出现过的项目 ID。
func (m *Manager) legacyProjectIDs(siteRoot, projectRoot, memoryRoot string) []string {
	set := map[string]bool{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id != "" {
			set[id] = true
		}
	}
	for _, dir := range listDirs(m.projectsRoot) {
		add(dir)
	}
	for _, dir := range listDirs(projectRoot) {
		add(dir)
	}
	for _, dir := range listDirs(siteRoot) {
		add(dir)
	}
	if files, _ := filepath.Glob(filepath.Join(m.projectsRoot, "*.json")); len(files) > 0 {
		for _, f := range files {
			add(strings.TrimSuffix(filepath.Base(f), ".json"))
		}
	}
	if files, _ := filepath.Glob(filepath.Join(memoryRoot, "*.md")); len(files) > 0 {
		for _, f := range files {
			add(strings.TrimSuffix(filepath.Base(f), ".md"))
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (m *Manager) migrateProject(id, siteRoot, projectRoot, memoryRoot string, rep *MigrationReport) {
	legacyDir := ""
	if strings.TrimSpace(projectRoot) != "" {
		legacyDir = filepath.Join(projectRoot, id)
	}
	target := m.ProjectDir(id)

	// 1. 合并元数据（project.json 是唯一来源）。
	p, merged, err := m.loadLegacyMeta(id, legacyDir)
	if err != nil {
		rep.Errors = append(rep.Errors, id+": 读取旧元数据失败: "+err.Error())
		return
	}
	if merged {
		rep.MergedMetadata++
	}
	m.backupLegacyMeta(id, legacyDir, rep)

	// 已删除项目不重建目录，只把元数据留存在备份中。
	if p.Deleted {
		rep.Details = append(rep.Details, id+"：已删除项目，仅备份元数据")
		return
	}

	if err := m.ensureLayout(id); err != nil {
		rep.Errors = append(rep.Errors, id+": 创建目录失败: "+err.Error())
		return
	}

	// 2. 旧网站目录 workspace/generated-websites/{id} → source/
	if strings.TrimSpace(siteRoot) != "" && !dirHasEntries(m.SiteDir(id)) {
		legacySite := filepath.Join(siteRoot, id)
		if dirHasEntries(legacySite) {
			if err := moveDir(legacySite, m.SiteDir(id)); err != nil {
				rep.Errors = append(rep.Errors, id+" 迁移源码失败: "+err.Error())
			} else {
				rep.MovedSource++
			}
		}
	}

	// 3. 合并旧项目目录 workspace/projects/{id}
	if legacyDir != "" && !sameDir(legacyDir, target) && dirExists(legacyDir) {
		m.mergeLegacyProjectDir(id, legacyDir, target, rep)
	}

	// 4. 旧项目记忆 memory/projects/{id}.md → {id}/memory.md
	if strings.TrimSpace(memoryRoot) != "" {
		legacyMem := filepath.Join(memoryRoot, safeFileName(id)+".md")
		if m.mergeMemoryFile(id, legacyMem, rep) {
			rep.MovedMemory++
		}
	}
	if !fileExists(m.MemoryFile(id)) {
		_ = m.ensureMemoryFile(id, p.Name)
	}

	// 5. 构建产物 dist/ → output/
	dist := filepath.Join(m.SiteDir(id), "dist")
	if dirHasEntries(dist) && !dirHasEntries(m.OutputDir(id)) {
		if copyDir(dist, m.OutputDir(id)) == nil {
			rep.CopiedOutput++
		}
	}

	// 6. 写入统一元数据：只保留项目目录内的 project.json。
	if p.ID == "" {
		p.ID = id
	}
	if p.Lifecycle == "" {
		p.Lifecycle = LifecycleSaved
	}
	if strings.TrimSpace(p.Name) == "" {
		p.Name = "未命名网站项目"
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	if err := m.save(&p); err != nil {
		rep.Errors = append(rep.Errors, id+": 写入 project.json 失败: "+err.Error())
	}
}

// loadLegacyMeta 读取并合并项目目录、旧索引 JSON、旧项目目录中的元数据。
func (m *Manager) loadLegacyMeta(id, legacyDir string) (Project, bool, error) {
	var candidates []Project
	paths := []string{m.metaPath(id)}
	if legacyDir != "" {
		paths = append(paths, filepath.Join(legacyDir, "project.json"))
	}
	paths = append(paths, filepath.Join(m.projectsRoot, id+".json"))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var p Project
		if err := json.Unmarshal(data, &p); err != nil {
			continue
		}
		if p.ID == "" {
			p.ID = id
		}
		candidates = append(candidates, p)
	}
	if len(candidates) == 0 {
		return Project{ID: id, Lifecycle: LifecycleSaved, CreatedAt: time.Now(), UpdatedAt: time.Now()}, false, nil
	}
	// 以更新时间最新的元数据为准，其余只补齐缺失字段，避免覆盖新版本。
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].UpdatedAt.After(candidates[j].UpdatedAt) })
	base := candidates[0]
	for _, other := range candidates[1:] {
		mergeProjectMeta(&base, other)
	}
	return base, len(candidates) > 1, nil
}

func mergeProjectMeta(base *Project, other Project) {
	if strings.TrimSpace(base.Name) == "" {
		base.Name = other.Name
	}
	if strings.TrimSpace(base.Description) == "" {
		base.Description = other.Description
	}
	if strings.TrimSpace(base.Status) == "" {
		base.Status = other.Status
	}
	if strings.TrimSpace(base.DevURL) == "" {
		base.DevURL = other.DevURL
	}
	if base.DevPort == 0 {
		base.DevPort = other.DevPort
	}
	if strings.TrimSpace(base.SessionID) == "" {
		base.SessionID = other.SessionID
	}
	if strings.TrimSpace(base.Lifecycle) == "" {
		base.Lifecycle = other.Lifecycle
	}
	if base.CreatedAt.IsZero() || (!other.CreatedAt.IsZero() && other.CreatedAt.Before(base.CreatedAt)) {
		base.CreatedAt = other.CreatedAt
	}
	if base.SavedAt == nil {
		base.SavedAt = other.SavedAt
	}
	if base.ArchivedAt == nil {
		base.ArchivedAt = other.ArchivedAt
	}
	if base.LastOpenedAt == nil || (other.LastOpenedAt != nil && other.LastOpenedAt.After(*base.LastOpenedAt)) {
		base.LastOpenedAt = other.LastOpenedAt
	}
	base.TechStack = mergeStrings(base.TechStack, other.TechStack)
	base.Pages = mergeStrings(base.Pages, other.Pages)
	for _, h := range other.History {
		dup := false
		for _, cur := range base.History {
			if cur.Summary == h.Summary && cur.Time.Equal(h.Time) {
				dup = true
				break
			}
		}
		if !dup {
			base.History = append(base.History, h)
		}
	}
	sort.Slice(base.History, func(i, j int) bool { return base.History[i].Time.Before(base.History[j].Time) })
}

func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{a, b} {
		for _, v := range list {
			v = strings.TrimSpace(v)
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// backupLegacyMeta 在迁移前复制旧元数据，保证可回滚。
func (m *Manager) backupLegacyMeta(id, legacyDir string, rep *MigrationReport) {
	paths := []string{filepath.Join(m.projectsRoot, id+".json")}
	if legacyDir != "" {
		paths = append(paths, filepath.Join(legacyDir, "project.json"))
	}
	for _, path := range paths {
		if !fileExists(path) {
			continue
		}
		dstDir := filepath.Join(rep.BackupDir, "metadata")
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			continue
		}
		_ = copyOneFile(path, filepath.Join(dstDir, id+"-"+filepath.Base(filepath.Dir(path))+"-"+filepath.Base(path)))
	}
}

// mergeLegacyProjectDir 把旧项目目录合并到统一目录，冲突文件放入备份。
func (m *Manager) mergeLegacyProjectDir(id, legacyDir, target string, rep *MigrationReport) {
	for _, sub := range []string{"source", "assets", "output"} {
		src := filepath.Join(legacyDir, sub)
		dst := filepath.Join(target, sub)
		if !dirHasEntries(src) {
			continue
		}
		if !dirHasEntries(dst) {
			// ensureLayout 会预先创建空的 source/assets/output；
			// 先移除空目录再移动，使 os.Rename 生效，避免整目录复制。
			_ = os.Remove(dst)
			if err := moveDir(src, dst); err == nil {
				switch sub {
				case "source":
					rep.MovedSource++
				case "assets":
					rep.MovedAssets++
				}
				continue
			}
		}
		moved := m.mergeDir(src, dst, filepath.Join(rep.BackupDir, "conflicts", id, sub))
		rep.MovedFiles += moved
		if moved > 0 {
			rep.Details = append(rep.Details, id+"："+sub+" 合并 "+itoa(moved)+" 个文件")
		}
	}

	if mem := filepath.Join(legacyDir, "memory.md"); fileExists(mem) {
		if m.mergeMemoryFile(id, mem, rep) {
			rep.MovedMemory++
		}
	}

	// 其余条目（旧布局的 package.json/src 等）归入 source，避免污染项目根目录。
	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "source", "assets", "output", "memory.md", "project.json":
			continue
		}
		src := filepath.Join(legacyDir, entry.Name())
		dst := filepath.Join(m.SiteDir(id), entry.Name())
		if _, statErr := os.Stat(dst); statErr != nil {
			if err := moveDir(src, dst); err == nil {
				rep.MovedFiles++
				continue
			}
		}
		// 无法确认归属或存在同名文件：保留到备份，不覆盖现有文件。
		_ = moveDir(src, filepath.Join(rep.BackupDir, "conflicts", id, "unknown", entry.Name()))
		rep.Details = append(rep.Details, id+"：保留未识别文件到备份 "+entry.Name())
	}

	// 旧项目目录内的 project.json 已合并到统一元数据，原文件移入备份。
	if legacyMeta := filepath.Join(legacyDir, "project.json"); fileExists(legacyMeta) {
		dst := filepath.Join(rep.BackupDir, "metadata", id+"-workspace-project.json")
		backupFile(legacyMeta, dst)
		_ = moveDir(legacyMeta, dst)
	}

	// 只在旧目录为空时移除它；仍有未知文件则原样保留。
	removeEmptyDirs(legacyDir)
}

// removeEmptyDirs 自底向上删除空目录，不触碰任何文件。
func removeEmptyDirs(root string) {
	if !dirExists(root) {
		return
	}
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		if entries, err := os.ReadDir(dirs[i]); err == nil && len(entries) == 0 {
			_ = os.Remove(dirs[i])
		}
	}
}

// mergeDir 把 src 中的文件合并到 dst：dst 缺失则移动，较新版本覆盖旧版本，
// 重复/较旧文件移入备份。返回实际移动或覆盖的文件数。
func (m *Manager) mergeDir(src, dst, backupDir string) int {
	moved := 0
	_ = filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return nil
		}
		target := filepath.Join(dst, rel)
		srcInfo, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		dstInfo, dstErr := os.Stat(target)
		if dstErr != nil {
			if moveDir(path, target) == nil {
				moved++
			}
			return nil
		}
		if srcInfo.ModTime().After(dstInfo.ModTime()) {
			backupFile(target, filepath.Join(backupDir, rel))
			if moveDir(path, target) == nil {
				moved++
			}
			return nil
		}
		// 目标已存在且不旧于源文件：源文件是重复项，移入备份后清理旧目录。
		backupPath := filepath.Join(backupDir, rel)
		backupFile(path, backupPath)
		if moveDir(path, backupPath) == nil {
			moved++
		}
		return nil
	})
	return moved
}

func (m *Manager) mergeMemoryFile(id, src string, rep *MigrationReport) bool {
	if !fileExists(src) {
		return false
	}
	target := m.MemoryFile(id)
	if !fileExists(target) {
		if err := moveDir(src, target); err != nil {
			rep.Errors = append(rep.Errors, id+": 迁移项目记忆失败: "+err.Error())
			return false
		}
		rep.Details = append(rep.Details, id+"：项目记忆已迁入 memory.md")
		return true
	}
	srcInfo, err1 := os.Stat(src)
	dstInfo, err2 := os.Stat(target)
	if err1 == nil && err2 == nil && srcInfo.ModTime().After(dstInfo.ModTime()) {
		backupFile(target, filepath.Join(rep.BackupDir, "memory", id+"-memory.md"))
		if err := moveDir(src, target); err == nil {
			rep.Details = append(rep.Details, id+"：保留较新的项目记忆")
			return true
		}
	} else {
		backupDir := filepath.Join(rep.BackupDir, "memory", id+"-legacy-memory.md")
		backupFile(src, backupDir)
		_ = moveDir(src, backupDir)
	}
	return false
}

// cleanupLegacy 迁移完成后清理旧索引 JSON 与空目录；非空目录一律保留。
func (m *Manager) cleanupLegacy(projectRoot, memoryRoot string, rep *MigrationReport) {
	files, _ := filepath.Glob(filepath.Join(m.projectsRoot, "*.json"))
	for _, f := range files {
		target := filepath.Join(rep.BackupDir, "metadata", filepath.Base(f))
		if moveDir(f, target) == nil {
			rep.Details = append(rep.Details, "旧元数据已归档："+filepath.Base(f))
		}
	}
	for _, dir := range []string{projectRoot, memoryRoot} {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
			_ = os.Remove(dir)
			rep.Details = append(rep.Details, "已清理空目录："+dir)
		}
	}
}

func listDirs(root string) []string {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			out = append(out, entry.Name())
		}
	}
	return out
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func sameDir(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}

func backupFile(src, dst string) {
	if !fileExists(src) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return
	}
	_ = copyOneFile(src, dst)
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
		// node_modules 是可重建的依赖缓存，跨盘复制时跳过，避免迁移耗时与体积膨胀。
		if entry.IsDir() && entry.Name() == "node_modules" {
			continue
		}
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
