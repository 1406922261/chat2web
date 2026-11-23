package shell

import (
	"path/filepath"
	"regexp"
	"strings"
)

type Decision struct {
	Allowed bool
	Reason  string
}

var allowedBins = map[string]bool{
	"npm": true, "npx": true, "pnpm": true, "yarn": true, "node": true, "bun": true,
	"go": true, "git": true, "python": true, "python3": true, "pip": true, "pip3": true,
	"mkdir": true, "md": true, "echo": true, "dir": true, "ls": true, "type": true,
	"cat": true, "copy": true, "cp": true, "move": true, "mv": true,
	"del": true, "rm": true, "rmdir": true, "ren": true, "rename": true,
	"tsc": true, "vite": true, "where": true, "which": true, "curl": true, "tar": true, "unzip": true,
}

var blockedPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\brm\s+-rf\s+/(\s|$)`),
	regexp.MustCompile(`(?i)\brd\s+/s\s+/q\s+[a-z]:\\?(\s|$)`),
	regexp.MustCompile(`(?i)\b(del|erase)\s+/[fsq]\b.*[a-z]:\\?(\s|$)`),
	regexp.MustCompile(`(?i)\bformat\s+[a-z]:`),
	regexp.MustCompile(`(?i)\bdiskpart\b`),
	regexp.MustCompile(`(?i)\bmkfs\b`),
	regexp.MustCompile(`(?i)\bshutdown\b`),
	regexp.MustCompile(`(?i)\bbcdedit\b`),
	regexp.MustCompile(`(?i)\breg\s+(add|delete|import)\b`),
	regexp.MustCompile(`(?i)\bnet\s+(user|localgroup)\b`),
	regexp.MustCompile(`(?i)\bwmic\b`),
	regexp.MustCompile(`(?i)\bvssadmin\b`),
	regexp.MustCompile(`(?i)\btakeown\b`),
	regexp.MustCompile(`(?i)\bicacls\b`),
	regexp.MustCompile(`(?i)\bsudo\b`),
	regexp.MustCompile(`(?i)(curl|wget)[^|]*\|\s*(sh|bash|powershell|cmd)`),
	regexp.MustCompile(`(?i)\b(iwr|invoke-webrequest|invoke-expression|iex)\b`),
	regexp.MustCompile(`(?i)\bpowershell\b`),
	regexp.MustCompile(`(?i)\bcmd\s*/c\b`),
	regexp.MustCompile(`(?i)\btaskkill\b`),
	regexp.MustCompile(`(?i)c:\\windows|/etc/passwd|/etc/shadow|/root/`),
	regexp.MustCompile(`(?i)\bssh\b|\bscp\b|\btelnet\b|\bnc\s+-`),
}

var destructiveRe = regexp.MustCompile(`(?i)(^|[\s&|;])(del|erase|rm|rmdir|rd)\s`)

var drivePathRe = regexp.MustCompile(`(?i)([a-z]:\\[^\s"']*)`)

// Validate 对 Agent 生成的命令执行安全校验：白名单二进制、危险模式拦截、路径边界检查。
func Validate(command, cwd, root string) Decision {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return Decision{false, "命令为空"}
	}
	if len(cmd) > 4000 {
		return Decision{false, "命令过长"}
	}
	lower := strings.ToLower(cmd)

	for _, re := range blockedPatterns {
		if re.MatchString(cmd) {
			return Decision{false, "包含被禁止的危险操作: " + re.String()}
		}
	}

	if destructiveRe.MatchString(cmd) {
		return Decision{false, "删除类命令必须使用专用 delete_file 工具（需显式确认）"}
	}

	if strings.Contains(lower, "-rf /") || strings.Contains(lower, "--recursive /") {
		return Decision{false, "禁止递归删除根目录"}
	}

	// 路径必须位于 workspace 内
	for _, m := range drivePathRe.FindAllStringSubmatch(cmd, -1) {
		p := strings.Trim(m[1], `"'`)
		if !withinLoose(root, p) {
			return Decision{false, "路径超出允许的 workspace 范围: " + p}
		}
	}
	for _, token := range strings.Fields(cmd) {
		t := strings.Trim(token, `"'`)
		if strings.Contains(t, "..") && !strings.HasPrefix(t, "--") {
			target := filepath.Join(cwd, t)
			if !withinLoose(root, target) {
				return Decision{false, "检测到越界路径: " + t}
			}
		}
	}

	// 逐段校验可执行程序白名单
	for _, seg := range splitSegments(cmd) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(seg), "set ") {
			continue
		}
		fields := strings.Fields(seg)
		if len(fields) == 0 {
			continue
		}
		bin := strings.ToLower(filepath.Base(fields[0]))
		bin = strings.TrimSuffix(bin, ".exe")
		bin = strings.TrimSuffix(bin, ".cmd")
		if !allowedBins[bin] {
			return Decision{false, "命令 " + bin + " 不在允许的执行白名单中"}
		}
	}

	if !within(root, cwd) {
		return Decision{false, "工作目录超出 workspace 范围"}
	}
	return Decision{true, ""}
}

func splitSegments(cmd string) []string {
	replaced := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n", "\r", "\n").Replace(cmd)
	return strings.Split(replaced, "\n")
}

func within(root, path string) bool {
	r, err1 := filepath.Abs(root)
	p, err2 := filepath.Abs(path)
	if err1 != nil || err2 != nil {
		return false
	}
	r = filepath.Clean(r)
	p = filepath.Clean(p)
	if strings.EqualFold(r, p) {
		return true
	}
	rel, err := filepath.Rel(r, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func withinLoose(root, path string) bool {
	if path == "" {
		return false
	}
	if !filepath.IsAbs(path) {
		return true
	}
	return within(root, path)
}
