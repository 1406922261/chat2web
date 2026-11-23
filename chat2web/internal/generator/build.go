package generator

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// BuildResult 是真实构建校验的结果。
type BuildResult struct {
	OK         bool     `json:"ok"`
	Stage      string   `json:"stage"`
	Errors     []string `json:"errors"`
	DurationMs int64    `json:"duration_ms"`
}

// ErrorText 把错误列表压缩为可直接回填模型的简短文本（≤1500 字符）。
func (r BuildResult) ErrorText() string {
	if r.OK {
		return ""
	}
	if len(r.Errors) == 0 {
		return "构建失败（未提取到具体错误）"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "构建失败（阶段 %s）：\n", r.Stage)
	for _, e := range r.Errors {
		b.WriteString("- ")
		b.WriteString(e)
		b.WriteString("\n")
	}
	out := strings.TrimRight(b.String(), "\n")
	runes := []rune(out)
	if len(runes) > 1500 {
		out = string(runes[:1500]) + "\n…（已截断）"
	}
	return out
}

// buildErrRe 提取 TS / Vite / npm 的关键错误行。
var buildErrRe = regexp.MustCompile(`(?i)(error TS\d+|Could not resolve|Failed to resolve import|Internal server error|ERR!|ELIFECYCLE|error during build|Rollup failed)`)

const maxBuildLines = 20

// CheckBuild 在项目目录执行真实的 TypeScript 检查与生产构建。
// 仅当两步都通过时 OK=true，绝不伪造成功。
func CheckBuild(ctx context.Context, dir string, timeout time.Duration) BuildResult {
	start := time.Now()
	result := BuildResult{Stage: "start"}
	if errs := CheckNestedRouter(dir); len(errs) > 0 {
		result.Stage = "router"
		result.Errors = errs
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); err != nil {
		// 新项目尚未安装依赖：先安装，否则无法进行真实构建校验。
		installCtx, installCancel := context.WithTimeout(ctx, timeout)
		installOut, installErr := runBuildCmd(installCtx, dir, "npm install --no-audit --no-fund")
		installCancel()
		if installErr != nil {
			result.Stage = "deps"
			if len(extractBuildErrors(installOut)) > 0 {
				result.Errors = extractBuildErrors(installOut)
			} else {
				result.Errors = []string{"依赖安装失败，请检查网络或 package.json"}
			}
			result.DurationMs = time.Since(start).Milliseconds()
			return result
		}
	}

	bctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tsOut, tsErr := runBuildCmd(bctx, dir, "npx --no-install tsc --noEmit")
	if tsErr != nil && !isMissingTSC(tsOut) {
		result.Stage = "tsc"
		result.Errors = extractBuildErrors(tsOut)
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}

	buildOut, buildErr := runBuildCmd(bctx, dir, "npm run build")
	if buildErr != nil {
		result.Stage = "build"
		result.Errors = extractBuildErrors(buildOut)
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}

	result.OK = true
	result.Stage = "done"
	result.DurationMs = time.Since(start).Milliseconds()
	return result
}

func isMissingTSC(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "could not determine executable") ||
		strings.Contains(lower, "not recognized") ||
		strings.Contains(lower, "not found")
}

// CheckNestedRouter 检查 main.tsx 与 App.tsx 是否同时包裹 Router。
// 嵌套 Router 不会被 tsc 发现，但会在浏览器中抛出
// "You cannot render a <Router> inside another <Router>" 并导致整页白屏。
func CheckNestedRouter(dir string) []string {
	mainSrc, err1 := os.ReadFile(filepath.Join(dir, "src", "main.tsx"))
	appSrc, err2 := os.ReadFile(filepath.Join(dir, "src", "App.tsx"))
	if err1 != nil || err2 != nil {
		return nil
	}
	if routerTagRe.Match(mainSrc) && routerTagRe.Match(appSrc) {
		return []string{
			"src/main.tsx 与 src/App.tsx 同时包裹了 <BrowserRouter>/<HashRouter>，运行时会报 \"You cannot render a <Router> inside another <Router>\" 并导致白屏。请只保留一处 Router：推荐删除 main.tsx 中的 Router 包装，让 App.tsx 提供路由。",
		}
	}
	return nil
}

