package shell

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNormalizeWindowsCommand(t *testing.T) {
	got := normalizeWindowsCommand(`curl -s -o /dev/null -w "%{http_code}" http://example.com 2>/dev/null`)
	if strings.Contains(got, "/dev/null") {
		t.Fatalf("命令仍包含 /dev/null: %s", got)
	}
	if strings.Count(got, "NUL") != 2 {
		t.Fatalf("期望 2 处 NUL，实际: %s", got)
	}
}

func TestRunWithDevNullRedirect(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅在 Windows 上验证 cmd 重定向")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("未安装 curl")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	e := NewExecutor(dir, 30*time.Second, 4000)
	res := e.Run(context.Background(), `curl -s -o /dev/null -w "%{http_code}" `+srv.URL, dir)
	if res.ExitCode != 0 {
		t.Fatalf("退出码 = %d，blocked=%q，stderr=%s", res.ExitCode, res.Blocked, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "200") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
}
