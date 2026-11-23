package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"chat2web/internal/config"
	"chat2web/internal/mcp"
	"chat2web/internal/memory"
	"chat2web/internal/project"
	"chat2web/internal/shell"
	"chat2web/internal/skill"
	"chat2web/internal/textutil"
)

// Deps 是每次 Agent 运行时的工具依赖，绑定当前项目目录。
type Deps struct {
	ProjectID   string
	ProjectName string
	ProjectDir  string
	Root        string

	Shell    *shell.Executor
	Skills   *skill.Manager
	Memory   *memory.Manager
	Projects *project.Manager
	MCP      *mcp.Manager
	Config   *config.Config
}

// RegisterBuiltins 注册内置工具（文件、Shell、生成、测试、Skill 等）。
func RegisterBuiltins(reg *Registry, d Deps) {
	reg.Register(writeFileTool(d))
	reg.Register(readFileTool(d))
	reg.Register(listFilesTool(d))
	reg.Register(deleteFileTool(d))
	reg.Register(shellTool(d))
	reg.Register(scaffoldTool(d))
	reg.Register(createReactProjectTool(d))
	reg.Register(devServerTool(d))
	reg.Register(webTestTool(d))
	reg.Register(skillListTool(d))
	reg.Register(skillLoadTool(d))
	reg.Register(projectStatusTool(d))
}

func writeFileTool(d Deps) Tool {
	return NewFuncTool(
		"write_file",
		"在项目目录中创建或覆盖文件。生成网页代码时必须使用此工具写入真实文件。path 为项目内相对路径，例如 src/pages/Home.tsx。",
		objSchema(map[string]any{
			"path":    strProp("项目内的相对路径，如 src/App.tsx"),
			"content": strProp("完整文件内容"),
		}, "path", "content"),
		func(ctx context.Context, args map[string]any) (string, error) {
			rel := getString(args, "path", "")
			content := getString(args, "content", "")
			if rel == "" {
				return "", errors.New("path 不能为空")
			}
			full, err := safeJoin(d.ProjectDir, rel)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("已写入 %s（%d 字节）", filepath.ToSlash(rel), len(content)), nil
		},
	)
}

func readFileTool(d Deps) Tool {
	return NewFuncTool(
		"read_file",
		"读取项目内文件内容，用于检查已生成的代码、定位错误。",
		objSchema(map[string]any{"path": strProp("项目内相对路径")}, "path"),
		func(ctx context.Context, args map[string]any) (string, error) {
			full, err := safeJoin(d.ProjectDir, getString(args, "path", ""))
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return "", fmt.Errorf("读取失败: %w", err)
			}
			return textutil.Truncate(string(data), 24000), nil
		},
	)
}

func listFilesTool(d Deps) Tool {
	return NewFuncTool(
		"list_files",
		"列出项目文件树（自动跳过 node_modules、dist 等目录），用于了解当前项目结构。",
		objSchema(map[string]any{"depth": intProp("遍历深度，默认 4")}),
		func(ctx context.Context, args map[string]any) (string, error) {
			depth := getInt(args, "depth", 4)
			if d.Projects == nil || d.ProjectID == "" {
				return renderDir(d.ProjectDir, depth)
			}
			tree, err := d.Projects.Tree(d.ProjectID, depth)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			renderNode(tree, "", &b)
			if b.Len() == 0 {
				return "（项目目录为空）", nil
			}
			return b.String(), nil
		},
	)
}

func deleteFileTool(d Deps) Tool {
	return NewFuncTool(
		"delete_file",
		"删除项目内的单个文件。必须显式传入 confirm=true 才会执行，用于清理错误文件。",
		objSchema(map[string]any{
			"path":    strProp("项目内相对路径"),
			"confirm": boolProp("必须为 true 确认删除"),
		}, "path", "confirm"),
		func(ctx context.Context, args map[string]any) (string, error) {
			if !getBool(args, "confirm", false) {
				return "", errors.New("删除操作需要 confirm=true 显式确认")
			}
			rel := getString(args, "path", "")
			if rel == "" {
				return "", errors.New("path 不能为空")
			}
			full, err := safeJoin(d.ProjectDir, rel)
			if err != nil {
				return "", err
			}
			info, err := os.Stat(full)
			if err != nil {
				return "", err
			}
			if info.IsDir() {
				return "", errors.New("delete_file 只允许删除文件，不允许删除目录")
			}
			if err := os.Remove(full); err != nil {
				return "", err
			}
			return fmt.Sprintf("已删除文件 %s（操作已记录）", filepath.ToSlash(rel)), nil
		},
	)
}

