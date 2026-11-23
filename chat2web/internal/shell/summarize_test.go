package shell

import (
	"strings"
	"testing"
)

func TestSummarizeExtractsErrorsAndLimits(t *testing.T) {
	res := Result{
		ExitCode:   1,
		DurationMs: 1234,
		Allowed:    true,
		Stdout:     "vite v5\nbuilding...\n",
		Stderr: "src/App.tsx(3,10): error TS2307: Cannot find module './pages/Taste'\n" +
			"src/App.tsx(3,10): error TS2307: Cannot find module './pages/Taste'\n" +
			"npm ERR! code ELIFECYCLE\n",
	}
	out := Summarize(res, 1500)
	if !strings.Contains(out, "exit=1") {
		t.Fatalf("缺少退出码：%s", out)
	}
	if !strings.Contains(out, "TS2307") {
		t.Fatalf("缺少关键错误：%s", out)
	}
	if strings.Count(out, "Cannot find module") != 1 {
		t.Fatalf("错误未去重：%s", out)
	}
	if len([]rune(out)) > 1600 {
		t.Fatalf("摘要超长：%d", len([]rune(out)))
	}
}

func TestSummarizeRespectsMaxChars(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("error failed line\n")
	}
	res := Result{ExitCode: 2, Stdout: sb.String(), Allowed: true}
	out := Summarize(res, 200)
	if len([]rune(out)) > 260 {
		t.Fatalf("未按 maxChars 截断：%d", len([]rune(out)))
	}
}

func TestSummarizeBlocked(t *testing.T) {
	res := Result{ExitCode: -1, Allowed: false, Blocked: "危险命令"}
	out := Summarize(res, 500)
	if !strings.Contains(out, "blocked=危险命令") {
		t.Fatalf("缺少拦截信息：%s", out)
	}
}
