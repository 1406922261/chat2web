package generator

import (
	"context"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"chat2web/internal/agent/analyzer"
	"chat2web/internal/agent/planner"
)

// Brief 生成注入 LLM 的内容简报：聊天事实 + 网页规划，明确禁止编造。
func Brief(a *analyzer.Analysis, p *planner.SitePlan) string {
	var b strings.Builder
	b.WriteString("## 聊天分析结果（网页内容的事实来源）\n")
	b.WriteString(a.JSON())
	b.WriteString("\n\n## 网页规划（页面结构以此为准，可按实际内容微调）\n")
	b.WriteString(p.JSON())
	b.WriteString("\n\n## 内容红线\n")
	b.WriteString("- 网页中的事实、数字、地点、人物、书名、菜品等只能来自上面的分析结果。\n")
	b.WriteString("- 信息不足时，在页面中明确写出“待补充：…”，禁止编造细节。\n")
	b.WriteString("- 不要输出与聊天主题无关的通用占位文案。\n")
	return b.String()
}

// RequiredFiles 是生成完成必须存在的核心文件。
var RequiredFiles = []string{"package.json", "index.html", "src/main.tsx", "src/App.tsx"}

// Validate 校验项目是否已生成可运行的网页骨架。
func Validate(dir string) error {
	var missing []string
	for _, rel := range RequiredFiles {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Stat(p)
		if err != nil || info.IsDir() || info.Size() == 0 {
			missing = append(missing, rel)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少关键文件: %s", strings.Join(missing, ", "))
	}
	return nil
}

// RenderResult 是真实浏览器渲染检查的结果。
type RenderResult struct {
	Checked bool   `json:"checked"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail"`
}

var (
	headRe        = regexp.MustCompile(`(?is)<head[^>]*>.*?</head>`)
	scriptStyleRe = regexp.MustCompile(`(?is)<(script|style|noscript)[^>]*>.*?</(script|style|noscript)>`)
	anyTagRe      = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRe       = regexp.MustCompile(`\s+`)
	consoleErrRe  = regexp.MustCompile(`(?i)Uncaught (Error|TypeError|ReferenceError|RangeError|SyntaxError)[^"\\]*`)
)

// RenderCheck 用本机 Edge/Chrome 的无头模式真实加载页面，确认渲染后存在可见内容。
// 未找到浏览器时返回 Checked=false 且 OK=true（不阻塞任务），并明确说明未验证渲染。
func RenderCheck(ctx context.Context, url, keyword string, timeout time.Duration) RenderResult {
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	exe := findBrowser()
	if exe == "" {
		return RenderResult{Checked: false, OK: true, Detail: "未找到 Edge/Chrome，跳过浏览器渲染检查"}
	}
	profile, err := os.MkdirTemp("", "chat2web-render-*")
	if err != nil {
		profile = filepath.Join(os.TempDir(), fmt.Sprintf("chat2web-render-%d", time.Now().UnixNano()))
		_ = os.MkdirAll(profile, 0o755)
	}
	defer os.RemoveAll(profile)

	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-background-networking",
		"--hide-scrollbars",
		"--virtual-time-budget=10000",
		"--user-data-dir=" + profile,
		"--dump-dom",
		url,
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, exe, args...)
	cmd.WaitDelay = 5 * time.Second
	stdout := &limitedBuffer{max: 3_000_000}
	stderr := &limitedBuffer{max: 4000}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil && stdout.String() == "" {
		return RenderResult{Checked: true, OK: false, Detail: "浏览器无法加载页面：" + clipBuild(err.Error(), 300)}
	}

	dom := stdout.String()
	return analyzeDOM(dom, extractConsoleError(stderr.String()), keyword)
}

// analyzeDOM 判断无头浏览器导出的 DOM 是否真正渲染出了内容。
func analyzeDOM(dom, consoleErr, keyword string) RenderResult {
	if strings.Contains(strings.ToLower(dom), "vite-error-overlay") {
		return RenderResult{Checked: true, OK: false, Detail: "页面出现 Vite 错误覆盖层：" + clipBuild(compact(visibleText(dom)), 300)}
	}
	text := visibleText(dom)
	n := len([]rune(text))
	if consoleErr != "" && n < 200 {
		return RenderResult{Checked: true, OK: false, Detail: "页面存在运行时错误：" + clipBuild(consoleErr, 300) + "；可见文本：" + clipBuild(compact(text), 120)}
	}
	if n < 40 {
		detail := fmt.Sprintf("页面渲染后可见内容过少（%d 字）", n)
		if keyword != "" {
			detail += fmt.Sprintf("，未匹配站点标题「%s」", keyword)
		}
		if consoleErr != "" {
			detail += "；浏览器控制台错误：" + clipBuild(consoleErr, 200)
		}
		return RenderResult{Checked: true, OK: false, Detail: detail}
	}
	return RenderResult{Checked: true, OK: true, Detail: fmt.Sprintf("浏览器渲染通过，可见文本 %d 字", n)}
}

// extractConsoleError 从浏览器 stderr 日志中提取未捕获异常，作为白屏诊断依据。
func extractConsoleError(log string) string {
	m := consoleErrRe.FindString(log)
	if m == "" {
		return ""
	}
	return clipBuild(compact(m), 240)
}

func visibleText(dom string) string {
	s := headRe.ReplaceAllString(dom, " ")
	s = scriptStyleRe.ReplaceAllString(s, " ")
	s = anyTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return spaceRe.ReplaceAllString(s, " ")
}

func compact(s string) string {
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// findBrowser 依次查找 Edge、Chrome；可用 CHAT2WEB_BROWSER 指定完整路径。
func findBrowser() string {
	if p := strings.TrimSpace(os.Getenv("CHAT2WEB_BROWSER")); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	candidates := []string{
		`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, name := range []string{"msedge.exe", "chrome.exe", "msedge", "google-chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}