func shellTool(d Deps) Tool {
	return NewFuncTool(
		"shell_exec",
		"在项目目录中执行开发命令（npm install、npm run build 等）。命令会经过安全校验、超时控制与输出截断。不要用于删除文件，删除请用 delete_file。",
		objSchema(map[string]any{
			"command":         strProp("要执行的命令，例如 npm install"),
			"cwd":             strProp("可选，项目内相对工作目录"),
			"timeout_seconds": intProp("可选，超时秒数，默认 300"),
		}, "command"),
		func(ctx context.Context, args map[string]any) (string, error) {
			command := strings.TrimSpace(getString(args, "command", ""))
			if command == "" {
				return "", errors.New("command 不能为空")
			}
			dir := d.ProjectDir
			if rel := getString(args, "cwd", ""); rel != "" {
				full, err := safeJoin(d.ProjectDir, rel)
				if err != nil {
					return "", err
				}
				dir = full
			}
			timeout := getInt(args, "timeout_seconds", 300)
			runCtx := ctx
			if timeout > 0 {
				var cancel context.CancelFunc
				runCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
				defer cancel()
			}
			res := d.Shell.Run(runCtx, command, dir)
			var b strings.Builder
			fmt.Fprintf(&b, "命令: %s\n退出码: %d  耗时: %dms\n", res.Command, res.ExitCode, res.DurationMs)
			if res.Blocked != "" {
				fmt.Fprintf(&b, "安全拦截: %s\n", res.Blocked)
			}
			if res.Stdout != "" {
				fmt.Fprintf(&b, "\n[stdout]\n%s\n", textutil.Truncate(res.Stdout, 6000))
			}
			if res.Stderr != "" {
				fmt.Fprintf(&b, "\n[stderr]\n%s\n", textutil.Truncate(res.Stderr, 3000))
			}
			if res.TimedOut {
				b.WriteString("\n[超时] 命令执行超时\n")
			}
			if res.Blocked != "" {
				return b.String(), errors.New(res.Blocked)
			}
			if res.ExitCode != 0 {
				return b.String(), fmt.Errorf("命令失败，退出码 %d", res.ExitCode)
			}
			return b.String(), nil
		},
	)
}

func scaffoldTool(d Deps) Tool {
	return NewFuncTool(
		"scaffold_project",
		"初始化网站项目骨架。stack 支持 react-ts-tailwind（默认，推荐）或 static。仅当项目为空时执行，随后用 write_file 生成真实页面。",
		objSchema(map[string]any{
			"stack": strProp("react-ts-tailwind 或 static"),
			"title": strProp("网站标题/项目名"),
		}),
		func(ctx context.Context, args map[string]any) (string, error) {
			if d.ProjectDir == "" {
				return "", errors.New("当前没有绑定项目目录")
			}
			if _, err := os.Stat(filepath.Join(d.ProjectDir, "package.json")); err == nil {
				return "项目骨架已存在（package.json 已生成），无需重复创建，请直接用 write_file 生成页面。", nil
			}
			stack := getString(args, "stack", "react-ts-tailwind")
			title := getString(args, "title", d.ProjectName)
			count, err := WriteScaffold(d.ProjectDir, title, stack)
			if err != nil {
				return "", err
			}
			if d.Projects != nil && d.ProjectID != "" {
				_, _ = d.Projects.UpdateFunc(d.ProjectID, func(p *project.Project) {
					if stack == "static" {
						p.TechStack = []string{"HTML", "CSS", "JavaScript"}
					} else {
						p.TechStack = []string{"React", "TypeScript", "Vite", "Tailwind CSS"}
					}
					p.Status = "generating"
				})
				d.Projects.AddHistory(d.ProjectID, "脚手架", "初始化项目骨架: "+stack)
			}
			return fmt.Sprintf("项目骨架已创建（%s，共 %d 个基础文件）。接下来请用 write_file 生成真实页面。", stack, count), nil
		},
	)
}