var routerTagRe = regexp.MustCompile(`<(BrowserRouter|HashRouter|MemoryRouter)\b`)

// TypeCheck 只执行 TypeScript 检查（不跑生产构建），用于打开已有项目前快速拦截白屏风险。
// install=true 时若缺少 node_modules 会先安装依赖。
func TypeCheck(ctx context.Context, dir string, timeout time.Duration, install bool) BuildResult {
	start := time.Now()
	result := BuildResult{Stage: "start"}
	if errs := CheckNestedRouter(dir); len(errs) > 0 {
		result.Stage = "router"
		result.Errors = errs
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	if install {
		if _, err := os.Stat(filepath.Join(dir, "node_modules")); err != nil {
			installCtx, installCancel := context.WithTimeout(ctx, timeout)
			installOut, installErr := runBuildCmd(installCtx, dir, "npm install --no-audit --no-fund")
			installCancel()
			if installErr != nil {
				result.Stage = "deps"
				if errs := extractBuildErrors(installOut); len(errs) > 0 {
					result.Errors = errs
				} else {
					result.Errors = []string{"依赖安装失败，请检查网络或 package.json"}
				}
				result.DurationMs = time.Since(start).Milliseconds()
				return result
			}
		}
	}
	bctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tsOut, tsErr := runBuildCmd(bctx, dir, "npx --no-install tsc --noEmit")
	if tsErr != nil && !isMissingTSC(tsOut) {
		result.Stage = "tsc"
		result.Errors = extractBuildErrors(tsOut)
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}
	result.OK = true
	result.Stage = "done"
	result.DurationMs = time.Since(start).Milliseconds()
	return result
}

func runBuildCmd(ctx context.Context, dir, cmdline string) (string, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", cmdline)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", cmdline)
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CI=1", "NO_COLOR=1", "FORCE_COLOR=0")
	buf := &limitedBuffer{max: 60000}
	cmd.Stdout = buf
	cmd.Stderr = buf
	err := cmd.Run()
	out := buf.String()
	if ctx.Err() == context.DeadlineExceeded {
		out += "\n构建超时"
	}
	return out, err
}

func extractBuildErrors(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !buildErrRe.MatchString(line) {
			continue
		}
		line = clipBuild(line, 300)
		if seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
		if len(out) >= maxBuildLines {
			break
		}
	}
	if len(out) == 0 {
		tail := strings.Split(strings.TrimSpace(raw), "\n")
		for _, line := range tail {
			line = strings.TrimSpace(line)
			if line == "" || seen[line] {
				continue
			}
			seen[line] = true
			out = append(out, clipBuild(line, 300))
			if len(out) >= 8 {
				break
			}
		}
	}
	return out
}

func clipBuild(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 || b.buf.Len() < b.max {
		remain := b.max - b.buf.Len()
		if remain > 0 {
			if len(p) > remain {
				b.buf.Write(p[:remain])
			} else {
				b.buf.Write(p)
			}
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }

var checkImportRe = regexp.MustCompile(`from\s+["'](\.{1,2}/[^"']+)["']`)

// CheckImports 找出被 import 但不存在的本地模块（如 src/App.tsx -> ./pages/Taste）。
func CheckImports(dir string) []string {
	src := filepath.Join(dir, "src")
	var missing []string
	seen := map[string]bool{}
	_ = filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		switch filepath.Ext(path) {
		case ".ts", ".tsx", ".js", ".jsx":
		default:
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		for _, m := range checkImportRe.FindAllStringSubmatch(string(data), -1) {
			spec := m[1]
			if moduleExists(filepath.Join(filepath.Dir(path), spec)) {
				continue
			}
			label := filepath.ToSlash(rel) + " -> " + spec
			if !seen[label] {
				seen[label] = true
				missing = append(missing, label)
			}
			if len(missing) >= 10 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return missing
}

func moduleExists(base string) bool {
	candidates := []string{base, base + ".tsx", base + ".ts", base + ".jsx", base + ".js",
		filepath.Join(base, "index.tsx"), filepath.Join(base, "index.ts"), filepath.Join(base, "index.jsx"), filepath.Join(base, "index.js")}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}
