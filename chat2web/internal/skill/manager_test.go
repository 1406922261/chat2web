package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, dir, name, desc, triggers, body string) {
	t.Helper()
	full := filepath.Join(root, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := "---\nname: " + name + "\ndescription: " + desc + "\ntriggers: " + triggers + "\ntags: test\n---\n\n" + body
	if err := os.WriteFile(filepath.Join(full, "SKILL.md"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanAndMatch(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "backend", "backend-dev", "后端接口登录订单数据库", "登录,订单,数据库,api", "# 后端规范\n必须实现鉴权与订单接口。")
	writeSkill(t, root, "ui", "ui-design", "深色风格视觉设计", "深色,配色,布局", "# UI 规范\n使用暗色科技风格。")

	m := NewManager(root)
	count, err := m.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("期望加载 2 个 Skill，实际 %d", count)
	}
	if _, ok := m.Get("backend-dev"); !ok {
		t.Fatalf("未加载 backend-dev")
	}

	matches := m.Match("制作在线商城，需要登录、订单和数据库", 2)
	if len(matches) == 0 || matches[0].Skill.Name != "backend-dev" {
		t.Fatalf("期望匹配 backend-dev，实际: %+v", matches)
	}
	ctx := m.BuildContext(matches, 4000)
	if !strings.Contains(ctx, "### Skill: backend-dev") || !strings.Contains(ctx, "鉴权与订单接口") {
		t.Fatalf("Skill 上下文注入内容不正确: %s", ctx)
	}
}

func TestScanDirDynamicLoad(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "custom", "custom-skill", "自定义技能", "测试", "# 自定义\n动态加载内容。")
	m := NewManager(root)
	sk, err := m.ScanDir(filepath.Join(root, "custom"))
	if err != nil {
		t.Fatal(err)
	}
	if sk.Name != "custom-skill" || !strings.Contains(sk.Body, "动态加载") {
		t.Fatalf("动态加载解析失败: %+v", sk)
	}
	if len(m.List()) != 1 {
		t.Fatalf("期望 1 个 Skill，实际 %d", len(m.List()))
	}
}