// startDevServer 确保依赖可用并启动开发服务器，返回访问地址与过程说明。
func startDevServer(ctx context.Context, d Deps, name string) (string, string, error) {
	procs := d.Shell.Processes()
	for i := range procs {
		p := &procs[i]
		if p.Name != name || p.Status != "running" {
			continue
		}
		if p.URL != "" {
			return p.URL, "开发服务器已在运行", nil
		}
		if u := d.Shell.WaitForURL(ctx, p.ID, 20*time.Second); u != "" {
			return u, "开发服务器已在运行", nil
		}
	}
	if _, err := os.Stat(filepath.Join(d.ProjectDir, "package.json")); err != nil {
		return "", "", errors.New("项目缺少 package.json，请先 scaffold_project 创建项目")
	}
	notes := []string{}
	if _, err := os.Stat(filepath.Join(d.ProjectDir, "node_modules")); err != nil {
		installed := time.Now()
		runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		res := d.Shell.Run(runCtx, "npm install --no-audit --no-fund --prefer-offline --loglevel=error", d.ProjectDir)
		cancel()
		if res.Blocked != "" {
			return "", "", fmt.Errorf("自动安装依赖被安全拦截: %s", res.Blocked)
		}
		if res.ExitCode != 0 || res.TimedOut {
			return "", "", fmt.Errorf("自动安装依赖失败（退出码 %d）:\n%s", res.ExitCode, textutil.Truncate(res.Stderr+"\n"+res.Stdout, 3000))
		}
		notes = append(notes, fmt.Sprintf("已自动安装依赖（%.0f 秒，命令合并执行）", time.Since(installed).Seconds()))
	}
	port := freePort()
	command := fmt.Sprintf("npm run dev -- --host 127.0.0.1 --port %d --strictPort", port)
	proc, err := d.Shell.StartProcess(ctx, name, command, d.ProjectDir)
	if err != nil {
		return "", "", err
	}
	url := d.Shell.WaitForURL(ctx, proc.ID, 90*time.Second)
	if url == "" {
		logs := proc.Output(40)
		_ = d.Shell.StopProcess(proc.ID)
		return "", "", fmt.Errorf("开发服务器 90 秒内未就绪。最近输出:\n%s", textutil.Truncate(logs, 4000))
	}
	if d.Projects != nil && d.ProjectID != "" {
		_, _ = d.Projects.UpdateFunc(d.ProjectID, func(p *project.Project) {
			p.DevURL = url
			p.DevPort = port
			p.Status = "ready"
		})
		d.Projects.AddHistory(d.ProjectID, "运行", "启动开发服务器: "+url)
	}
	notes = append(notes, fmt.Sprintf("PID: %d", proc.PID))
	return url, strings.Join(notes, "；"), nil
}

func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") || strings.Contains(err.Error(), "超时")
}

func createReactProjectTool(d Deps) Tool {
	return NewFuncTool(
		"create_react_project",
		"一站式创建 React + TypeScript + Vite + Tailwind 项目：初始化骨架 → 安装依赖（已安装则跳过）→ 校验基础文件。确定性步骤由程序完成，返回真实耗时。",
		objSchema(map[string]any{
			"title":   strProp("网站标题/项目名"),
			"install": boolProp("是否安装依赖，默认 true"),
		}),
		func(ctx context.Context, args map[string]any) (string, error) {
			if d.ProjectDir == "" {
				return "", errors.New("当前没有绑定项目目录")
			}
			start := time.Now()
			var steps []string
			if _, err := os.Stat(filepath.Join(d.ProjectDir, "package.json")); err == nil {
				steps = append(steps, "骨架：已存在，跳过初始化")
			} else {
				n, err := WriteScaffold(d.ProjectDir, getString(args, "title", d.ProjectName), "react-ts-tailwind")
				if err != nil {
					return "", err
				}
				steps = append(steps, fmt.Sprintf("骨架：已创建 %d 个基础文件（%dms）", n, time.Since(start).Milliseconds()))
			}
			if getBool(args, "install", true) {
				if _, err := os.Stat(filepath.Join(d.ProjectDir, "node_modules")); err == nil {
					steps = append(steps, "依赖：已安装，跳过")
				} else {
					t := time.Now()
					runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
					res := d.Shell.Run(runCtx, "npm install --no-audit --no-fund --prefer-offline --loglevel=error", d.ProjectDir)
					cancel()
					if res.Blocked != "" {
						return "", errors.New("依赖安装被安全拦截: " + res.Blocked)
					}
					if res.ExitCode != 0 || res.TimedOut {
						return "", fmt.Errorf("依赖安装失败（退出码 %d）:\n%s", res.ExitCode, textutil.Truncate(res.Stderr+"\n"+res.Stdout, 3000))
					}
					steps = append(steps, fmt.Sprintf("依赖：安装完成（%dms）", time.Since(t).Milliseconds()))
				}
			}
			var missing []string
			for _, f := range []string{"package.json", "index.html", "src/main.tsx", "src/App.tsx"} {
				if _, err := os.Stat(filepath.Join(d.ProjectDir, f)); err != nil {
					missing = append(missing, f)
				}
			}
			if len(missing) > 0 {
				return "", fmt.Errorf("项目结构校验失败，缺少: %s", strings.Join(missing, ", "))
			}
			steps = append(steps, "结构校验：通过")
			if d.Projects != nil && d.ProjectID != "" {
				d.Projects.AddHistory(d.ProjectID, "脚手架", "create_react_project 完成初始化与依赖安装")
			}
			return strings.Join(steps, "\n") + fmt.Sprintf("\n总耗时：%dms", time.Since(start).Milliseconds()), nil
		},
	)
}

