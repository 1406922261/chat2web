package project

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	base := t.TempDir()
	return NewManager(filepath.Join(base, "data"), filepath.Join(base, "workspace"))
}

func TestCreateWriteReadTree(t *testing.T) {
	m := newTestManager(t)
	p, err := m.Create("测试作品集", "深色科技风个人作品集")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.Status != "draft" || p.SessionID == "" {
		t.Fatalf("项目初始化字段不正确: %+v", p)
	}

	if err := m.WriteFile(p.ID, "src/App.tsx", "export default function App() { return <div>hi</div> }"); err != nil {
		t.Fatal(err)
	}
	content, err := m.ReadFile(p.ID, "src/App.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if content == "" {
		t.Fatal("读取文件内容为空")
	}

	tree, err := m.Tree(p.ID, 4)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	var walk func(FileNode)
	walk = func(n FileNode) {
		if n.Path == "src/App.tsx" {
			found = true
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(tree)
	if !found {
		t.Fatalf("文件树未包含 src/App.tsx: %+v", tree)
	}

	if _, err := m.ReadFile(p.ID, "../../etc/passwd"); err == nil {
		t.Fatal("越界读取应被拒绝")
	}
}

func TestDeleteRestoreAndRename(t *testing.T) {
	m := newTestManager(t)
	p, err := m.Create("旧名称", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(m.SiteDir(p.ID), "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Get(p.ID); got == nil || !got.Deleted {
		t.Fatalf("软删除后应标记 deleted")
	}
	if len(m.List(false)) != 0 {
		t.Fatalf("默认列表不应包含已删除项目")
	}
	if len(m.List(true)) != 1 {
		t.Fatalf("回收站列表应包含 1 个项目")
	}
	if _, err := m.Restore(p.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Get(p.ID); got == nil || got.Deleted {
		t.Fatalf("恢复后不应标记 deleted")
	}
	renamed, err := m.Rename(p.ID, "新名称")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "新名称" {
		t.Fatalf("重命名失败: %s", renamed.Name)
	}
}
