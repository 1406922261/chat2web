package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chat2web/internal/llm"
)

func newTestManager(t *testing.T, root string) *Manager {
	t.Helper()
	m := NewManager(root, nil)
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMemoryPersistenceAndRetrieval(t *testing.T) {
	root := t.TempDir()
	m := newTestManager(t, root)

	m.UpsertEntry(Entry{Kind: "preference", Key: "design_style", Value: "深色科技风格", Tags: []string{"深色"}, Source: "manual"})
	m.UpsertEntry(Entry{Kind: "tech", Key: "frontend", Value: "React + TypeScript", Tags: []string{"React"}, Source: "manual"})
	if len(m.Entries()) != 2 {
		t.Fatalf("期望 2 条记忆，实际 %d", len(m.Entries()))
	}

	// 相同 key 或相同内容应更新而不是重复插入
	m.UpsertEntry(Entry{Kind: "preference", Key: "design_style", Value: "极简深色风格", Source: "manual"})
	m.UpsertEntry(Entry{Kind: "preference", Key: "other_key", Value: "极简深色风格", Source: "manual"})
	if len(m.Entries()) != 2 {
		t.Fatalf("更新/去重后不应产生重复条目，实际 %d", len(m.Entries()))
	}

	m.AppendMessages("s1", "p1", llm.Message{Role: "user", Content: "我喜欢深色网站，使用 React"})
	m.UpdateProjectMemory("p1", "个人作品集", "深色作品集", []string{"React"}, []string{"首页"}, "generated", "", "")

	block := m.Retrieve("帮我做一个深色个人博客，使用 React", "p1")
	if !strings.Contains(block, "深色") || !strings.Contains(block, "React") {
		t.Fatalf("检索上下文未注入偏好: %s", block)
	}

	// MEMORY.md 为唯一用户记忆源文件
	if _, err := os.Stat(filepath.Join(root, "MEMORY.md")); err != nil {
		t.Fatalf("MEMORY.md 未生成: %v", err)
	}
	if files, _ := filepath.Glob(filepath.Join(root, "sessions", "*.json")); len(files) != 0 {
		t.Fatalf("原始会话不应持久化，发现 %d 个 json", len(files))
	}

	// 重启后恢复长期记忆；原始对话按设计不恢复
	m2 := newTestManager(t, root)
	if len(m2.Entries()) != 2 {
		t.Fatalf("重启后记忆条目丢失，实际 %d", len(m2.Entries()))
	}
	if got := m2.SessionMessages("s1"); len(got) != 0 {
		t.Fatalf("原始会话不应跨进程恢复，实际 %d 条", len(got))
	}
	if pm := m2.ProjectMemory("p1"); pm == nil || pm.Name != "个人作品集" {
		t.Fatalf("项目记忆未恢复: %+v", pm)
	}

	// 模拟重启后继续项目：应能恢复项目目标与进度，而不是依赖原始对话。
	resumed := m2.Retrieve("继续完善个人作品集", "p1")
	if !strings.Contains(resumed, "个人作品集") || !strings.Contains(resumed, "generated") {
		t.Fatalf("重启后未恢复项目进度: %s", resumed)
	}
}

func TestProjectIsolation(t *testing.T) {
	root := t.TempDir()
	m := newTestManager(t, root)

	m.SaveMemory(Entry{Kind: "preference", Key: "视觉风格", Value: "强调留白", Source: "manual"})
	m.SaveMemory(Entry{Kind: "project", Key: "业务主题", Value: "海鲜电商", ProjectID: "p1", Source: "manual"})
	m.SaveMemory(Entry{Kind: "project", Key: "业务主题", Value: "滑雪装备", ProjectID: "p2", Source: "manual"})
	m.UpdateProjectMemory("p1", "海鲜商城", "卖海鲜", nil, nil, "draft", "", "")
	m.UpdateProjectMemory("p2", "滑雪商城", "卖滑雪装备", nil, nil, "draft", "", "")

	b1 := m.Retrieve("商城页面设计", "p1")
	if !strings.Contains(b1, "海鲜") || strings.Contains(b1, "滑雪") {
		t.Fatalf("项目 p1 记忆被污染: %s", b1)
	}
	b2 := m.Retrieve("商城页面设计", "p2")
	if !strings.Contains(b2, "滑雪") || strings.Contains(b2, "海鲜电商") {
		t.Fatalf("项目 p2 记忆被污染: %s", b2)
	}
}

func TestIncrementalSessionSummary(t *testing.T) {
	root := t.TempDir()
	m := newTestManager(t, root)

	m.AppendMessages("s1", "p1",
		llm.Message{Role: "user", Content: "做一个深色科技风个人作品集，使用 React"},
		llm.Message{Role: "assistant", Content: "已生成首页与项目列表页面"},
	)
	res, err := m.SummarizeSession(context.Background(), "s1", "p1", "个人作品集")
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != "heuristic" {
		t.Fatalf("无 LLM 时应使用本地摘要，实际 %s", res.Source)
	}
	m.mu.RLock()
	d := m.digests["s1"]
	firstCursor := 0
	var firstSummary string
	if d != nil {
		firstCursor = d.Cursor
		firstSummary = d.Summary
	}
	m.mu.RUnlock()
	if firstCursor != 2 {
		t.Fatalf("游标应为 2，实际 %d", firstCursor)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(res.File))); err != nil {
		t.Fatalf("会话摘要文件不存在: %v", err)
	}

	// 增量：只追加新消息，摘要应更新且游标前进
	m.AppendMessages("s1", "p1", llm.Message{Role: "user", Content: "再加一个旅行相册页面，保留深色风格"})
	res2, err := m.SummarizeSession(context.Background(), "s1", "p1", "个人作品集")
	if err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	cursor := m.digests["s1"].Cursor
	summary := m.digests["s1"].Summary
	m.mu.RUnlock()
	if cursor != 3 {
		t.Fatalf("增量摘要游标应为 3，实际 %d", cursor)
	}
	if summary == firstSummary {
		t.Fatalf("增量摘要未更新: %s", summary)
	}
	if !strings.Contains(summary, "旅行相册") {
		t.Fatalf("新消息未进入摘要: %s", summary)
	}

	// 没有新消息时不重复调用模型/重复写入
	if _, err := m.SummarizeSession(context.Background(), "s1", "p1", "个人作品集"); err == nil {
		t.Fatal("无新增消息时应跳过摘要")
	}
	_ = res2

	// 重启后摘要与项目进度可从磁盘恢复
	m2 := newTestManager(t, root)
	if len(m2.Digests()) != 1 {
		t.Fatalf("重启后会话摘要丢失，实际 %d", len(m2.Digests()))
	}
	m2.mu.RLock()
	restored := m2.digests["s1"]
	m2.mu.RUnlock()
	if restored == nil || restored.Cursor != 3 {
		t.Fatalf("重启后摘要游标错误: %+v", restored)
	}
	if pm := m2.ProjectMemory("p1"); pm == nil || pm.Status != "ready" || len(pm.Completed) == 0 {
		t.Fatalf("重启后项目进度未恢复: %+v", pm)
	}
}