func devServerTool(d Deps) Tool {
	return NewFuncTool(
		"dev_server",
		"管理网站开发服务器。action=start 启动（依赖缺失时会自动安装 npm 依赖）、stop 停止、status 查看状态与运行地址。启动成功后会返回真实可访问的 URL。",
		objSchema(map[string]any{
			"action": strProp("start | stop | status"),
		}, "action"),
		func(ctx context.Context, args map[string]any) (string, error) {
			action := strings.ToLower(getString(args, "action", "status"))
			name := "dev-" + d.ProjectID
			switch action {
			case "start":
				url, note, err := startDevServer(ctx, d, name)
				if err != nil {
					return "", err
				}
				if note != "" {
					return note + "\n开发服务器已启动: " + url, nil
				}
				return "开发服务器已启动: " + url, nil
			case "stop":
				stopped := false
				procs := d.Shell.Processes()
				for i := range procs {
					p := &procs[i]
					if p.Name == name && p.Status == "running" {
						if err := d.Shell.StopProcess(p.ID); err == nil {
							stopped = true
						}
					}
				}
				if d.Projects != nil && d.ProjectID != "" {
					_, _ = d.Projects.UpdateFunc(d.ProjectID, func(p *project.Project) { p.DevURL = "" })
				}
				if stopped {
					return "开发服务器已停止。", nil
				}
				return "没有正在运行的开发服务器。", nil
			default:
				procs := d.Shell.Processes()
				for i := range procs {
					p := &procs[i]
					if p.Name == name {
						if p.Status == "running" {
							url := p.URL
							if url == "" {
								url = "（尚未探测到 URL）"
							}
							return fmt.Sprintf("状态: running\nURL: %s\nPID: %d\n最近输出:\n%s", url, p.PID, textutil.Truncate(p.Output(20), 3000)), nil
						}
						return fmt.Sprintf("状态: %s\n退出码: %d\n最近输出:\n%s", p.Status, p.ExitCode, textutil.Truncate(p.Output(20), 3000)), nil
					}
				}
				return "开发服务器未启动。", nil
			}
		},
	)
}

var titleRe = regexp.MustCompile(`(?is)<title>(.*?)</title>`)

