package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newLinkedManager(t *testing.T, root, projectsRoot string) *Manager {
	t.Helper()
	m := NewManager(filepath.Join(root, "data", "memory"), nil)
	m.Configure(Options{
		ProjectRoot: projectsRoot,
		RuntimeRoot: filepath.Join(root, "runtime"),
		ReportsRoot: filepath.Join(root, "reports"),
		TasksRoot:   filepath.Join(root, "tasks"),
	})
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestProjectMemoryLivesWithProject(t *testing.T) {
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "workspace", "projects")
	id := "project-20260101-000000-abcdef"
	if err := os.MkdirAll(filepath.Join(projectsRoot, id), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newLinkedManager(t, root, projectsRoot)

	m.UpdateProjectMemory(id, "北京涮羊肉攻略", "涮羊肉推荐", []string{"React"}, []string{"首页"}, "generated", "包含店铺推荐", "")
	path := filepath.Join(projectsRoot, id, "memory.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("项目记忆应写入项目目录：%v", err)
	}

	// 重新加载后索引仍指向项目内 memory.md，并可检索。
	m2 := newLinkedManager(t, root, projectsRoot)
	results := m2.SearchMemory("涮羊肉", 5)
	found := false
	for _, r := range results {
		if strings.Contains(r.Title, "涮羊肉") || strings.Contains(r.Snippet, "涮羊肉") {
			found = true
			if !strings.Contains(r.File, filepath.ToSlash(filepath.Join("..", "workspace", "projects", id, "memory.md"))) {
				t.Fatalf("索引未指向项目内记忆文件：%s", r.File)
			}
		}
	}
	if !found {
		t.Fatalf("重新加载后无法检索项目记忆：%+v", results)
	}
}

func TestMoveEntryToProjectAndRebuildIndex(t *testing.T) {
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "workspace", "projects")
	id := "project-20260101-000000-abcdef"
	if err := os.MkdirAll(filepath.Join(projectsRoot, id), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newLinkedManager(t, root, projectsRoot)
	m.UpsertEntry(Entry{ID: "m1", Kind: "preference", Key: "减脂训练", Value: "每天 30 分钟哑铃", Source: "manual"})
	if len(m.Entries()) != 1 {
		t.Fatalf("期望 1 条用户记忆，实际 %d", len(m.Entries()))
	}

	if err := m.MoveEntryToProject("m1", id); err != nil {
		t.Fatal(err)
	}
	if len(m.UserEntries()) != 0 {
		t.Fatal("项目专属记忆应从用户记忆移出")
	}
	data, err := os.ReadFile(filepath.Join(projectsRoot, id, "memory.md"))
	if err != nil || !strings.Contains(string(data), "哑铃") {
		t.Fatalf("项目记忆未写入：%v", err)
	}

	m.RebuildIndex()
	if info, err := os.Stat(filepath.Join(root, "data", "memory", "index.json")); err != nil || info.Size() > 30*1024 {
		t.Fatalf("索引异常：%v", err)
	}
}

func TestCleanupTaskScreenshotsKeepsUserAssets(t *testing.T) {
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtime")
	playwright := filepath.Join(runtimeRoot, "playwright")
	if err := os.MkdirAll(playwright, 0o755); err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(root, "workspace", "projects", "p1", "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-time.Minute)
	debugShot := filepath.Join(playwright, "step.png")
	userShot := filepath.Join(assets, "upload.png")
	snapshot := filepath.Join(playwright, "page.yml")
	for _, f := range []string{debugShot, userShot, snapshot} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := NewManager(filepath.Join(root, "data", "memory"), nil)
	m.Configure(Options{RuntimeRoot: runtimeRoot})
	rep := m.CleanupTaskScreenshots(since)
	if rep.DeletedFiles != 1 {
		t.Fatalf("应只清理 1 张调试截图，实际 %d", rep.DeletedFiles)
	}
	if exists(debugShot) {
		t.Fatal("调试截图应被清理")
	}
	if !exists(userShot) {
		t.Fatal("用户上传图片不能被误删")
	}
	if !exists(snapshot) {
		t.Fatal("页面快照按 3 天策略保留，不应立即删除")
	}
}

func TestConsolidateSessions(t *testing.T) {
	root := t.TempDir()
	m := newLinkedManager(t, root, filepath.Join(root, "workspace", "projects"))
	dir := filepath.Join(root, "data", "memory", "sessions", "2026-01")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"session-p1-1.md", "legacy-1.md"} {
		content := "# 会话摘要\n\n- 会话：s" + name + "\n- 项目ID：p1\n- 项目：涮羊肉\n- 核心任务：推荐店铺\n- 更新时间：" + time.Now().Add(time.Duration(i)*time.Minute).Format("2006-01-02 15:04:05") + "\n\n## 已完成工作\n\n- 第" + string(rune('1'+i)) + "步\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	moved, err := m.ConsolidateSessions()
	if err != nil {
		t.Fatal(err)
	}
	if moved == 0 {
		t.Fatal("重复会话摘要应被合并备份")
	}
	files, _ := filepath.Glob(filepath.Join(root, "data", "memory", "sessions", "*", "*.md"))
	if len(files) != 1 {
		t.Fatalf("合并后每个项目应只保留 1 份摘要，实际 %d", len(files))
	}
}