func TestMemoryAPIAndSearch(t *testing.T) {
	root := t.TempDir()
	m := newTestManager(t, root)

	saved, err := m.SaveMemory(Entry{Kind: "preference", Key: "配色", Value: "深色科技风", Tags: []string{"深色", "科技"}})
	if err != nil {
		t.Fatal(err)
	}
	hits := m.SearchMemory("深色", 5)
	if len(hits) == 0 || hits[0].Title != "配色" {
		t.Fatalf("检索未命中: %+v", hits)
	}
	if err := m.UpdateMemory(saved.ID, MemoryPatch{Value: "浅色简约风"}); err != nil {
		t.Fatal(err)
	}
	if hits := m.SearchMemory("浅色", 5); len(hits) == 0 {
		t.Fatal("更新后检索未命中新内容")
	}
	if err := m.DeleteMemory(saved.ID); err != nil {
		t.Fatal(err)
	}
	if len(m.Entries()) != 0 {
		t.Fatalf("删除后仍有 %d 条记忆", len(m.Entries()))
	}
}

func TestCleanupRuntimeAndReports(t *testing.T) {
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtime")
	reportsRoot := filepath.Join(root, "reports")
	m := NewManager(filepath.Join(root, "memory"), nil)
	m.Configure(Options{
		RuntimeRoot:    runtimeRoot,
		ReportsRoot:    reportsRoot,
		TasksRoot:      filepath.Join(root, "tasks"),
		PlaywrightDays: 3,
		TaskDays:       7,
		ReportsKeep:    10,
	})
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}

	writeAged(t, filepath.Join(runtimeRoot, "playwright", "old.log"), "old", 5*24*time.Hour)
	writeAged(t, filepath.Join(runtimeRoot, "playwright", "new.log"), "new", time.Hour)
	writeAged(t, filepath.Join(runtimeRoot, "shell", "old.log"), "old", 8*24*time.Hour)
	writeAged(t, filepath.Join(runtimeRoot, "tasks-tmp", "recent.log"), "recent", time.Hour)
	for i := 0; i < 12; i++ {
		writeAged(t, filepath.Join(runtimeRoot, "reports-tmp", "tmp-"+itoa(i)+".md"), "tmp", time.Duration(i)*time.Hour)
		writeAged(t, filepath.Join(reportsRoot, "report-"+itoa(i)+".md"), "report", time.Duration(i)*time.Hour)
	}
	m.SaveMemory(Entry{Kind: "preference", Key: "长期", Value: "必须保留", Pinned: true})

	rep := m.CleanupMemory()
	if rep.DeletedFiles < 2 {
		t.Fatalf("应删除过期 Playwright/Shell 文件，实际 %d", rep.DeletedFiles)
	}
	if _, err := os.Stat(filepath.Join(runtimeRoot, "playwright", "old.log")); !os.IsNotExist(err) {
		t.Fatal("过期 Playwright 日志未清理")
	}
	if _, err := os.Stat(filepath.Join(runtimeRoot, "playwright", "new.log")); err != nil {
		t.Fatal("新 Playwright 日志被误删")
	}
	if _, err := os.Stat(filepath.Join(runtimeRoot, "tasks-tmp", "recent.log")); err != nil {
		t.Fatal("未过期任务临时文件被误删")
	}
	if rep.ArchivedFiles != 2 {
		t.Fatalf("旧报告应归档 2 份，实际 %d", rep.ArchivedFiles)
	}
	if _, err := os.Stat(filepath.Join(reportsRoot, "report-9.md")); err != nil {
		t.Fatal("应保留最近 10 份报告")
	}
	if _, err := os.Stat(filepath.Join(reportsRoot, "report-11.md")); !os.IsNotExist(err) {
		t.Fatal("最旧报告应被归档移出目录")
	}
	if _, err := os.Stat(filepath.Join(m.Root(), "MEMORY.md")); err != nil {
		t.Fatal("长期记忆被误删")
	}
	if len(m.Entries()) != 1 {
		t.Fatal("清理不应影响长期记忆条目")
	}
}