func webTestTool(d Deps) Tool {
	return NewFuncTool(
		"web_test",
		"检查网站是否可访问。若 Playwright MCP 已连接，将执行真实浏览器导航与页面快照；否则退化为 HTTP 可用性检查（会明确说明）。",
		objSchema(map[string]any{"url": strProp("要测试的完整 URL，例如 http://127.0.0.1:5173")}, "url"),
		func(ctx context.Context, args map[string]any) (string, error) {
			url := getString(args, "url", "")
			prefix := ""
			if url == "" && d.ProjectID != "" && d.Projects != nil {
				if p, err := d.Projects.Get(d.ProjectID); err == nil {
					url = p.DevURL
				}
			}
			if url == "" && d.ProjectDir != "" && d.Shell != nil {
				// 合并操作：没有可用地址时自动启动开发服务器（依赖缺失会一并安装）。
				u, note, err := startDevServer(ctx, d, "dev-"+d.ProjectID)
				if err != nil {
					return "", fmt.Errorf("自动启动开发服务器失败: %w", err)
				}
				url = u
				prefix = "已自动启动开发服务器: " + url + "\n"
				if note != "" {
					prefix += note + "\n"
				}
			}
			if url == "" {
				return "", errors.New("缺少 url，且项目没有运行中的开发服务器地址")
			}

			note := "Playwright MCP 未连接"
			if d.MCP != nil {
				if client := d.MCP.FindServer("playwright"); client != nil {
					if ok, why := d.MCP.Available(client.Name()); !ok {
						note = "Playwright 不可用（" + why + "），已降级为 HTTP 检查"
					} else if navigate := pickTool(client, "browser_navigate", "browser_navigate_page", "navigate"); navigate != "" {
						var out string
						var err error
						// 超时最多重试一次，随后降级，避免反复等待。
						for attempt := 0; attempt < 2; attempt++ {
							out, err = d.MCP.CallTool(ctx, client.Name(), navigate, map[string]any{"url": url})
							if err == nil || !isTimeoutErr(err) {
								break
							}
						}
						if err == nil {
							var b strings.Builder
							b.WriteString(prefix)
							fmt.Fprintf(&b, "Playwright 浏览器已加载页面: %s\n", url)
							b.WriteString(textutil.Truncate(out, 2000))
							b.WriteString("\n")
							if snap := pickTool(client, "browser_snapshot", "browser_get_snapshot"); snap != "" {
								if snapOut, snapErr := d.MCP.CallTool(ctx, client.Name(), snap, map[string]any{}); snapErr == nil {
									fmt.Fprintf(&b, "\n[页面快照]\n%s", textutil.Truncate(snapOut, 6000))
								}
							}
							return b.String(), nil
						}
						// 浏览器无法启动（例如沙箱限制）时，本次任务内不再尝试，直接降级为 HTTP 检查。
						d.MCP.MarkUnavailable(client.Name(), err.Error(), 10*time.Minute)
						note = "Playwright 浏览器不可用（" + textutil.Truncate(err.Error(), 200) + "），已降级为 HTTP 检查"
					}
				}
			}

			client := &http.Client{Timeout: 20 * time.Second}
			resp, err := client.Get(url)
			if err != nil {
				return "", fmt.Errorf("HTTP 检查失败，网站无法访问: %w", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 300_000))
			title := ""
			if m := titleRe.FindStringSubmatch(string(body)); len(m) > 1 {
				title = strings.TrimSpace(m[1])
			}
			status := "通过"
			if resp.StatusCode >= 400 {
				status = "异常"
			}
			return prefix + fmt.Sprintf("HTTP 检查%s：状态码 %d，HTML 大小 %d 字节，标题「%s」。\n注意：%s，本次仅验证 HTTP 可访问性，未执行浏览器渲染与交互测试。", status, resp.StatusCode, len(body), title, note), nil
		},
	)
}

func skillListTool(d Deps) Tool {
	return NewFuncTool(
		"skill_list",
		"列出可用的专业技能（Skill）名称与描述。",
		objSchema(map[string]any{}),
		func(ctx context.Context, args map[string]any) (string, error) {
			if d.Skills == nil {
				return "（Skill 系统未初始化）", nil
			}
			var b strings.Builder
			for _, s := range d.Skills.List() {
				fmt.Fprintf(&b, "- %s：%s\n", s.Name, s.Description)
			}
			return b.String(), nil
		},
	)
}

func skillLoadTool(d Deps) Tool {
	return NewFuncTool(
		"skill_load",
		"按名称加载 SKILL.md 的完整内容（开发规范与工作流程）。在开始某一阶段工作前调用。",
		objSchema(map[string]any{"name": strProp("Skill 名称，如 frontend-development")}, "name"),
		func(ctx context.Context, args map[string]any) (string, error) {
			if d.Skills == nil {
				return "", errors.New("Skill 系统未初始化")
			}
			name := getString(args, "name", "")
			sk, ok := d.Skills.Get(name)
			if !ok {
				return "", fmt.Errorf("Skill 不存在: %s（可先调用 skill_list 查看）", name)
			}
			return fmt.Sprintf("# Skill: %s\n%s\n\n%s", sk.Name, sk.Description, sk.Body), nil
		},
	)
}

