package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"chat2web/internal/agent"
	"chat2web/internal/agent/analyzer"
	"chat2web/internal/config"
	"chat2web/internal/llm"
	"chat2web/internal/memory"
	"chat2web/internal/project"
	"chat2web/internal/textutil"
	"chat2web/internal/tools/mcp"
	"chat2web/internal/tools/shell"
	"chat2web/internal/tools/skill"
)

const banner = `
========================================
       Chat2Web AI
  Conversation to Website Agent
========================================
`

type app struct {
	cfg      *config.Config
	llm      *llm.Client
	mem      *memory.Manager
	skills   *skill.Manager
	projects *project.Manager
	shell    *shell.Executor
	mcp      *mcp.Manager
	agent    *agent.Agent
	reader   *bufio.Reader
	eof      bool
}

func main() {
	a, err := setup()
	if err != nil {
		fmt.Println("初始化失败：", err)
		os.Exit(1)
	}
	defer a.shutdown()

	fmt.Print(banner)
	fmt.Println("欢迎使用 Chat2Web AI！")
	fmt.Println("我可以根据你输入的聊天内容，自动生成一个完整的网站。")
	fmt.Println()
	fmt.Printf("工作目录：%s\n", a.cfg.Root)
	fmt.Printf("LLM：%s | 模型：%s | 已配置：%v\n", a.cfg.LLMBaseURL, orDefault(a.cfg.LLMModel, "(未设置)"), a.llm.Configured())
	if !a.llm.Configured() {
		fmt.Println("提示：未检测到可用的 LLM 配置，Agent 将以离线降级模式生成网站（内容来自聊天分析，但不是 LLM 生成）。")
	}
	fmt.Printf("Skill：%d 个 | MCP Server：%d 个（连接中） | 历史项目：%d 个\n",
		len(a.skills.List()), len(a.mcp.Statuses()), len(a.projects.List(false)))

	a.mainLoop()
}

func setup() (*app, error) {
	cfg := config.Load()
	client := llm.New(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel, cfg.LLMTemperature, cfg.LLMMaxTokens)
	if cfg.LLMTimeout > 0 {
		client.HTTP.Timeout = cfg.LLMTimeout
	}

	// 先迁移项目目录，再把记忆指向项目内 memory.md，保证记忆与项目正确关联。
	projects := project.NewManager(cfg.DataRoot, cfg.ProjectsRoot)
	rep := projects.MigrateLayout(
		filepath.Join(cfg.Root, "workspace", "generated-websites"),
		filepath.Join(cfg.Root, "workspace", "projects"),
		filepath.Join(cfg.MemoryRoot, "projects"),
	)
	if len(rep.Details) > 0 || len(rep.Errors) > 0 {
		fmt.Printf("[项目] 目录迁移：项目 %d、源码 %d、记忆 %d、成果 %d、合并元数据 %d\n",
			rep.Projects, rep.MovedSource, rep.MovedMemory, rep.CopiedOutput, rep.MergedMetadata)
		for _, d := range rep.Details {
			fmt.Println("  -", d)
		}
		for _, e := range rep.Errors {
			fmt.Println("  ! 迁移警告：", e)
		}
		if rep.BackupDir != "" {
			fmt.Println("  备份目录：", rep.BackupDir)
		}
	}
	// 旧回收站 data/projects/.trash 合并到 data/backups/trash，可恢复项目不丢失。
	if moved, err := projects.MigrateTrash(); err != nil {
		fmt.Println("[项目] 回收站迁移警告：", err)
	} else if moved > 0 {
		fmt.Printf("[项目] 已迁移 %d 个回收站项目到 %s\n", moved, projects.TrashRoot())
	}

	mem := memory.NewManager(cfg.MemoryRoot, client)
	mem.Configure(memory.Options{
		DataRoot:       cfg.DataRoot,
		SessionsRoot:   cfg.SessionsRoot,
		BackupsRoot:    cfg.BackupsRoot,
		RuntimeRoot:    cfg.RuntimeRoot,
		ReportsRoot:    cfg.ReportsRoot,
		TasksRoot:      cfg.TasksRoot,
		ProjectRoot:    cfg.ProjectsRoot,
		PlaywrightDays: cfg.RetentionPlaywrightDays,
		TaskDays:       cfg.RetentionTaskDays,
		SessionDays:    cfg.RetentionSessionDays,
		ReportsKeep:    cfg.ReportsKeep,
		Budget:         cfg.MemoryTokenBudget,
	})
	if err := mem.Init(); err != nil {
		return nil, fmt.Errorf("初始化长期记忆失败: %w", err)
	}
	if lr := mem.LastLayoutReport(); !lr.Empty() {
		fmt.Printf("[记忆] 目录迁移：会话摘要 %d、备份 %d、旧摘要 %d、报告 %d、运行数据 %d、任务 %d、冲突 %d\n",
			lr.MovedSessions, lr.MovedBackups, lr.MovedSummaries,
			lr.MovedReports, lr.MovedRuntime, lr.MovedTasks, lr.Conflicts)
		for _, d := range lr.Details {
			fmt.Println("  -", d)
		}
		for _, e := range lr.Errors {
			fmt.Println("  ! 迁移警告：", e)
		}
	}
	// 启动时自动清理过期临时数据；长期记忆与项目产物不参与清理。
	if rep := mem.CleanupMemory(); rep.DeletedFiles > 0 || rep.ArchivedFiles > 0 {
		fmt.Printf("[记忆] 已自动清理临时文件 %d 个、归档旧报告 %d 个\n", rep.DeletedFiles, rep.ArchivedFiles)
	}

	skills := skill.NewManager(cfg.SkillsRoot)
	if _, err := skills.Scan(); err != nil {
		fmt.Printf("提示：扫描技能目录失败（%v），将继续运行\n", err)
	}

	shellExec := shell.NewExecutor(cfg.WorkspaceRoot, cfg.ShellTimeout, cfg.MaxOutputChars)

	mcpc := mcp.NewManager(cfg.MCPConfigPath, cfg.Root, cfg.WorkspaceRoot)
	if err := mcpc.Load(); err != nil {
		fmt.Printf("提示：加载 MCP 配置失败（%v），MCP 功能不可用\n", err)
	} else {
		mcpc.StartEnabled(context.Background())
	}

	ag := agent.New(cfg, client, mem, skills, projects, shellExec, mcpc)
	return &app{
		cfg: cfg, llm: client, mem: mem, skills: skills, projects: projects,
		shell: shellExec, mcp: mcpc, agent: ag,
		reader: bufio.NewReaderSize(os.Stdin, 64*1024),
	}, nil
}

