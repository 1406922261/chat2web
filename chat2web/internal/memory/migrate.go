package memory

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"chat2web/internal/llm"
)

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
// 原则：先写新文件，成功后再把旧文件移动到 backup/{ts}/，绝不直接删除。
func (m *Manager) migrateLegacyLocked() error {
	ts := time.Now().Format("20060102-150405")
	backupRoot := filepath.Join(m.root, "backup", ts)
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

	// 1) 用户长期记忆：long_term.json + user_profile.md → MEMORY.md
	userPath := m.userFile()
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

	// 2) 项目记忆：projects/*.json → projects/{id}.md
	files, _ := filepath.Glob(filepath.Join(m.root, "projects", "*.json"))
	for _, f := range files {
		var lp legacyProject
		if err := readJSON(f, &lp); err != nil || strings.TrimSpace(lp.ProjectID) == "" {
			continue
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

	// 3) 会话：sessions/*.json → sessions/{month}/{id}.md（本地启发式摘要）
	files, _ = filepath.Glob(filepath.Join(m.root, "sessions", "*.json"))
	for _, f := range files {
		var ls legacySession
		if err := readJSON(f, &ls); err != nil || strings.TrimSpace(ls.ID) == "" {
			continue
		}
		when := ls.UpdatedAt
		if when.IsZero() {
			when = time.Now()
		}
		target := filepath.Join(m.root, "sessions", when.Format("2006-01"), safeName(ls.ID)+".md")
		if !exists(target) {
			result := heuristicDigest(ls.Messages)
			d := mergeDigest(nil, result, len(ls.Messages), ls.ProjectID, ls.Title)
			rel, _ := filepath.Rel(m.root, target)
			d.File = filepath.ToSlash(rel)
			d.UpdatedAt = when
			if err := writeText(target, renderDigest(d)); err != nil {
				continue
			}
		}
		moveToBackup(f)
	}

	// 4) 旧摘要：summaries/*.md → sessions/{month}/legacy-*.md
	files, _ = filepath.Glob(filepath.Join(m.root, "summaries", "*.md"))
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		target := filepath.Join(m.root, "sessions", info.ModTime().Format("2006-01"), "legacy-"+filepath.Base(f))
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
	dataRoot := filepath.Dir(m.root)
	files, _ := filepath.Glob(filepath.Join(dataRoot, "*.log"))
	for _, f := range files {
		target := filepath.Join(m.opts.RuntimeRoot, "shell", "legacy-"+ts+"-"+filepath.Base(f))
		_ = movePath(f, target)
	}
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
	dataRoot := filepath.Dir(m.root)
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
