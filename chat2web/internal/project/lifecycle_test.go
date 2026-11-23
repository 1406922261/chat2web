package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chat2web/internal/textutil"
)

func newLifecycleTestManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	return NewManager(filepath.Join(root, "data"), filepath.Join(root, "workspace", "projects"))
}

func TestLifecycleLayoutSaveArchiveRestore(t *testing.T) {
	m := newLifecycleTestManager(t)
	p, err := m.Create("测试项目", "测试")
	if err != nil {
		t.Fatal(err)
	}
	if p.Lifecycle != LifecycleTemporary {
		t.Fatalf("新建项目应为临时项目，实际 %s", p.Lifecycle)
	}
	for _, dir := range []string{m.SiteDir(p.ID), m.AssetsDir(p.ID), m.OutputDir(p.ID)} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("统一目录缺失：%s", dir)
		}
	}
	if _, err := os.Stat(filepath.Join(m.ProjectDir(p.ID), "project.json")); err != nil {
		t.Fatalf("项目目录缺少 project.json：%v", err)
	}
	if len(m.FormalProjects()) != 0 {
		t.Fatal("临时项目不应出现在正式项目列表")
	}

	// 写入源码、用户素材与记忆
	if err := os.WriteFile(filepath.Join(m.SiteDir(p.ID), "App.tsx"), []byte("app"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.AssetsDir(p.ID), "photo.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.MemoryFile(p.ID), []byte("# 项目记忆"), 0o644); err != nil {
		t.Fatal(err)
	}

	saved, err := m.Save(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Lifecycle != LifecycleSaved || saved.SavedAt == nil {
		t.Fatal("保存后应进入 saved 且记录保存时间")
	}
	if len(m.FormalProjects()) != 1 || len(m.TemporaryProjects()) != 0 {
		t.Fatal("保存后正式/临时项目列表不正确")
	}

	archived, err := m.Archive(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Lifecycle != LifecycleArchived || archived.ArchivedAt == nil {
		t.Fatal("归档状态不正确")
	}
	if len(m.SelectableProjects()) != 0 {
		t.Fatal("归档项目不应出现在可继续列表")
	}

	if _, err := m.Unarchive(p.ID); err != nil {
		t.Fatal(err)
	}
	if len(m.FormalProjects()) != 1 {
		t.Fatal("归档恢复后应回到已保存状态")
	}

	if err := m.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.ProjectDir(p.ID)); !os.IsNotExist(err) {
		t.Fatal("删除后项目目录应移入回收站")
	}
	if _, err := m.Restore(p.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(m.SiteDir(p.ID), "App.tsx"),
		filepath.Join(m.AssetsDir(p.ID), "photo.png"),
		m.MemoryFile(p.ID),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("恢复后文件丢失：%s", path)
		}
	}
}

func TestMatchReusesSameProject(t *testing.T) {
	m := newLifecycleTestManager(t)
	chat := "我想吃涮羊肉，做一个北京涮羊肉攻略网页，包含推荐店铺和吃法"
	if m.Match(chat) != nil {
		t.Fatal("初始不应匹配到项目")
	}
	p, err := m.Create(textutil.TitleFromMessage(chat), chat)
	if err != nil {
		t.Fatal(err)
	}
	// 连续 20 次同样的修改请求都应命中同一个项目。
	for i := 0; i < 20; i++ {
		got := m.Match(chat)
		if got == nil || got.ID != p.ID {
			t.Fatalf("第 %d 次修改未复用到同一项目", i+1)
		}
	}
	if len(m.List(false)) != 1 {
		t.Fatalf("应只有 1 个项目，实际 %d 个", len(m.List(false)))
	}
}

func TestPurgeTemporaryKeepsSavedProjects(t *testing.T) {
	m := newLifecycleTestManager(t)
	temp, _ := m.Create("临时页面", "临时")
	saved, _ := m.Create("重要页面", "重要")
	if _, err := m.Save(saved.ID); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -30)
	// 直接调整元数据中的更新时间，模拟长期未使用的临时项目。
	temp.UpdatedAt = old
	raw, _ := json.MarshalIndent(temp, "", "  ")
	if err := os.WriteFile(filepath.Join(filepath.Dir(m.WorkspaceRoot()), "..", "data", "projects", temp.ID+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	ids, err := m.PurgeTemporary(7, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != temp.ID {
		t.Fatalf("应只清理过期临时项目，实际 %v", ids)
	}
	if _, err := m.Get(saved.ID); err != nil {
		t.Fatal("已保存项目不能被子清理影响")
	}
	if len(m.FormalProjects()) != 1 {
		t.Fatal("已保存项目应仍然存在")
	}
}

func TestSavedProjectSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	projectsRoot := filepath.Join(root, "workspace", "projects")
	m1 := NewManager(dataRoot, projectsRoot)
	p, err := m1.Create("重启用项目", "重启验证")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m1.Save(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m1.MemoryFile(p.ID), []byte("# 记忆"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 模拟 Agent 重启：新实例从磁盘恢复项目与生命周期。
	m2 := NewManager(dataRoot, projectsRoot)
	got, err := m2.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lifecycle != LifecycleSaved || got.SavedAt == nil {
		t.Fatal("重启后项目生命周期未恢复")
	}
	if len(m2.FormalProjects()) != 1 {
		t.Fatal("重启后正式项目列表未恢复")
	}
	if _, err := os.Stat(m2.MemoryFile(p.ID)); err != nil {
		t.Fatal("重启后项目记忆丢失")
	}
}

func TestMigrateLayout(t *testing.T) {
	id := "project-20260101-000000-abcdef"
	root := t.TempDir()
	m := NewManager(filepath.Join(root, "data"), filepath.Join(root, "workspace", "projects"))

	legacySite := filepath.Join(root, "workspace", "generated-websites", id)
	legacyMem := filepath.Join(root, "data", "memory", "projects")
	if err := os.MkdirAll(filepath.Join(legacySite, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(legacyMem, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(legacySite, "package.json"), []byte("{}"), 0o644)
	_ = os.WriteFile(filepath.Join(legacySite, "dist", "index.html"), []byte("<html>"), 0o644)
	_ = os.WriteFile(filepath.Join(legacyMem, id+".md"), []byte("# 旧项目记忆"), 0o644)

	p := Project{ID: id, Name: "旧项目", Status: "generated", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	raw, _ := json.Marshal(p)
	if err := os.MkdirAll(filepath.Join(root, "data", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data", "projects", id+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	rep := m.MigrateLayout(filepath.Join(root, "workspace", "generated-websites"), legacyMem)
	if rep.MovedSource != 1 || rep.MovedMemory != 1 {
		t.Fatalf("迁移统计不正确：%+v", rep)
	}
	for _, path := range []string{
		filepath.Join(m.SiteDir(id), "package.json"),
		filepath.Join(m.OutputDir(id), "index.html"),
		m.MemoryFile(id),
		filepath.Join(m.ProjectDir(id), "project.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("迁移后缺少：%s", path)
		}
	}
	migrated, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Lifecycle != LifecycleSaved {
		t.Fatalf("旧项目应默认为已保存，实际 %s", migrated.Lifecycle)
	}
	if !strings.Contains(migrated.Dir, filepath.Join("projects", id, "source")) {
		t.Fatalf("源码目录不在统一结构内：%s", migrated.Dir)
	}
}