func (a *app) mainLoop() {
	for {
		fmt.Println()
		fmt.Println("请选择操作：")
		fmt.Println()
		fmt.Println("1. 新建网页生成任务")
		fmt.Println("2. 打开 / 继续之前的项目")
		fmt.Println("3. 查看历史生成记录")
		fmt.Println("4. 记忆管理")
		fmt.Println("5. 项目管理（保存 / 归档 / 恢复 / 清理）")
		fmt.Println("6. 查看 Skill 列表")
		fmt.Println("7. 查看 MCP 工具")
		fmt.Println("8. 退出")
		fmt.Println()
		choice := a.readLine("请输入选项：")
		if a.eof {
			fmt.Println("\n输入已结束，退出。")
			return
		}
		fmt.Println()
		switch strings.TrimSpace(choice) {
		case "1":
			a.newTask()
		case "2":
			a.continueProject()
		case "3":
			a.showHistory()
		case "4":
			a.manageMemory()
		case "5":
			a.manageProjects()
		case "6":
			a.showSkills()
		case "7":
			a.showMCP()
		case "8":
			fmt.Println("再见！")
			return
		case "":
			fmt.Println("输入不能为空，请重新选择。")
		default:
			fmt.Println("无效选项，请输入 1-8。")
		}
	}
}

func (a *app) newTask() {
	chat, ok := a.readChat()
	if !ok {
		return
	}
	// 同一个网页反复修改必须归属同一个项目，避免不断产生新项目记录。
	if match := a.projects.Match(chat); match != nil {
		fmt.Printf("\n检测到相关项目：%s（%s，%s）\n", match.Name, match.ID, project.LifecycleLabel(match.Lifecycle))
		if a.confirm("是否在该项目中继续修改", true) {
			fmt.Printf("\n继续项目：%s\n\n", match.Name)
			a.runAgent(match, chat, true)
			return
		}
	}
	fmt.Println("\n未匹配到已有项目。普通对话不会自动创建项目。")
	if !a.confirm("是否新建独立项目并开始生成", true) {
		fmt.Println("已取消，未创建项目。")
		return
	}
	p, err := a.projects.Create(textutil.TitleFromMessage(chat), "来自终端聊天内容")
	if err != nil {
		fmt.Println("创建项目失败：", err)
		return
	}
	fmt.Printf("\n已创建项目：%s\n目录：%s\n\n", p.ID, a.projects.ProjectDir(p.ID))
	a.runAgent(p, chat, false)
}

func (a *app) continueProject() {
	list := a.projects.SelectableProjects()
	if len(list) == 0 {
		fmt.Println("暂无可继续的项目（归档项目请在「项目管理」中恢复）。")
		return
	}
	fmt.Println("可继续项目：")
	for i, p := range list {
		fmt.Printf("  %d. %s  [%s]  %s\n", i+1, p.Name, project.LifecycleLabel(p.Lifecycle), p.UpdatedAt.Format("2006-01-02 15:04"))
		fmt.Printf("     ID: %s\n", p.ID)
	}
	raw := strings.TrimSpace(a.readLine("\n请输入项目序号或 ID（回车取消）："))
	if raw == "" {
		return
	}
	var target *project.Project
	if idx := parseIndex(raw); idx > 0 && idx <= len(list) {
		target = list[idx-1]
	} else {
		for _, p := range list {
			if p.ID == raw {
				target = p
				break
			}
		}
	}
	if target == nil {
		fmt.Println("未找到该项目。")
		return
	}
	fmt.Println("\n请选择操作：")
	fmt.Println("  1. 直接打开项目（启动服务，不调用大模型）")
	fmt.Println("  2. 输入修改要求（增量修改，会调用大模型）")
	switch strings.TrimSpace(a.readLine("请输入选项（回车取消）：")) {
	case "1":
		a.openProject(target)
		return
	case "2":
	default:
		return
	}
	chat, ok := a.readChat()
	if !ok {
		return
	}
	fmt.Printf("\n继续项目：%s（%s）\n\n", target.Name, target.ID)
	a.runAgent(target, chat, true)
}

