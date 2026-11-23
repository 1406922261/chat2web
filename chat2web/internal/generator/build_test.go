package generator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckNestedRouter(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, "src", "main.tsx")
	appPath := filepath.Join(dir, "src", "App.tsx")
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(appPath, `export default function App(){ return <BrowserRouter><Routes/></BrowserRouter> }`)
	write(mainPath, `createRoot(el).render(<BrowserRouter><App/></BrowserRouter>)`)
	if errs := CheckNestedRouter(dir); len(errs) == 0 {
		t.Fatal("双重 Router 应被检测出来")
	}
	write(mainPath, `createRoot(el).render(<App/>)`)
	if errs := CheckNestedRouter(dir); len(errs) != 0 {
		t.Fatalf("单层 Router 不应报错: %v", errs)
	}
}

func TestCheckBuildEmptyProjectFails(t *testing.T) {
	dir := t.TempDir()
	res := CheckBuild(context.Background(), dir, 20*time.Second)
	if res.OK {
		t.Fatal("空项目不应通过构建")
	}
	if res.Stage == "" || res.Stage == "done" {
		t.Fatalf("Stage = %s, want 失败阶段", res.Stage)
	}
	if res.ErrorText() == "" {
		t.Fatal("应返回错误说明")
	}
}

func TestExtractBuildErrorsDedup(t *testing.T) {
	raw := "src/App.tsx(3,10): error TS2307: Cannot find module './pages/Taste'\n" +
		"src/App.tsx(3,10): error TS2307: Cannot find module './pages/Taste'\n" +
		"Failed to resolve import \"./pages/Taste\" from \"src/App.tsx\"\n" +
		"npm ERR! code ELIFECYCLE\n"
	errs := extractBuildErrors(raw)
	if len(errs) < 2 {
		t.Fatalf("错误提取过少：%v", errs)
	}
	if len(errs) > maxBuildLines {
		t.Fatalf("错误未限制数量：%d", len(errs))
	}
	joined := strings.Join(errs, "\n")
	if strings.Count(joined, "TS2307") != 1 {
		t.Fatalf("重复错误未去重：%s", joined)
	}
}

func TestErrorTextLimit(t *testing.T) {
	res := BuildResult{Stage: "build", Errors: []string{strings.Repeat("错", 400)}}
	text := res.ErrorText()
	if len([]rune(text)) > 1600 {
		t.Fatalf("错误文本超长：%d", len([]rune(text)))
	}
}

func TestCheckImports(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	app := "import Taste from './pages/Taste'\nimport Home from './pages/Home'\n"
	if err := os.WriteFile(filepath.Join(src, "App.tsx"), []byte(app), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "pages"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "pages", "Home.tsx"), []byte("export default function Home(){return null}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := CheckImports(dir)
	if len(missing) != 1 || !strings.Contains(missing[0], "pages/Taste") {
		t.Fatalf("missing = %v", missing)
	}
}