func projectStatusTool(d Deps) Tool {
	return NewFuncTool(
		"project_status",
		"获取当前项目的名称、状态、技术栈、运行地址与文件统计。",
		objSchema(map[string]any{}),
		func(ctx context.Context, args map[string]any) (string, error) {
			if d.Projects == nil || d.ProjectID == "" {
				return "（未绑定项目）", nil
			}
			p, err := d.Projects.Get(d.ProjectID)
			if err != nil {
				return "", err
			}
			tree, _ := d.Projects.Tree(d.ProjectID, 6)
			files, dirs := countNodes(tree)
			payload := map[string]any{
				"id":         p.ID,
				"name":       p.Name,
				"status":     p.Status,
				"tech_stack": p.TechStack,
				"dev_url":    p.DevURL,
				"pages":      p.Pages,
				"file_count": files,
				"dir_count":  dirs,
				"workspace":  d.ProjectDir,
				"updated_at": p.UpdatedAt.Format(time.RFC3339),
			}
			data, _ := json.MarshalIndent(payload, "", "  ")
			return string(data), nil
		},
	)
}

func countNodes(n project.FileNode) (files, dirs int) {
	if n.IsDir {
		dirs++
	} else {
		files++
	}
	for _, c := range n.Children {
		f, d := countNodes(c)
		files += f
		dirs += d
	}
	return
}

func renderNode(n project.FileNode, prefix string, b *strings.Builder) {
	name := n.Name
	if n.IsDir {
		fmt.Fprintf(b, "%s%s/\n", prefix, name)
		for _, c := range n.Children {
			renderNode(c, prefix+"  ", b)
		}
		return
	}
	fmt.Fprintf(b, "%s%s (%d B)\n", prefix, name, n.Size)
}

func renderDir(dir string, depth int) (string, error) {
	if depth <= 0 {
		depth = 4
	}
	tree, err := walk(dir, "", depth)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	renderNode(tree, "", &b)
	return b.String(), nil
}

func walk(abs, rel string, depth int) (project.FileNode, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return project.FileNode{}, err
	}
	node := project.FileNode{Name: filepath.Base(abs), Path: rel, IsDir: info.IsDir(), Size: info.Size()}
	if !info.IsDir() || depth <= 0 {
		return node, nil
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return node, nil
	}
	for _, e := range entries {
		if e.Name() == "node_modules" || e.Name() == ".git" || e.Name() == "dist" {
			continue
		}
		childRel := e.Name()
		if rel != "" {
			childRel = rel + "/" + e.Name()
		}
		child, err := walk(filepath.Join(abs, e.Name()), childRel, depth-1)
		if err != nil {
			continue
		}
		node.Children = append(node.Children, child)
	}
	return node, nil
}

func pickTool(c *mcp.Client, names ...string) string {
	tools := c.Tools()
	for _, want := range names {
		for _, t := range tools {
			if t.Name == want {
				return t.Name
			}
		}
	}
	for _, want := range names {
		for _, t := range tools {
			if strings.Contains(strings.ToLower(t.Name), strings.ToLower(want)) {
				return t.Name
			}
		}
	}
	return ""
}

func freePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 5180
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func safeJoin(base, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", errors.New("路径不能为空")
	}
	clean := strings.TrimPrefix(strings.ReplaceAll(rel, "\\", "/"), "/")
	full := filepath.Join(base, filepath.FromSlash(clean))
	absBase, err1 := filepath.Abs(base)
	absFull, err2 := filepath.Abs(full)
	if err1 != nil || err2 != nil {
		return "", errors.New("路径解析失败")
	}
	r, err := filepath.Rel(absBase, absFull)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", errors.New("路径超出项目目录范围")
	}
	return absFull, nil
}

func objSchema(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func getString(args map[string]any, key, def string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func getBool(args map[string]any, key string, def bool) bool {
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func getInt(args map[string]any, key string, def int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case json.Number:
			i, _ := n.Int64()
			return int(i)
		}
	}
	return def
}