func (a *app) openProject(p *project.Project) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	a.projects.Touch(p.ID)
	fmt.Printf("\n正在启动项目：%s（不调用大模型）…\n\n", p.Name)
	var devURL string
	devURL, err := a.agent.Open(ctx, p, func(e agent.Event) {
		printEvent(e)
		if e.Type == "dev_url" {
			devURL = e.Text
		}
	})
	if proj, getErr := a.projects.Get(p.ID); getErr == nil && proj.DevURL != "" {
		devURL = proj.DevURL
	}
	fmt.Println()
	fmt.Println("----------------------------------------")
	if err != nil {
		fmt.Printf("启动失败：%v\n", err)
		printFailureHints(err)
		fmt.Println("----------------------------------------")
		return
	}
	fmt.Printf("项目已就绪：%s\n", devURL)
	if devURL != "" {
		openBrowser(devURL)
	}
	fmt.Println("----------------------------------------")
}

func (a *app) runAgent(p *project.Project, chat string, modify bool) {
	a.ensureMCP()
	a.projects.Touch(p.ID)
	if modify {
		a.projects.MarkActive(p.ID)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Println("Agent 开始分析聊天内容…")
	start := time.Now()
	var devURL string
	devURLFresh := false

	err := a.agent.Run(ctx, agent.RunRequest{
		SessionID: p.SessionID,
		ProjectID: p.ID,
		Chat:      chat,
		Project:   p,
		Modify:    modify,
	}, func(e agent.Event) {
		printEvent(e)
		if e.Type == "dev_url" && e.Text != "" {
			devURL = e.Text
			devURLFresh = true
		}
	})

	// 本轮没有启动新的开发服务器时，才回退到项目记录里的地址（可能已失效）。
	if !devURLFresh {
		if proj, getErr := a.projects.Get(p.ID); getErr == nil && proj.DevURL != "" {
			devURL = proj.DevURL
		}
	}

	// 任务结束后清理本次产生的浏览器调试截图；项目素材与最终成果不受影响。
	if rep := a.mem.CleanupTaskScreenshots(start); rep.DeletedFiles > 0 {
		fmt.Printf("[清理] 已清理本次任务调试截图 %d 个\n", rep.DeletedFiles)
	}

	fmt.Println()
	fmt.Println("----------------------------------------")
	if err != nil {
		fmt.Printf("任务未完成（耗时 %s）：%v\n", time.Since(start).Round(time.Second), err)
		printFailureHints(err)
		// 页面已经能跑起来时，即使任务停在「暂停」也直接打开，方便边看边继续修改。
		if devURL != "" && devURLFresh {
			fmt.Printf("预览地址：%s（已自动打开浏览器）\n", devURL)
			openBrowser(devURL)
		}
	} else {
		fmt.Printf("任务完成（耗时 %s）\n", time.Since(start).Round(time.Second))
		if out, pubErr := a.projects.PublishOutput(p.ID); pubErr != nil {
			fmt.Printf("提示：整理最终成果失败（%v）\n", pubErr)
		} else if out != "" {
			fmt.Printf("最终成果：%s\n", out)
		}
		if proj, getErr := a.projects.Get(p.ID); getErr == nil {
			fmt.Printf("项目：%s\n目录：%s\n状态：%s\n生命周期：%s\n", proj.Name, proj.Dir, proj.Status, project.LifecycleLabel(proj.Lifecycle))
			if proj.Lifecycle != project.LifecycleSaved && !a.eof {
				if a.confirm("是否保存为正式项目（永久保留）", false) {
					if saved, saveErr := a.projects.Save(p.ID); saveErr == nil {
						fmt.Printf("已保存：%s\n", saved.Name)
					} else {
						fmt.Printf("保存失败：%v\n", saveErr)
					}
				}
			}
		}
		if devURL != "" {
			fmt.Printf("访问地址：%s\n", devURL)
			openBrowser(devURL)
		}
	}
	fmt.Println("----------------------------------------")
}

func printEvent(e agent.Event) {
	switch e.Type {
	case "status":
		fmt.Printf("[步骤] %s\n", e.Text)
	case "warning":
		fmt.Printf("[提示] %s\n", e.Text)
	case "memory":
		fmt.Printf("[记忆] 检索到相关历史：%s\n", textutil.Truncate(textutil.Compact(e.Text), 200))
	case "skill":
		fmt.Printf("[技能] %s %s\n", e.Name, e.Text)
	case "analysis":
		if an, ok := e.Data.(*analyzer.Analysis); ok {
			fmt.Printf("[分析] 主题：%s | 类别：%s | 网页类型：%s | 来源：%s\n", an.Topic, an.Category, an.WebsiteType, an.Source)
			fmt.Printf("       摘要：%s\n", an.Summary)
			fmt.Printf("       关键信息：%d 条 | 实体：%d 个 | 待补充：%d 项\n", len(an.KeyFacts), len(an.Entities), len(an.Missing))
		}
	case "plan":
		fmt.Printf("[规划]\n%s", indent(e.Text, "  "))
	case "tool_call":
		fmt.Printf("  → %s %s\n", e.Name, textutil.Truncate(e.Args, 220))
	case "tool_result":
		mark := "成功"
		if !e.OK {
			mark = "失败"
		}
		fmt.Printf("  ← %s（%s）%s\n", e.Name, mark, indent(textutil.Truncate(e.Text, 600), "      "))
	case "assistant":
		fmt.Printf("\n[Agent] %s\n", e.Text)
	case "assistant_delta":
		// 终端不逐字刷屏，最终消息在 assistant / done 中输出
	case "progress":
		fmt.Printf("  … %s\n", e.Text)
	case "stats":
		fmt.Printf("[耗时] %s\n", e.Text)
	case "perf":
		fmt.Printf("\n[性能报告]\n%s\n", indent(e.Text, "  "))
	case "error":
		fmt.Printf("[错误] %s\n", e.Text)
	case "memory_updated":
		fmt.Printf("[记忆] 已保存会话摘要：%s\n", textutil.Truncate(e.Text, 200))
	case "dev_url":
		fmt.Printf("[运行] %s\n", e.Text)
	case "done":
		if e.Text != "" {
			fmt.Printf("\n[完成] %s\n", e.Text)
		}
	}
}

func (a *app) showHistory() {
	list := a.projects.List(false)
	if len(list) == 0 {
		fmt.Println("暂无历史生成记录。")
		return
	}
	for _, p := range list {
		fmt.Printf("- %s\n", p.Name)
		fmt.Printf("  ID：%s | 生命周期：%s | 状态：%s | 创建：%s | 更新：%s\n",
			p.ID, project.LifecycleLabel(p.Lifecycle), p.Status, p.CreatedAt.Format("2006-01-02 15:04"), p.UpdatedAt.Format("2006-01-02 15:04"))
		if len(p.Pages) > 0 {
			fmt.Printf("  页面：%s\n", strings.Join(p.Pages, "、"))
		}
		if p.DevURL != "" {
			fmt.Printf("  地址：%s\n", p.DevURL)
		}
		fmt.Printf("  目录：%s\n", p.Dir)
	}
	fmt.Printf("\n共 %d 个项目（归档项目可在「项目管理」中查看）。\n", len(list))
}

func (a *app) confirm(prompt string, defYes bool) bool {
	hint := "(y/N)"
	if defYes {
		hint = "(Y/n)"
	}
	raw := strings.ToLower(strings.TrimSpace(a.readLine(prompt + " " + hint + "：")))
	if raw == "" {
		return defYes
	}
	return raw == "y" || raw == "yes" || raw == "是"
}

func (a *app) manageProjects() {
	for {
		fmt.Println()
		fmt.Println("== 项目管理 ==")
		fmt.Printf("正式 %d | 临时 %d | 归档 %d | 回收站 %d\n",
			len(a.projects.FormalProjects()), len(a.projects.TemporaryProjects()),
			len(a.projects.ArchivedProjects()), len(a.projects.DeletedProjects()))
		fmt.Println("1. 查看正式项目（已保存）")
		fmt.Println("2. 查看临时项目")
		fmt.Println("3. 查看已归档项目")
		fmt.Println("4. 保存项目")
		fmt.Println("5. 归档项目")
		fmt.Println("6. 恢复项目（归档 / 回收站）")
		fmt.Println("7. 删除项目（移入回收站，可恢复）")
		fmt.Println("8. 清理临时文件与过期任务")
		fmt.Println("9. 清理过期临时项目（超过 7 天）")
		fmt.Println("0. 返回主菜单")
		switch strings.TrimSpace(a.readLine("请输入选项：")) {
		case "1":
			a.listProjects("正式项目", a.projects.FormalProjects())
		case "2":
			a.listProjects("临时项目", a.projects.TemporaryProjects())
		case "3":
			a.listProjects("已归档项目", a.projects.ArchivedProjects())
		case "4":
			a.saveProject()
		case "5":
			a.archiveProject()
		case "6":
			a.restoreProject()
		case "7":
			a.deleteProject()
		case "8":
			a.runCleanup()
		case "9":
			a.cleanStaleTemporary()
		case "0", "":
			return
		default:
			fmt.Println("无效选项，请输入 0-9。")
		}
		if a.eof {
			return
		}
	}
}

func (a *app) listProjects(title string, list []*project.Project) {
	fmt.Printf("\n== %s（%d）==\n", title, len(list))
	if len(list) == 0 {
		fmt.Println("暂无。")
		return
	}
	for i, p := range list {
		last := "-"
		if p.LastOpenedAt != nil {
			last = p.LastOpenedAt.Format("2006-01-02 15:04")
		}
		fmt.Printf("%d. %s\n   ID：%s | 更新：%s | 最近打开：%s\n   目录：%s\n",
			i+1, p.Name, p.ID, p.UpdatedAt.Format("2006-01-02 15:04"), last, p.ProjectDirForDisplay())
	}
}

func (a *app) pickProject(title string, list []*project.Project) *project.Project {
	if len(list) == 0 {
		fmt.Printf("暂无可选项目（%s）。\n", title)
		return nil
	}
	for i, p := range list {
		fmt.Printf("  %d. %s  [%s]  %s\n", i+1, p.Name, project.LifecycleLabel(p.Lifecycle), p.UpdatedAt.Format("2006-01-02 15:04"))
		fmt.Printf("     ID: %s\n", p.ID)
	}
	raw := strings.TrimSpace(a.readLine("\n请输入项目序号或 ID（回车取消）："))
	if raw == "" {
		return nil
	}
	if idx := parseIndex(raw); idx > 0 && idx <= len(list) {
		return list[idx-1]
	}
	for _, p := range list {
		if p.ID == raw {
			return p
		}
	}
	fmt.Println("未找到该项目。")
	return nil
}

func (a *app) saveProject() {
	p := a.pickProject("可保存项目", a.projects.TemporaryProjects())
	if p == nil {
		return
	}
	if _, err := a.projects.Save(p.ID); err != nil {
		fmt.Println("保存失败：", err)
		return
	}
	fmt.Printf("已保存为正式项目：%s\n", p.Name)
}

func (a *app) archiveProject() {
	list := a.projects.SelectableProjects()
	p := a.pickProject("可归档项目", list)
	if p == nil {
		return
	}
	if !a.confirm("归档后项目将移出活动列表，源码与记忆保留。确认归档 "+p.Name, true) {
		return
	}
	if _, err := a.projects.Archive(p.ID); err != nil {
		fmt.Println("归档失败：", err)
		return
	}
	fmt.Printf("已归档：%s\n", p.Name)
}

func (a *app) restoreProject() {
	var list []*project.Project
	list = append(list, a.projects.ArchivedProjects()...)
	list = append(list, a.projects.DeletedProjects()...)
	p := a.pickProject("可恢复项目（归档或回收站）", list)
	if p == nil {
		return
	}
	if p.Deleted {
		if _, err := a.projects.Restore(p.ID); err != nil {
			fmt.Println("恢复失败：", err)
			return
		}
		_ = a.mem.Reload()
		fmt.Printf("已从回收站恢复：%s\n", p.Name)
		return
	}
	if _, err := a.projects.Unarchive(p.ID); err != nil {
		fmt.Println("恢复失败：", err)
		return
	}
	fmt.Printf("已从归档恢复：%s\n", p.Name)
}

func (a *app) deleteProject() {
	list := a.projects.SelectableProjects()
	p := a.pickProject("可删除项目", list)
	if p == nil {
		return
	}
	if !a.confirm("删除后项目目录与记忆移入回收站，可恢复。确认删除 "+p.Name, false) {
		return
	}
	if err := a.projects.Delete(p.ID); err != nil {
		fmt.Println("删除失败：", err)
		return
	}
	a.mem.ForgetProject(p.ID)
	fmt.Printf("已删除（可在「恢复项目」中找回）：%s\n", p.Name)
}

func (a *app) cleanStaleTemporary() {
	stale := a.projects.StaleTemporary(7)
	if len(stale) == 0 {
		fmt.Println("没有超过 7 天未使用的临时项目。")
		return
	}
	fmt.Println("以下临时项目超过 7 天未使用：")
	for _, p := range stale {
		fmt.Printf("- %s（%s，最近更新 %s）\n", p.Name, p.ID, p.UpdatedAt.Format("2006-01-02 15:04"))
	}
	if !a.confirm("是否把它们移入回收站（可恢复）", false) {
		return
	}
	ids, err := a.projects.PurgeTemporary(7, false)
	if err != nil {
		fmt.Println("清理失败：", err)
		return
	}
	for _, id := range ids {
		a.mem.ForgetProject(id)
	}
	fmt.Printf("已清理 %d 个临时项目，目录保存在回收站可恢复。\n", len(ids))
}

func (a *app) manageMemory() {
	for {
		fmt.Println()
		fmt.Println("== 记忆管理 ==")
		fmt.Println("1. 查看用户长期记忆")
		fmt.Println("2. 查看项目记忆")
		fmt.Println("3. 搜索历史记忆")
		fmt.Println("4. 手动添加重要记忆")
		fmt.Println("5. 编辑已有记忆")
		fmt.Println("6. 删除指定记忆")
		fmt.Println("7. 查看临时文件占用")
		fmt.Println("8. 手动清理临时文件")
		fmt.Println("9. 查看会话摘要")
		fmt.Println("0. 返回主菜单")
		switch strings.TrimSpace(a.readLine("请输入选项：")) {
		case "1":
			a.showUserMemory()
		case "2":
			a.showProjectMemory()
		case "3":
			a.searchMemory()
		case "4":
			a.addMemory()
		case "5":
			a.editMemory()
		case "6":
			a.deleteMemory()
		case "7":
			a.showRuntimeUsage()
		case "8":
			a.runCleanup()
		case "9":
			a.showSessionDigests()
		case "0", "":
			return
		default:
			fmt.Println("无效选项，请输入 0-9。")
		}
		if a.eof {
			return
		}
	}
}

func (a *app) showUserMemory() {
	fmt.Printf("== 用户长期记忆（%s）==\n", filepath.Join(a.mem.Root(), "memory.md"))
	fmt.Println(textutil.Truncate(a.mem.UserMemoryText(), 4000))
	fmt.Printf("\n共 %d 条跨项目用户记忆（项目专属记忆见“查看项目记忆”）。\n", len(a.mem.UserEntries()))
}

func (a *app) showProjectMemory() {
	list := a.mem.Projects()
	if len(list) == 0 {
		fmt.Println("暂无项目记忆。")
		return
	}
	for i, p := range list {
		fmt.Printf("  %d. %s [%s] 更新：%s\n     ID：%s\n", i+1, p.Name, p.Status, p.UpdatedAt.Format("2006-01-02 15:04"), p.ProjectID)
	}
	raw := strings.TrimSpace(a.readLine("请输入项目序号或 ID（回车取消）："))
	if raw == "" {
		return
	}
	id := raw
	if idx := parseIndex(raw); idx > 0 && idx <= len(list) {
		id = list[idx-1].ProjectID
	}
	text, err := a.mem.ProjectMemoryText(id)
	if err != nil {
		fmt.Println("读取失败：", err)
		return
	}
	fmt.Println()
	fmt.Println(textutil.Truncate(text, 6000))
}

func (a *app) searchMemory() {
	query := strings.TrimSpace(a.readLine("请输入检索关键词："))
	results := a.mem.SearchMemory(query, 10)
	if len(results) == 0 {
		fmt.Println("未找到匹配的记忆。")
		return
	}
	for _, r := range results {
		project := r.ProjectID
		if project == "" {
			project = "全局"
		}
		fmt.Printf("- [%s] %s（%s，更新：%s）\n", r.Type, r.Title, project, r.UpdatedAt.Format("2006-01-02 15:04"))
		fmt.Printf("  文件：%s\n", r.File)
		if r.Snippet != "" {
			fmt.Printf("  片段：%s\n", textutil.Truncate(r.Snippet, 180))
		}
	}
}

func (a *app) addMemory() {
	key := strings.TrimSpace(a.readLine("记忆标题（如 设计偏好）："))
	value := strings.TrimSpace(a.readLine("记忆内容："))
	if key == "" || value == "" {
		fmt.Println("标题和内容不能为空，已取消。")
		return
	}
	tags := strings.TrimSpace(a.readLine("检索标签（逗号分隔，可留空）："))
	saved, err := a.mem.SaveMemory(memory.Entry{
		Kind:   "preference",
		Key:    key,
		Value:  value,
		Tags:   splitInput(tags),
		Source: "manual",
		Pinned: true,
	})
	if err != nil {
		fmt.Println("保存失败：", err)
		return
	}
	fmt.Printf("已保存：%s（ID：%s）\n", saved.Key, saved.ID)
}

func (a *app) editMemory() {
	e := a.pickMemoryEntry()
	if e == nil {
		return
	}
	fmt.Printf("当前：%s：%s\n", e.Key, e.Value)
	key := strings.TrimSpace(a.readLine("新标题（回车保持不变）："))
	value := strings.TrimSpace(a.readLine("新内容（回车保持不变）："))
	if key == "" && value == "" {
		fmt.Println("未做修改。")
		return
	}
	if err := a.mem.UpdateMemory(e.ID, memory.MemoryPatch{Key: key, Value: value}); err != nil {
		fmt.Println("更新失败：", err)
		return
	}
	fmt.Println("已更新记忆。")
}

func (a *app) deleteMemory() {
	e := a.pickMemoryEntry()
	if e == nil {
		return
	}
	if strings.ToLower(strings.TrimSpace(a.readLine("确认删除「"+e.Key+"」？(y/N)："))) != "y" {
		fmt.Println("已取消。")
		return
	}
	if err := a.mem.DeleteMemory(e.ID); err != nil {
		fmt.Println("删除失败：", err)
		return
	}
	fmt.Println("已删除记忆。")
}

func (a *app) pickMemoryEntry() *memory.Entry {
	entries := a.mem.Entries()
	if len(entries) == 0 {
		fmt.Println("暂无用户长期记忆。")
		return nil
	}
	for i, e := range entries {
		fmt.Printf("  %d. [%s] %s：%s\n", i+1, e.Kind, e.Key, textutil.Truncate(e.Value, 80))
	}
	raw := strings.TrimSpace(a.readLine("请输入序号或 ID（回车取消）："))
	if raw == "" {
		return nil
	}
	if idx := parseIndex(raw); idx > 0 && idx <= len(entries) {
		return &entries[idx-1]
	}
	for i := range entries {
		if entries[i].ID == raw {
			return &entries[i]
		}
	}
	fmt.Println("未找到该记忆。")
	return nil
}

func (a *app) showRuntimeUsage() {
	usage := a.mem.RuntimeUsage()
	var files int
	var bytes int64
	fmt.Println("== 临时文件占用 ==")
	for _, u := range usage {
		fmt.Printf("- %s：%d 个文件，%.1f KB\n", u.Path, u.Files, float64(u.Bytes)/1024)
		files += u.Files
		bytes += u.Bytes
	}
	fmt.Printf("合计：%d 个文件，%.1f KB\n", files, float64(bytes)/1024)
	fmt.Printf("保留策略：Playwright %d 天，临时任务 %d 天，临时报告最近 %d 份。\n",
		a.cfg.RetentionPlaywrightDays, a.cfg.RetentionTaskDays, a.cfg.ReportsKeep)
}

func (a *app) runCleanup() {
	rep := a.mem.CleanupMemory()
	fmt.Printf("清理完成：删除 %d 个文件（%.1f KB），归档旧报告 %d 个（%.1f KB）。\n",
		rep.DeletedFiles, float64(rep.DeletedBytes)/1024, rep.ArchivedFiles, float64(rep.ArchivedBytes)/1024)
	if len(rep.SkippedReferenced) > 0 {
		fmt.Printf("跳过仍被任务引用的文件 %d 个。\n", len(rep.SkippedReferenced))
	}
}

func (a *app) showSessionDigests() {
	list := a.mem.Digests()
	if len(list) == 0 {
		fmt.Println("暂无会话摘要。")
		return
	}
	for _, d := range list {
		fmt.Printf("- [%s] %s\n", d.UpdatedAt.Format("2006-01-02 15:04"), orDefault(d.ProjectName, d.ProjectID))
		fmt.Printf("  任务：%s\n", textutil.Truncate(d.CoreTask, 120))
		fmt.Printf("  摘要：%s\n", textutil.Truncate(d.Summary, 200))
		fmt.Printf("  文件：%s（消息游标 %d）\n", d.File, d.Cursor)
	}
	fmt.Printf("\n共 %d 条会话摘要，目录：%s\n", len(list), a.mem.SessionsRoot())
}

func splitInput(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '，' || r == '、'
	}) {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (a *app) showSkills() {
	list := a.skills.List()
	if len(list) == 0 {
		fmt.Println("未发现 Skill。请在 skills/<name>/SKILL.md 中新增。")
		return
	}
	fmt.Printf("已加载 %d 个 Skill（来自 %s）：\n\n", len(list), a.skills.Root())
	for _, s := range list {
		fmt.Printf("- %s\n", s.Name)
		fmt.Printf("  描述：%s\n", s.Description)
		if len(s.Triggers) > 0 {
			fmt.Printf("  触发词：%s\n", strings.Join(s.Triggers, "、"))
		}
		fmt.Printf("  路径：%s（%d 字节）\n", s.Path, s.Size)
	}
	fmt.Println("\n新增 Skill：创建 skills/<名称>/SKILL.md，写入 frontmatter（name/description/triggers）与正文，重启后自动扫描。")
}

func (a *app) showMCP() {
	statuses := a.mcp.Statuses()
	if len(statuses) == 0 {
		fmt.Println("未配置 MCP Server，请编辑 configs/mcp.json。")
		return
	}
	fmt.Println("MCP Server 状态：")
	pending := []string{}
	for _, st := range statuses {
		flag := "启用"
		if !st.Enabled {
			flag = "停用"
		}
		fmt.Printf("- %s [%s] 状态：%s 工具：%d\n", st.Name, flag, st.Status, st.ToolCount)
		if st.Error != "" {
			fmt.Printf("  错误：%s\n", textutil.Truncate(st.Error, 300))
		}
		for i, t := range st.Tools {
			if i >= 8 {
				fmt.Printf("  ... 其余 %d 个工具\n", len(st.Tools)-8)
				break
			}
			fmt.Printf("  · %s：%s\n", t.Name, textutil.Truncate(t.Description, 100))
		}
		if st.Enabled && st.Status != "connected" {
			pending = append(pending, st.Name)
		}
	}
	if len(pending) > 0 {
		ans := strings.ToLower(strings.TrimSpace(a.readLine("\n是否立即连接未连接的 Server？(y/N)：")))
		if ans == "y" || ans == "yes" {
			a.ensureMCP()
			fmt.Println("\n连接结果：")
			for _, st := range a.mcp.Statuses() {
				fmt.Printf("- %s：%s（工具 %d 个）\n", st.Name, st.Status, st.ToolCount)
			}
		}
	}
}

func (a *app) ensureMCP() {
	if a.mcp == nil {
		return
	}
	for _, st := range a.mcp.Statuses() {
		if !st.Enabled || st.Status == "connected" {
			continue
		}
		if st.Status == "connecting" {
			a.waitMCP(st.Name, 60*time.Second)
			continue
		}
		fmt.Printf("[MCP] 连接 %s …\n", st.Name)
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		err := a.mcp.Connect(ctx, st.Name)
		cancel()
		if err != nil {
			fmt.Printf("[MCP] %s 连接失败：%v\n", st.Name, textutil.Truncate(err.Error(), 300))
			continue
		}
		a.waitMCP(st.Name, 60*time.Second)
	}
}

func (a *app) waitMCP(name string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, st := range a.mcp.Statuses() {
			if st.Name != name {
				continue
			}
			switch st.Status {
			case "connected":
				fmt.Printf("[MCP] %s 已连接，发现 %d 个工具\n", name, st.ToolCount)
				return
			case "error":
				fmt.Printf("[MCP] %s 连接出错：%s\n", name, textutil.Truncate(st.Error, 300))
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Printf("[MCP] %s 连接超时\n", name)
}

func (a *app) readChat() (string, bool) {
	fmt.Println("请输入聊天内容。")
	fmt.Println("支持多行输入：输入 END 结束；/cancel 取消；/file <路径> 读取文本文件。")
	var lines []string
	for {
		fmt.Print("> ")
		line, err := a.reader.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			if len(lines) == 0 {
				fmt.Println("\n输入结束（EOF）。")
				return "", false
			}
			break
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.EqualFold(trimmed, "END"):
			chat := strings.TrimSpace(strings.Join(lines, "\n"))
			if chat == "" {
				fmt.Println("聊天内容为空，请重新输入或使用 /cancel 取消。")
				return "", false
			}
			return chat, true
		case strings.EqualFold(trimmed, "/cancel"):
			fmt.Println("已取消本次任务。")
			return "", false
		case strings.HasPrefix(strings.ToLower(trimmed), "/file"):
			path := strings.TrimSpace(trimmed[len("/file"):])
			if path == "" {
				fmt.Println("用法：/file path/to/chat.txt")
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				fmt.Println("读取文件失败：", err)
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				fmt.Println("文件内容为空。")
				continue
			}
			lines = append(lines, content)
			fmt.Printf("已读取 %s（%d 字符），继续输入或 END 结束。\n", path, len([]rune(content)))
		case trimmed == "":
			if len(lines) > 0 {
				lines = append(lines, "")
			}
		default:
			lines = append(lines, strings.TrimRight(line, "\r\n"))
		}
		if err != nil {
			break
		}
	}
	chat := strings.TrimSpace(strings.Join(lines, "\n"))
	if chat == "" {
		fmt.Println("聊天内容为空，已取消。")
		return "", false
	}
	return chat, true
}

func (a *app) readLine(prompt string) string {
	fmt.Print(prompt)
	line, err := a.reader.ReadString('\n')
	if err != nil && line == "" {
		a.eof = true
		return ""
	}
	return strings.TrimSpace(line)
}

func (a *app) shutdown() {
	fmt.Println("\n正在清理开发服务器与 MCP 连接…")
	procs := a.shell.Processes()
	for i := range procs {
		if procs[i].Status == "running" {
			_ = a.shell.StopProcess(procs[i].ID)
		}
	}
	// 服务已停止，清掉失效地址，避免历史记录里留下打不开的 ready 链接。
	for _, p := range a.projects.List(false) {
		if p.DevURL == "" {
			continue
		}
		_, _ = a.projects.UpdateFunc(p.ID, func(pp *project.Project) {
			pp.DevURL = ""
			if pp.Status == "ready" {
				pp.Status = "generated"
			}
		})
	}
	if a.mcp != nil {
		for _, st := range a.mcp.Statuses() {
			if st.Status == "connected" || st.Status == "connecting" {
				_ = a.mcp.Disconnect(st.Name)
			}
		}
	}
}

func openBrowser(url string) {
	if os.Getenv("CHAT2WEB_NO_OPEN") == "1" {
		return
	}
	var cmd *exec.Cmd
	switch strings.ToLower(os.Getenv("OS")) {
	case "windows_nt":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		if filepath.Separator == '\\' {
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
		} else if _, err := exec.LookPath("open"); err == nil {
			cmd = exec.Command("open", url)
		} else {
			cmd = exec.Command("xdg-open", url)
		}
	}
	if err := cmd.Start(); err != nil {
		fmt.Printf("（自动打开浏览器失败，请手动访问：%s）\n", url)
	}
}

func printFailureHints(err error) {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "modelnotopen"):
		fmt.Println("原因：当前账号未开通该模型。请在火山方舟控制台开通模型，或改用其他 OpenAI 兼容服务。")
	case strings.Contains(msg, "未配置 llm") || strings.Contains(msg, "llm_api_key"):
		fmt.Println("请配置 LLM：在 chat2web/.env 中填写 LLM_BASE_URL / LLM_API_KEY / LLM_MODEL。")
	case strings.Contains(msg, "context canceled"):
		fmt.Println("任务已被用户取消。已生成的代码仍保存在项目目录中。")
	case strings.Contains(msg, "读取 llm 流失败") || strings.Contains(msg, "connection") || strings.Contains(msg, "wsarecv") || strings.Contains(msg, "timeout"):
		fmt.Println("原因：与 LLM 服务的网络连接中断或超时（已自动重试 3 次）。")
		fmt.Println("建议：检查网络/代理，或在 chat2web/.env 中调小 LLM_MAX_TOKENS / LLM_TIMEOUT_SECONDS 后重试。已生成的文件会保留。")
	}
}

func parseIndex(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func indent(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n") + "\n"
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
