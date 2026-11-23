package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"chat2web/internal/planner"
)

func TestWriteBaselineSite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	placeholder := "export default function App() {\n  return <div>页面正在生成中</div>\n}\n"
	if err := os.WriteFile(filepath.Join(src, "App.tsx"), []byte(placeholder), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := &planner.SitePlan{
		SiteName: "晨光早餐指南",
		Pages: []planner.PagePlan{
			{Name: "首页", Path: "/", Purpose: "展示推荐早餐", Sections: []planner.SectionPlan{{Title: "今日推荐"}}},
			{Name: "早餐详情", Path: "/breakfast/:id", Purpose: "展示单个早餐"},
		},
	}
	files, ok := writeBaselineSite(dir, plan)
	if !ok {
		t.Fatal("基线站点未生成")
	}
	if len(files) < 3 {
		t.Fatalf("生成文件过少：%v", files)
	}
	if placeholderApp(dir) {
		t.Fatal("App.tsx 仍是占位页")
	}
	app, err := os.ReadFile(filepath.Join(src, "App.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), "晨光早餐指南") || !strings.Contains(string(app), "Routes") {
		t.Fatalf("App.tsx 内容不完整：%s", string(app))
	}
}

func TestWriteBaselineSiteSkipsRealApp(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	real := "export default function App() { return <div>真实页面</div> }\n"
	if err := os.WriteFile(filepath.Join(src, "App.tsx"), []byte(real), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := writeBaselineSite(dir, &planner.SitePlan{SiteName: "x"}); ok {
		t.Fatal("不应覆盖已完成的入口")
	}
}
