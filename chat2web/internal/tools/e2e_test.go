package tools

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chat2web/internal/project"
	"chat2web/internal/shell"
)

// TestScaffoldInstallDevServer 验证网页生成引擎的真实链路：
// 脚手架创建文件 → npm install → npm run dev → HTTP 访问 → 停止进程。
// 默认跳过；使用 CHAT2WEB_E2E=1 go test ./internal/tools -run TestScaffoldInstallDevServer -v 执行。
func TestScaffoldInstallDevServer(t *testing.T) {
	if os.Getenv("CHAT2WEB_E2E") != "1" {
		t.Skip("设置 CHAT2WEB_E2E=1 以运行真实依赖安装与 dev server 集成测试")
	}
	base := t.TempDir()
	projects := project.NewManager(filepath.Join(base, "data"), filepath.Join(base, "workspace"))
	p, err := projects.Create("E2E 测试站", "自动验收")
	if err != nil {
		t.Fatal(err)
	}
	exec := shell.NewExecutor(projects.WorkspaceRoot(), 5*time.Minute, 24000)
	reg := NewRegistry()
	RegisterBuiltins(reg, Deps{
		ProjectID:   p.ID,
		ProjectName: p.Name,
		ProjectDir:  projects.SiteDir(p.ID),
		Root:        base,
		Shell:       exec,
		Projects:    projects,
	})
	disp := NewDispatcher(reg)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	out, err := disp.Dispatch(ctx, "scaffold_project", `{"stack":"react-ts-tailwind","title":"E2E 测试站"}`)
	if err != nil {
		t.Fatalf("scaffold 失败: %v", err)
	}
	t.Logf("scaffold: %s", out)
	for _, rel := range []string{"package.json", "src/App.tsx", "index.html", "tailwind.config.js"} {
		if _, err := os.Stat(filepath.Join(projects.SiteDir(p.ID), filepath.FromSlash(rel))); err != nil {
			t.Fatalf("缺少生成文件 %s: %v", rel, err)
		}
	}

	start := time.Now()
	out, err = disp.Dispatch(ctx, "shell_exec", `{"command":"npm install"}`)
	if err != nil {
		t.Fatalf("npm install 失败: %v\n%s", err, out)
	}
	t.Logf("npm install 完成，耗时 %s", time.Since(start).Round(time.Second))

	out, err = disp.Dispatch(ctx, "dev_server", `{"action":"start"}`)
	if err != nil {
		t.Fatalf("dev server 启动失败: %v\n%s", err, out)
	}
	t.Logf("dev server: %s", out)
	url := extractURL(out)
	if url == "" {
		t.Fatalf("未获得运行地址: %s", out)
	}

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("访问预览地址失败: %v", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "id=\"root\"") {
		t.Fatalf("页面返回异常: status=%d body=%s", resp.StatusCode, string(body[:min(200, len(body))]))
	}
	t.Logf("HTTP 预览成功: %s（%d 字节）", url, len(body))

	if _, err := disp.Dispatch(ctx, "dev_server", `{"action":"stop"}`); err != nil {
		t.Fatalf("停止 dev server 失败: %v", err)
	}
	exec.StopByName("dev-" + p.ID)
}

func extractURL(s string) string {
	for _, f := range strings.Fields(s) {
		if strings.HasPrefix(f, "http://") || strings.HasPrefix(f, "https://") {
			return strings.TrimRight(f, "。,;；)")
		}
	}
	return ""
}