func TestLegacyMigrationWithBackup(t *testing.T) {
	dataRoot := t.TempDir()
	memRoot := filepath.Join(dataRoot, "memory")
	runtimeRoot := filepath.Join(dataRoot, "runtime")
	if err := os.MkdirAll(filepath.Join(memRoot, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(memRoot, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(memRoot, "summaries"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(memRoot, "long_term.json"), `{"entries":[{"id":"old1","kind":"preference","key":"风格","value":"深色","source":"manual"}]}`)
	writeTestFile(t, filepath.Join(memRoot, "user_profile.md"), "# 用户长期偏好\n\n## 手动备注\n\n请记住使用中文\n")
	writeTestFile(t, filepath.Join(memRoot, "projects", "p1.json"), `{"project_id":"p1","name":"旧项目","description":"旧描述","tech_stack":["Vue"],"status":"ready"}`)
	writeTestFile(t, filepath.Join(memRoot, "sessions", "s1.json"), `{"id":"s1","project_id":"p1","title":"旧会话","messages":[{"role":"user","content":"做一个深色官网"}]}`)
	writeTestFile(t, filepath.Join(memRoot, "summaries", "20260101-s1.md"), "# 摘要\n- 会话：s1\n- 项目ID：p1\n## 摘要\n旧摘要\n")
	writeTestFile(t, filepath.Join(dataRoot, ".playwright-mcp", "console.log"), "log")

	m := NewManager(memRoot, nil)
	m.Configure(Options{RuntimeRoot: runtimeRoot})
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(memRoot, "MEMORY.md")); err != nil {
		t.Fatal("未生成 MEMORY.md")
	}
	if _, err := os.Stat(filepath.Join(memRoot, "projects", "p1.md")); err != nil {
		t.Fatal("未生成项目 Markdown")
	}
	digests, _ := filepath.Glob(filepath.Join(memRoot, "sessions", "*", "s1.md"))
	if len(digests) == 0 {
		t.Fatal("旧会话未迁移为月度摘要")
	}
	if _, err := os.Stat(filepath.Join(memRoot, "backup")); err != nil {
		t.Fatal("迁移未生成备份目录")
	}
	if _, err := os.Stat(filepath.Join(memRoot, "long_term.json")); !os.IsNotExist(err) {
		t.Fatal("旧 long_term.json 应已移入备份")
	}
	if _, err := os.Stat(filepath.Join(memRoot, "projects", "p1.json")); !os.IsNotExist(err) {
		t.Fatal("旧项目 json 应已移入备份")
	}
	matches, _ := filepath.Glob(filepath.Join(runtimeRoot, "playwright", "legacy-*", "console.log"))
	if len(matches) == 0 {
		t.Fatal("旧 Playwright 日志未迁移到 runtime")
	}
	if len(m.Entries()) == 0 {
		t.Fatal("旧长期记忆条目未迁入 MEMORY.md")
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeAged(t *testing.T, path, content string, age time.Duration) {
	t.Helper()
	writeTestFile(t, path, content)
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}
