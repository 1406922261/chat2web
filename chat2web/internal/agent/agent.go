package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"chat2web/internal/agent/analyzer"
	"chat2web/internal/agent/planner"
	"chat2web/internal/config"
	"chat2web/internal/generator"
	"chat2web/internal/llm"
	"chat2web/internal/memory"
	"chat2web/internal/perf"
	"chat2web/internal/project"
	"chat2web/internal/task"
	"chat2web/internal/textutil"
	"chat2web/internal/tools"
	"chat2web/internal/tools/mcp"
	"chat2web/internal/tools/shell"
	"chat2web/internal/tools/skill"
)

// Event 是 Agent 推送给终端的过程事件。
type Event struct {
	Type   string    `json:"type"`
	Step   int       `json:"step,omitempty"`
	Text   string    `json:"text,omitempty"`
	Name   string    `json:"name,omitempty"`
	Args   string    `json:"args,omitempty"`
	Output string    `json:"output,omitempty"`
	OK     bool      `json:"ok,omitempty"`
	Data   any       `json:"data,omitempty"`
	Time   time.Time `json:"time"`
}

type RunRequest struct {
	SessionID string
	ProjectID string
	Chat      string
	Project   *project.Project
	Modify    bool
}

type Agent struct {
	Cfg      *config.Config
	LLM      *llm.Client
	Memory   *memory.Manager
	Skills   *skill.Manager
	Projects *project.Manager
	Shell    *shell.Executor
	MCP      *mcp.Manager
	Analyzer *analyzer.Analyzer
	Planner  *planner.Planner
	Context  *ContextManager
}

func New(cfg *config.Config, client *llm.Client, mem *memory.Manager, skills *skill.Manager,
	projects *project.Manager, sh *shell.Executor, mcpc *mcp.Manager) *Agent {
	return &Agent{
		Cfg:      cfg,
		LLM:      client,
		Memory:   mem,
		Skills:   skills,
		Projects: projects,
		Shell:    sh,
		MCP:      mcpc,
		Analyzer: analyzer.New(client),
		Planner:  planner.New(client),
		Context:  &ContextManager{Cfg: cfg},
	}
}

func newEvent(kind string) Event { return Event{Type: kind, Time: time.Now()} }

func statusEvent(text string) Event {
	e := newEvent("status")
	e.Text = text
	return e
}

func warnEvent(text string) Event {
	e := newEvent("warning")
	e.Text = text
	return e
}

// startHeartbeat 在模型流式生成较慢时周期性输出进度，避免终端长时间无反馈。
func startHeartbeat(emit func(Event), step int) func() {
	done := make(chan struct{})
	go func() {
		start := time.Now()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				emit(Event{
					Type: "progress",
					Step: step,
					Text: fmt.Sprintf("模型仍在生成内容（已等待 %d 秒）…", int(time.Since(start).Seconds())),
					Time: time.Now(),
				})
			}
		}
	}()
	return func() { close(done) }
}

// appendToolFailure 把失败的工具调用追加到运行时日志，单行一条便于 grep。
func appendToolFailure(path string, e Event) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	args := e.Args
	if len(args) > 800 {
		args = args[:800] + "…"
	}
	fmt.Fprintf(f, "%s tool=%s args=%s error=%s\n",
		time.Now().Format("2006-01-02 15:04:05"), e.Name, args, textutil.Truncate(e.Text, 800))
}

// Run 执行完整链路：记忆检索 → 聊天分析 → 网页规划 → Skill 匹配 → 工具循环 → 启动测试 → 保存记忆。
func (a *Agent) Run(ctx context.Context, req RunRequest, emit func(Event)) error {
	var emitMu sync.Mutex
	var toolLogPath string
	rawEmit := emit
	emit = func(e Event) {
		emitMu.Lock()
		// 工具调用失败写入运行时日志，便于事后定位是哪个工具、什么参数、什么错误。
		if toolLogPath != "" && e.Type == "tool_result" && !e.OK {
			appendToolFailure(toolLogPath, e)
		}
		rawEmit(e)
		emitMu.Unlock()
	}
	// 规划、分析等非流式调用期间定期上报进度，避免终端长时间无反馈。
	if a.LLM != nil {
		a.LLM.OnProgress = func(text string) { emit(statusEvent(text)) }
	}
	if req.Project == nil {
		err := errors.New("缺少项目上下文")
		emit(Event{Type: "error", Text: err.Error(), Time: time.Now()})
		return err
	}
	toolLogPath = filepath.Join(a.Cfg.RuntimeRoot, "tools", req.Project.ID+".log")
	if strings.TrimSpace(req.Chat) == "" {
		err := errors.New("聊天内容为空")
		emit(Event{Type: "error", Text: err.Error(), Time: time.Now()})
		return err
	}
	// 只是重新打开已有项目时，不调用大模型，直接启动服务。
	if req.Modify && isOpenOnlyIntent(req.Chat) {
		_, err := a.openExisting(ctx, req.Project, emit)
		return err
	}
	start := time.Now()
	phaseStart := time.Now()

	collector := perf.NewCollector(req.Chat, a.Cfg.LLMModel)

	// 本 Agent 的入口就是“网页生成任务”：任何输入都进入分析 → 规划 → 生成流程，不做普通对话快路径。

	emit(statusEvent("读取长期记忆…"))
	memBlock := a.Memory.Retrieve(req.Chat, req.Project.ID)
	if strings.TrimSpace(memBlock) != "" {
		emit(Event{Type: "memory", Text: memBlock, Time: time.Now()})
	}

	emit(statusEvent("阶段 1/7 分析聊天内容（主题 / 关键信息 / 结构化摘要）…"))
	analysis, warn, analysisUsage := a.Analyzer.Analyze(ctx, req.Chat)
	if warn != "" {
		emit(warnEvent(warn))
		a.Projects.AddHistory(req.Project.ID, "警告", textutil.Truncate(warn, 300))
	}
	if analysis == nil {
		err := errors.New("聊天分析未产生结果")
		emit(Event{Type: "error", Text: err.Error(), Time: time.Now()})
		return err
	}
	emit(Event{Type: "analysis", Text: analysis.Summary, Data: analysis, Time: time.Now()})
	analysisMs := time.Since(phaseStart).Milliseconds()
	collector.SetStage("analysis", time.Duration(analysisMs)*time.Millisecond)
	if analysis.Source == "llm" {
		metric := perf.RoundMetric{Round: 1, Stage: "analysis", Model: a.Cfg.LLMModel, DurationMs: analysisMs, Success: true}
		if analysisUsage != nil {
			metric.InputTokens = analysisUsage.PromptTokens
			metric.OutputTokens = analysisUsage.CompletionTokens
			metric.CachedTokens = analysisUsage.Cached()
		}
		collector.Add(metric)
	}
	phaseStart = time.Now()

	// 把主题与偏好写入长期记忆，供后续会话检索（不依赖 LLM 摘要）。
	projectID := req.Project.ID
	a.Memory.UpsertEntry(memory.Entry{
		Kind: "project", Key: "主题:" + analysis.Category,
		Value: analysis.Topic + "｜" + textutil.Truncate(analysis.Summary, 120),
		Tags:  analysis.Entities, ProjectID: projectID, Source: analysis.Source,
	})
	for i, pref := range analysis.Preferences {
		if i >= 3 {
			break
		}
		a.Memory.UpsertEntry(memory.Entry{
			Kind: "preference", Key: fmt.Sprintf("偏好%d", i+1), Value: pref,
			ProjectID: projectID, Source: analysis.Source,
		})
	}

	skillBlock := a.collectSkills(req.Chat, emit)
	skillSummaries := a.Skills.Summaries()

	emit(statusEvent("阶段 2/7 规划网页类型与页面结构…"))
	plan, warn, planUsage := a.Planner.Plan(ctx, analysis, memBlock, req.Project, req.Modify)
	if warn != "" {
		emit(warnEvent(warn))
		a.Projects.AddHistory(req.Project.ID, "警告", textutil.Truncate(warn, 300))
	}
	if plan == nil {
		err := errors.New("网页规划未产生结果")
		emit(Event{Type: "error", Text: err.Error(), Time: time.Now()})
		return err
	}
	emit(Event{Type: "plan", Text: plan.ToText(), Data: plan, Time: time.Now()})
	planMs := time.Since(phaseStart).Milliseconds()
	collector.SetStage("plan", time.Duration(planMs)*time.Millisecond)
	if plan.Source == "llm" {
		metric := perf.RoundMetric{Round: 2, Stage: "plan", Model: a.Cfg.LLMModel, DurationMs: planMs, Success: true}
		if planUsage != nil {
			metric.InputTokens = planUsage.PromptTokens
			metric.OutputTokens = planUsage.CompletionTokens
			metric.CachedTokens = planUsage.Cached()
		}
		collector.Add(metric)
	}
	phaseStart = time.Now()

	a.applyPlan(req.Project, plan)
	if proj, err := a.Projects.Get(req.Project.ID); err == nil {
		req.Project = proj
	}
	a.Memory.AppendMessages(req.SessionID, req.Project.ID, llm.Message{Role: "user", Content: req.Chat})

	llmReady := a.LLM.Configured() && analysis.Source == "llm"
	if !llmReady && req.Modify {
		err := fmt.Errorf("LLM 不可用，无法对已有网站做增量修改（避免用模板覆盖现有项目）：%s", warn)
		emit(Event{Type: "error", Text: err.Error(), Time: time.Now()})
		_, _ = a.Projects.UpdateFunc(req.Project.ID, func(p *project.Project) { p.Status = "error" })
		return err
	}

	reg, dispatcher := a.buildToolsFor(req.Project, req.Chat, req.Modify)
	executor := NewExecutor(dispatcher, a.Cfg.MaxToolCalls)
	toolCount := reg.Count()

	// 确定性预检：脚手架、入口兜底与文件树快照，不消耗 LLM 轮次。
	pre := PreflightResult{}
	if llmReady {
		pre = a.preflight(req.Project, plan, !req.Modify, emit)
		if pre.Scaffolded {
			a.Projects.AddHistory(req.Project.ID, "脚手架", "系统自动初始化 React + TS + Tailwind 骨架")
		}
	}

	mode := "llm"
	finalText := ""
	var runErr error
	if llmReady {
		emit(statusEvent("阶段 4/7 生成网页代码与内容…"))
	}
	if !llmReady {
		mode = "offline"
		finalText, runErr = a.runOffline(ctx, req, analysis, plan, dispatcher, executor, emit)
	} else {
		finalText, runErr = a.runLLMLoop(ctx, req, analysis, plan, memBlock, skillBlock, skillSummaries,
			pre.FileTree, reg, executor, collector, emit)
	}

	genMs := time.Since(phaseStart).Milliseconds()
	collector.SetStage("generate", time.Duration(genMs)*time.Millisecond)
	phaseStart = time.Now()

	// 统一收尾：校验文件、确保开发服务器、测试、状态与记忆
	devURL := ""
	if proj, err := a.Projects.Get(req.Project.ID); err == nil {
		devURL = proj.DevURL
	}
	if runErr == nil {
		if err := generator.Validate(req.Project.Dir); err != nil {
			runErr = fmt.Errorf("网页文件不完整：%w", err)
			emit(Event{Type: "error", Text: runErr.Error(), Time: time.Now()})
		}
	}
	// 只要入口不是占位页，就尝试启动开发服务器并打开浏览器；
	// 即使本轮任务以「未通过构建」结束，也让用户能直接看到已生成的部分。
	if runErr == nil && mode == "llm" {
		devURL, runErr = a.ensureRunnable(ctx, req.Project, plan.SiteName, dispatcher, emit)
	} else if mode == "llm" && !placeholderApp(req.Project.Dir) {
		if u, rerr := a.ensureRunnable(ctx, req.Project, plan.SiteName, dispatcher, emit); rerr != nil {
			emit(warnEvent("预览服务未启动：" + rerr.Error()))
		} else {
			devURL = u
		}
	}
	verifyMs := time.Since(phaseStart).Milliseconds()
	collector.SetStage("verify", time.Duration(verifyMs)*time.Millisecond)
	if st, _ := task.Load(a.Cfg.TasksRoot, req.Project.ID); st != nil {
		if runErr == nil {
			st.SetStep("启动并验证页面", devURL != "", devURL)
			st.Status = "completed"
		} else {
			st.Status = "paused"
		}
		a.saveTaskState(st)
	}

	if runErr == nil {
		_, _ = a.Projects.UpdateFunc(req.Project.ID, func(p *project.Project) {
			if devURL != "" {
				p.Status = "ready"
			} else if p.Status != "ready" {
				p.Status = "generated"
			}
			if !req.Modify || len(p.Pages) == 0 {
				p.Pages = plan.PageNames()
			}
		})
	} else {
		_, _ = a.Projects.UpdateFunc(req.Project.ID, func(p *project.Project) {
			p.Status = "error"
			p.History = append(p.History, project.HistoryItem{
				Time:    time.Now(),
				Kind:    "失败",
				Summary: textutil.Truncate(runErr.Error(), 300),
			})
			if len(p.History) > 60 {
				p.History = p.History[len(p.History)-60:]
			}
		})
	}

	proj, _ := a.Projects.Get(req.Project.ID)
	if proj != nil && devURL == "" {
		devURL = proj.DevURL
	}
	status := "error"
	if runErr == nil {
		status = "generated"
		if devURL != "" {
			status = "ready"
		}
	}
	if proj != nil {
		note := ""
		if runErr != nil {
			note = "本次会话失败：" + runErr.Error()
		}
		pages := plan.PageNames()
		if req.Modify && len(proj.Pages) > 0 {
			pages = proj.Pages
		}
		a.Memory.UpdateProjectState(proj.ID, proj.Name, plan.Summary, plan.TechStack, pages, status, note)
	}

	if finalText == "" && runErr == nil {
		if req.Modify {
			name := plan.SiteName
			if proj != nil && strings.TrimSpace(proj.Name) != "" {
				name = proj.Name
			}
			finalText = fmt.Sprintf("已完成对《%s》的增量修改，并通过 TypeScript 检查与生产构建。", name)
		} else {
			finalText = fmt.Sprintf("已根据聊天内容生成网站《%s》，包含 %d 个页面：%s。", plan.SiteName, len(plan.Pages), strings.Join(plan.PageNames(), "、"))
		}
		if devURL != "" {
			finalText += " 运行地址：" + devURL
		}
	}
	if finalText != "" {
		a.Memory.AppendMessages(req.SessionID, req.Project.ID, llm.Message{Role: "assistant", Content: finalText})
	}

	if runErr == nil && proj != nil {
		_, _ = a.Projects.UpdateFunc(proj.ID, func(p *project.Project) {
			p.History = append(p.History, project.HistoryItem{
				Time:    time.Now(),
				Kind:    "会话",
				Summary: textutil.Truncate("聊天："+req.Chat+"；"+executor.Summary(), 300),
			})
			if len(p.History) > 60 {
				p.History = p.History[len(p.History)-60:]
			}
		})
	}

	emit(statusEvent("阶段 7/7 写入长期记忆与会话摘要…"))
	sctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	if summary, err := a.Memory.SummarizeSession(sctx, req.SessionID, req.Project.ID, req.Project.Name); err == nil && summary != nil {
		emit(Event{Type: "memory_updated", Text: summary.Summary, Data: summary, Time: time.Now()})
	}
	cancel()

	if devURL != "" {
		emit(Event{Type: "dev_url", Text: devURL, Time: time.Now()})
	}

	tokenInfo := "未知（接口未返回 usage）"
	if executor.PromptTokens > 0 || executor.CompletionTokens > 0 {
		tokenInfo = fmt.Sprintf("输入 %d（缓存 %d）/ 输出 %d", executor.PromptTokens, executor.CachedTokens, executor.CompletionTokens)
	}
	report := collector.Finish()
	report.TotalMs = int64(time.Since(start).Milliseconds())
	reportPath := ""
	if a.Cfg.ReportsRoot != "" {
		if p, err := report.Write(a.Cfg.ReportsRoot, req.Project.ID); err == nil {
			reportPath = p
		}
	}
	timings := map[string]any{
		"analysis_ms":       analysisMs,
		"plan_ms":           planMs,
		"generate_ms":       genMs,
		"verify_ms":         verifyMs,
		"total_ms":          time.Since(start).Milliseconds(),
		"llm_calls":         report.LLMCalls,
		"llm_ms":            executor.LLMTime.Milliseconds(),
		"tool_calls":        executor.Calls,
		"tool_ms":           executor.ToolTime.Milliseconds(),
		"prompt_tokens":     executor.PromptTokens,
		"completion_tokens": executor.CompletionTokens,
		"cached_tokens":     executor.CachedTokens,
		"cache_hit_rate":    report.CacheHitRate(),
		"retries":           report.Retries,
	}
	if reportPath != "" {
		timings["report_path"] = reportPath
	}
	if a.MCP != nil {
		for k, v := range a.MCP.Stats() {
			timings["mcp_"+k] = v
		}
	}
	emit(Event{
		Type: "stats",
		Text: fmt.Sprintf("耗时统计：分析 %dms｜规划 %dms｜生成 %dms（LLM %d 轮 / %dms，Token %s）｜验证 %dms｜总计 %dms｜缓存命中 %.1f%%",
			analysisMs, planMs, genMs, report.LLMCalls, executor.LLMTime.Milliseconds(), tokenInfo, verifyMs,
			time.Since(start).Milliseconds(), report.CacheHitRate()*100),
		Data: timings,
		Time: time.Now(),
	})
	if reportPath != "" {
		emit(Event{Type: "perf", Text: report.Text() + "\n报告：" + reportPath, Time: time.Now()})
	}

	emit(Event{
		Type: "done",
		Text: finalText,
		OK:   runErr == nil,
		Time: time.Now(),
		Data: map[string]any{
			"project_id":      req.Project.ID,
			"session_id":      req.SessionID,
			"duration_ms":     time.Since(start).Milliseconds(),
			"tool_calls":      executor.Calls,
			"tool_errors":     executor.Errors,
			"changed_files":   keys(executor.ChangedFiles),
			"dev_url":         devURL,
			"steps":           executor.Calls,
			"tools_available": toolCount,
			"mode":            mode,
			"topic":           analysis.Topic,
			"category":        analysis.Category,
			"site_name":       plan.SiteName,
			"pages":           plan.PageNames(),
			"analysis_source": analysis.Source,
			"plan_source":     plan.Source,
			"timings":         timings,
			"performance":     report,
			"error":           errorString(runErr),
		},
	})
	return runErr
}

// progressNote 生成项目进度快照，约束模型只写缺失文件，避免重复重写同一批文件。
func progressNote(dir string) string {
	var b strings.Builder
	b.WriteString("[系统状态] 当前 src 文件：\n")
	_ = filepath.Walk(filepath.Join(dir, "src"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		switch filepath.Ext(p) {
		case ".tsx", ".ts", ".css":
		default:
			return nil
		}
		if rel, relErr := filepath.Rel(dir, p); relErr == nil {
			fmt.Fprintf(&b, "- %s\n", filepath.ToSlash(rel))
		}
		return nil
	})
	if placeholderApp(dir) {
		b.WriteString("src/App.tsx 仍是占位页，必须补全。\n")
	}
	if missing := missingLocalImports(dir); len(missing) > 0 {
		b.WriteString("缺失的本地模块：\n")
		for _, m := range missing {
			b.WriteString("  · ")
			b.WriteString(m)
			b.WriteString("\n")
		}
	}
	b.WriteString("下一轮只写缺失或确实有错误的文件，禁止重写已存在且内容正确的文件。")
	return textutil.Truncate(b.String(), 800)
}

// collectSkills 只匹配一次，仅注入与开发/测试最相关的 2 个技能正文，其余技能只保留摘要。
func (a *Agent) collectSkills(chat string, emit func(Event)) string {
	query := "网页 React TypeScript Vite Tailwind 组件 页面实现 测试 可访问性 " + textutil.Truncate(chat, 160)
	matches := a.Skills.Match(query, 6)
	selected := devStageSkills(matches)
	if len(selected) > 2 {
		selected = selected[:2]
	}
	for _, mt := range selected {
		emit(Event{
			Type: "skill",
			Name: mt.Skill.Name,
			Text: mt.Skill.Description,
			Data: map[string]any{"score": mt.Score, "path": mt.Skill.Path},
			Time: time.Now(),
		})
	}
	return a.Skills.BuildContextCached(selected, 3000)
}

// devStageSkills 只注入开发与测试阶段需要的 Skill，分析与规划 Skill 已由其内部提示词承担。
func devStageSkills(all []skill.Match) []skill.Match {
	var out []skill.Match
	for _, mt := range all {
		switch mt.Skill.Name {
		case "frontend-developer", "webpage-tester":
			out = append(out, mt)
		}
	}
	if len(out) == 0 {
		return all
	}
	return out
}

func (a *Agent) applyPlan(p *project.Project, plan *planner.SitePlan) {
	_, _ = a.Projects.UpdateFunc(p.ID, func(item *project.Project) {
		if !strings.EqualFold(p.Name, "待命名网站") && p.Name != "" {
			item.Name = p.Name
		} else {
			item.Name = plan.SiteName
		}
		item.Description = plan.Summary
		item.Pages = plan.PageNames()
		item.TechStack = plan.TechStack
		if item.Status == "draft" {
			item.Status = "generating"
		}
	})
}

func (a *Agent) buildTools(p *project.Project) (*tools.Registry, *tools.Dispatcher) {
	reg := tools.NewRegistry()
	tools.RegisterBuiltins(reg, tools.Deps{
		ProjectID:   p.ID,
		ProjectName: p.Name,
		ProjectDir:  p.Dir,
		Root:        a.Cfg.Root,
		Shell:       a.Shell,
		Skills:      a.Skills,
		Memory:      a.Memory,
		Projects:    a.Projects,
		MCP:         a.MCP,
		Config:      a.Cfg,
	})
	tools.RegisterMCPTools(reg, a.MCP)
	return reg, tools.NewDispatcher(reg)
}

// buildToolsFor 按任务意图注册工具：核心内置工具始终可用，
// MCP 仅在任务明确需要外部信息时按 Server 过滤注册，避免每轮发送几十个无关 Schema。
func (a *Agent) buildToolsFor(p *project.Project, chat string, modify bool) (*tools.Registry, *tools.Dispatcher) {
	reg := tools.NewRegistry()
	tools.RegisterBuiltins(reg, tools.Deps{
		ProjectID:   p.ID,
		ProjectName: p.Name,
		ProjectDir:  p.Dir,
		Root:        a.Cfg.Root,
		Shell:       a.Shell,
		Skills:      a.Skills,
		Memory:      a.Memory,
		Projects:    a.Projects,
		MCP:         a.MCP,
		Config:      a.Cfg,
	})
	// 新建任务已有文件树与已知脚手架，禁止把决策轮次浪费在反复读取上；
	// 增量修改仍保留读取能力。
	if !modify {
		reg.Unregister("list_files")
		reg.Unregister("read_file")
	}
	if servers := mcpServersFor(chat, a.MCP); len(servers) > 0 {
		tools.RegisterMCPToolsFiltered(reg, a.MCP, servers)
	}
	return reg, tools.NewDispatcher(reg)
}

// mcpServersFor 判断任务是否需要外部信息，并选出相关 MCP Server；默认返回 nil（不注册 MCP）。
func mcpServersFor(chat string, mgr *mcp.Manager) map[string]bool {
	if mgr == nil {
		return nil
	}
	lower := strings.ToLower(chat)
	triggers := []string{"http://", "https://", "网址", "链接", "抓取", "爬取", "搜索", "检索",
		"外部信息", "外部资料", "最新", "实时", "mcp", "天气", "新闻", "股票", "汇率", "地图"}
	if !containsAny(lower, triggers) {
		return nil
	}
	serverHints := []string{"fetch", "search", "browser", "playwright", "web", "http", "news", "weather", "map", "filesystem"}
	out := map[string]bool{}
	for _, st := range mgr.Tools() {
		name := strings.ToLower(st.Server)
		tool := strings.ToLower(st.Tool.Name)
		if containsAny(name, serverHints) || containsAny(tool, []string{"fetch", "search", "navigate", "browser", "weather", "read_url"}) {
			out[st.Server] = true
		}
	}
	return out
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// PreflightResult 是确定性预检的结果。
type PreflightResult struct {
	FileTree   string
	Scaffolded bool
}

// preflight 在不调用 LLM 的前提下完成脚手架与入口兜底，并抓取一次文件树快照。
func (a *Agent) preflight(p *project.Project, plan *planner.SitePlan, allowScaffold bool, emit func(Event)) PreflightResult {
	res := PreflightResult{}
	if allowScaffold {
		if _, err := os.Stat(filepath.Join(p.Dir, "package.json")); err != nil {
			if n, err := tools.WriteScaffold(p.Dir, plan.SiteName, "react-ts-tailwind"); err == nil {
				emit(statusEvent(fmt.Sprintf("阶段 3/7 已自动创建项目骨架（%d 个基础文件），直接生成页面…", n)))
				res.Scaffolded = true
			} else {
				emit(warnEvent("自动创建项目骨架失败：" + err.Error()))
			}
		}
	}
	if ensureRouterWrap(p.Dir) {
		emit(statusEvent("已自动为入口补上 <BrowserRouter>（缺少时 react-router 钩子会在运行时报错白屏）"))
	}
	res.FileTree = a.fileTree(p.ID)
	return res
}

// initTaskState 读取或初始化任务状态，确保已完成的步骤有记录。
func (a *Agent) initTaskState(projectID, goal string) *task.State {
	st, _ := task.Load(a.Cfg.TasksRoot, projectID)
	if st == nil {
		st = &task.State{
			ProjectID: projectID,
			Goal:      textutil.Truncate(goal, 200),
			Steps: []task.Step{
				{Name: "分析需求", Done: true},
				{Name: "规划页面结构", Done: true},
				{Name: "生成页面代码"},
				{Name: "TypeScript 检查"},
				{Name: "生产构建"},
				{Name: "启动并验证页面"},
			},
		}
	}
	st.Status = "running"
	return st
}

func (a *Agent) saveTaskState(st *task.State) {
	if st == nil || a.Cfg.TasksRoot == "" {
		return
	}
	_ = task.Save(a.Cfg.TasksRoot, st)
}

// runLLMLoop 是有限轮次的任务执行循环：模型只负责生成/修复代码，
// 文件检查、构建验证与入口兜底由 Go 侧确定性执行。
func (a *Agent) runLLMLoop(ctx context.Context, req RunRequest, an *analyzer.Analysis, plan *planner.SitePlan,
	memBlock, skillBlock, skillSummaries, fileTree string,
	reg *tools.Registry, executor *Executor, collector *perf.Collector, emit func(Event)) (string, error) {
	history := a.Memory.SessionMessages(req.SessionID)
	st := a.initTaskState(req.Project.ID, req.Chat)
	a.saveTaskState(st)
	if strings.TrimSpace(fileTree) == "" {
		fileTree = a.fileTree(req.Project.ID)
	}

	in := a.Context.BuildPrompt(PromptInput{
		Project:        req.Project,
		ProjectDir:     req.Project.Dir,
		Brief:          generator.Brief(an, plan),
		MemoryBlock:    memBlock,
		SkillBlock:     skillBlock,
		SkillSummaries: skillSummaries,
		FileTree:       fileTree,
		TaskState:      st.Text(),
		MCPTools:       countMCPTools(reg),
		Modify:         req.Modify,
	})
	systemPrompt := a.Context.SystemPrompt(in)

	messages := []llm.Message{{Role: "system", Content: systemPrompt}}
	messages = append(messages, a.Context.ShortMemory(history, 2)...)
	userMsg := "请根据以下聊天内容生成网站：\n" + req.Chat
	if req.Modify {
		userMsg = "请根据以下聊天内容对现有网站做增量修改，不要重建整个项目：\n" + req.Chat
	}
	messages = append(messages, llm.Message{Role: "user", Content: userMsg})

	// 主题变更检测：本次需求给出了更具体的对象（如“涮羊肉”），但已有站点仍残留旧主题文案时，
	// 把具体文件与行号交给模型，避免增量修改只改数据文件、首页仍显示旧主题。
	staleSubject := ""
	if req.Modify {
		if cur := generator.CurrentTopic(req.Project.Dir); cur != "" &&
			!strings.EqualFold(cur, an.Topic) &&
			!strings.Contains(an.Topic, cur) && !strings.Contains(cur, an.Topic) {
			staleSubject = cur
		}
	}
	consistencyMsg := func() string {
		if staleSubject == "" {
			return ""
		}
		hits := generator.FindStaleText(req.Project.Dir, staleSubject, 12)
		if len(hits) == 0 {
			return ""
		}
		var b strings.Builder
		fmt.Fprintf(&b, "[一致性检查] 本次聊天已明确更具体的主题「%s」，但以下文件仍保留旧主题「%s」的可见文案，必须改写：\n", an.Topic, staleSubject)
		for _, h := range hits {
			fmt.Fprintf(&b, "- %s:%d %s\n", h.File, h.Line, textutil.Truncate(h.Text, 80))
		}
		b.WriteString("要求：首页标题、首屏说明、板块名称与内容必须与「" + an.Topic + "」一致；删除“聊天记录只出现了旧主题”之类过期说明；聊天已确认的具体对象属于事实，必须使用，不要再标为待补充。")
		return b.String()
	}
	if note := consistencyMsg(); note != "" {
		messages = append(messages, llm.Message{Role: "user", Content: note})
		emit(statusEvent("检测到旧主题文案残留，已把需要同步更新的文件加入任务上下文"))
	}
	consistencyTries := 0
	defs := reg.Definitions()

	maxSteps := a.Cfg.MaxAgentSteps
	if maxSteps <= 0 {
		maxSteps = 6
	}
	maxRepairs := a.Cfg.MaxRepairs
	if maxRepairs <= 0 {
		maxRepairs = 2
	}

	finalText := ""
	var runErr error
	repairs := 0
	truncations := 0
	buildOK := false

	// buildAndMaybeRepair 执行真实构建：通过返回 done=true；失败则追加定向修复消息。
	buildAndMaybeRepair := func() (bool, error) {
		buildOK = false
		emit(statusEvent("执行 TypeScript 检查与生产构建…"))
		buildStart := time.Now()
		buildRes := generator.CheckBuild(ctx, req.Project.Dir, a.Cfg.BuildTimeout)
		collector.AddStage("build", time.Since(buildStart))
		tsOK := buildRes.Stage != "tsc" && buildRes.Stage != "deps"
		st.SetStep("TypeScript 检查", tsOK, "")
		st.SetStep("生产构建", buildRes.OK, "")
		a.saveTaskState(st)
		if buildRes.OK {
			buildOK = true
			return true, nil
		}
		if repairs < maxRepairs {
			repairs++
			executor.Retries++
			collector.AddRetry()
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: "构建未通过。请只修复以下错误，不要重写已完成内容，也不要修改无关文件：\n" + buildRes.ErrorText(),
			})
			emit(statusEvent(fmt.Sprintf("构建失败（%s），进行第 %d/%d 次定向修复…", buildRes.Stage, repairs, maxRepairs)))
			return false, nil
		}
		// 最后一次机会：为确实缺失的页面生成简化占位并重新构建；仍失败则如实报错。
		var stubbed []string
		if more := ensureOptionalPages(req.Project.Dir); len(more) > 0 {
			emit(statusEvent("已自动补全缺失的 404 页面: " + strings.Join(more, ", ")))
		}
		if created := ensurePageStubs(req.Project.Dir); len(created) > 0 {
			stubbed = append(stubbed, created...)
			emit(warnEvent("已为缺失页面生成简化占位（可继续修改补全）: " + strings.Join(created, ", ")))
		}
		if len(stubbed) > 0 {
			retryStart := time.Now()
			retry := generator.CheckBuild(ctx, req.Project.Dir, a.Cfg.BuildTimeout)
			collector.AddStage("build", time.Since(retryStart))
			st.SetStep("生产构建", retry.OK, "部分页面为简化占位")
			a.saveTaskState(st)
			if retry.OK {
				buildOK = true
				finalText = strings.TrimSpace(finalText + "\n注意：以下页面为自动生成的简化占位，需要后续会话补全：" + strings.Join(stubbed, ", "))
				return true, nil
			}
		}
		return false, fmt.Errorf("构建失败且已用完 %d 次自动修复：%s", maxRepairs, buildRes.ErrorText())
	}

	// 修改任务：先做一次确定性构建，把真实错误直接交给模型，避免模型用轮次自行试探。
	if req.Modify && !placeholderApp(req.Project.Dir) && len(missingLocalImports(req.Project.Dir)) == 0 {
		emit(statusEvent("检查现有项目的构建状态…"))
		baseStart := time.Now()
		baseRes := generator.CheckBuild(ctx, req.Project.Dir, a.Cfg.BuildTimeout)
		collector.AddStage("build", time.Since(baseStart))
		st.SetStep("TypeScript 检查", baseRes.Stage != "tsc" && baseRes.Stage != "deps", "")
		st.SetStep("生产构建", baseRes.OK, "")
		a.saveTaskState(st)
		if baseRes.OK {
			buildOK = true
		} else {
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: "现有项目构建未通过。请先用 read_file 读取相关文件（每个文件只读一次），再用 write_file 写入修复后的完整文件；只改必要文件，不要重写整个项目：\n" + baseRes.ErrorText(),
			})
			emit(warnEvent("现有项目构建未通过，已将错误信息交给模型定向修复"))
		}
	}

loop:
	for step := 0; step < maxSteps; step++ {
		if ctx.Err() != nil {
			runErr = ctx.Err()
			break
		}
		emit(Event{Type: "status", Step: step + 1, Text: fmt.Sprintf("第 %d/%d 步：模型决策中…", step+1, maxSteps), Time: time.Now()})
		stepStart := time.Now()
		stopHeartbeat := startHeartbeat(emit, step+1)
		resp, err := a.LLM.Chat(ctx, messages, defs, func(delta string) {
			emit(Event{Type: "assistant_delta", Step: step + 1, Text: delta, Time: time.Now()})
		})
		stopHeartbeat()
		cost := time.Since(stepStart)
		executor.LLMCalls++
		executor.LLMTime += cost
		var usage *llm.Usage
		if resp != nil {
			usage = resp.Usage
		}
		executor.AddUsage(usage)
		metric := perf.RoundMetric{
			Round:      step + 1,
			Stage:      "generate",
			Model:      a.Cfg.LLMModel,
			DurationMs: cost.Milliseconds(),
			Success:    err == nil,
		}
		if usage != nil {
			metric.InputTokens = usage.PromptTokens
			metric.OutputTokens = usage.CompletionTokens
			metric.CachedTokens = usage.Cached()
		}
		collector.Add(metric)
		if cost > 8*time.Second {
			emit(Event{Type: "progress", Step: step + 1, Text: fmt.Sprintf("第 %d 步生成完成（耗时 %d 秒）", step+1, int(cost.Seconds())), Time: time.Now()})
		}
		if err != nil {
			if errors.Is(err, llm.ErrOutputTruncated) && truncations < 2 {
				truncations++
				executor.Retries++
				collector.AddRetry()
				messages = append(messages, llm.Message{
					Role:    "user",
					Content: "上一次响应因达到输出长度上限被截断，工具参数不完整，文件没有写入。请减小单次输出：单个文件 ≤120 行，一次响应只写一个文件；大页面拆成多个组件，分多步用 write_file 写入。",
				})
				emit(statusEvent("响应被截断，已要求模型拆小文件后重试…"))
				continue
			}
			runErr = err
			emit(Event{Type: "error", Text: err.Error(), Time: time.Now()})
			break
		}
		messages = append(messages, *resp)

		if len(resp.ToolCalls) == 0 {
			finalText = strings.TrimSpace(resp.Content)
			if finalText != "" {
				emit(Event{Type: "assistant", Step: step + 1, Text: finalText, Time: time.Now()})
			}
			// 模型声称完成：先做确定性检查，再用真实构建判定是否成功。
			placeholder := placeholderApp(req.Project.Dir)
			missing := missingLocalImports(req.Project.Dir)
			if (placeholder || len(missing) > 0) && repairs < maxRepairs {
				repairs++
				executor.Retries++
				collector.AddRetry()
				var sb strings.Builder
				sb.WriteString("检测到项目仍有构建问题，请立即修复（只改必要文件，不要重写已完成内容）：\n")
				if placeholder {
					sb.WriteString("- src/App.tsx 仍是脚手架占位页（包含“页面正在生成中”），请用已生成的页面组合出完整入口。\n")
				}
				if len(missing) > 0 {
					sb.WriteString("- 以下本地模块被 import 但文件不存在，请用 write_file 创建缺失文件或修正 import：\n")
					for _, m := range missing {
						sb.WriteString("  · ")
						sb.WriteString(m)
						sb.WriteString("\n")
					}
				}
				messages = append(messages, llm.Message{Role: "user", Content: sb.String()})
				emit(statusEvent(fmt.Sprintf("检测到入口/引用问题，进行第 %d/%d 次定向修复…", repairs, maxRepairs)))
				continue
			}

			done, berr := buildAndMaybeRepair()
			if berr != nil {
				runErr = berr
				emit(Event{Type: "error", Text: runErr.Error(), Time: time.Now()})
				break
			}
			if done {
				if note := consistencyMsg(); note != "" && consistencyTries < 1 {
					consistencyTries++
					executor.Retries++
					collector.AddRetry()
					messages = append(messages, llm.Message{Role: "user", Content: note})
					emit(statusEvent(fmt.Sprintf("构建已通过，但仍有旧主题「%s」文案残留，进行第 %d/%d 次一致性修复…", staleSubject, consistencyTries, 1)))
					continue
				}
				break
			}
			continue
		}

		if strings.TrimSpace(resp.Content) != "" {
			emit(Event{Type: "assistant", Step: step + 1, Text: resp.Content, Time: time.Now()})
		}
		for i := range resp.ToolCalls {
			if executor.Exhausted() {
				runErr = errors.New("工具调用次数达到上限，任务可能未完成")
				break loop
			}
			call := &resp.ToolCalls[i]
			out, ok := executor.Execute(ctx, *call, emit)
			collector.AddTool()
			if !ok && executor.ConsecutiveErrors >= 6 {
				runErr = errors.New("连续 6 次工具调用失败，已停止以避免无效循环")
				emit(Event{Type: "error", Text: runErr.Error(), Time: time.Now()})
				break loop
			}
			content := out
			if content == "" {
				content = "（工具执行完成，无输出）"
			}
			if call.Function.Name == "shell_exec" {
				content = shell.SummarizeText(content, 1500)
			}
			// 读取文件需要完整内容才能改写，其余工具结果保持紧凑以节省 Token。
			limit := 1600
			if call.Function.Name == "read_file" {
				limit = 8000
			}
			messages = append(messages, llm.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    textutil.Truncate(content, limit),
			})
		}
		if len(executor.ChangedFiles) > 0 {
			st.SetStep("生成页面代码", true, executor.Summary())
			a.saveTaskState(st)
		}
		// 结构已完整时主动构建，避免模型用额外轮次做无意义的重复检查。
		if len(executor.ChangedFiles) > 0 && !placeholderApp(req.Project.Dir) && len(missingLocalImports(req.Project.Dir)) == 0 {
			done, berr := buildAndMaybeRepair()
			if berr != nil {
				runErr = berr
				emit(Event{Type: "error", Text: runErr.Error(), Time: time.Now()})
				break
			}
			if done {
				// 构建已通过，但旧主题文案仍有残留时，追加一次定向一致性修复。
				if note := consistencyMsg(); note != "" && consistencyTries < 1 {
					consistencyTries++
					executor.Retries++
					collector.AddRetry()
					messages = append(messages, llm.Message{Role: "user", Content: note})
					emit(statusEvent(fmt.Sprintf("构建已通过，但仍有旧主题「%s」文案残留，进行第 %d/%d 次一致性修复…", staleSubject, consistencyTries, 1)))
					continue
				}
				break
			}
		}
		if len(executor.ChangedFiles) > 0 {
			note := progressNote(req.Project.Dir)
			if c := consistencyMsg(); c != "" {
				note += "\n" + c
			}
			messages = append(messages, llm.Message{Role: "user", Content: note})
		}
		messages = trimOldToolMessages(messages)
	}

	// 主题变更后仍有旧主题文案残留时如实告知，绝不宣称已同步。
	if runErr == nil && staleSubject != "" {
		if hits := generator.FindStaleText(req.Project.Dir, staleSubject, 8); len(hits) > 0 {
			var sb strings.Builder
			fmt.Fprintf(&sb, "仍有 %d 处旧主题「%s」文案未同步为「%s」：\n", len(hits), staleSubject, an.Topic)
			for _, h := range hits {
				fmt.Fprintf(&sb, "- %s\n", h.String())
			}
			sb.WriteString("这些位置仍是旧内容，可在后续会话中继续修改。")
			emit(warnEvent(sb.String()))
			finalText = strings.TrimSpace(finalText + "\n" + strings.TrimSpace(sb.String()))
		}
	}

	// 未通过真实构建（含达到轮数上限）一律视为失败，绝不伪造成功。
	if runErr == nil && !buildOK {
		// 模型未能完成入口：按规划生成可运行的基线站点并如实说明，避免产出不可用项目。
		if files, ok := writeBaselineSite(req.Project.Dir, plan); ok {
			emit(warnEvent(fmt.Sprintf("模型未在 %d 轮内完成入口，已按规划生成可运行的基线页面（%d 个文件），可在后续会话继续补全。", maxSteps, len(files))))
			ensureRouterWrap(req.Project.Dir)
			buildStart := time.Now()
			res := generator.CheckBuild(ctx, req.Project.Dir, a.Cfg.BuildTimeout)
			collector.AddStage("build", time.Since(buildStart))
			st.SetStep("生产构建", res.OK, "基线页面")
			a.saveTaskState(st)
			if res.OK {
				buildOK = true
				finalText = strings.TrimSpace(finalText + "\n说明：模型未在限定轮次内完成全部页面，已按规划生成可运行的基线版本，可继续让 Agent 补全内容。")
			} else {
				emit(Event{Type: "error", Text: "基线站点构建失败：" + res.ErrorText(), Time: time.Now()})
			}
		}
	}
	if runErr == nil && !buildOK {
		runErr = fmt.Errorf("未在 %d 轮决策内完成并通过真实构建，任务已暂停（已生成文件保留，可继续修改）", maxSteps)
		emit(Event{Type: "error", Text: runErr.Error(), Time: time.Now()})
	}
	if runErr == nil && placeholderApp(req.Project.Dir) {
		runErr = errors.New("入口 src/App.tsx 仍是占位页，未真正生成页面")
		emit(Event{Type: "error", Text: runErr.Error(), Time: time.Now()})
	}
	if runErr == nil {
		st.Status = "generated"
		a.saveTaskState(st)
	}
	return finalText, runErr
}

// trimOldToolMessages 丢弃较早的工具调用轮次，避免上下文无限累积。
// 注意：不能改写工具参数内容，否则模型会把“已省略”标记当成真实文件内容复写。
func trimOldToolMessages(messages []llm.Message) []llm.Message {
	const keepSteps = 2
	var starts []int
	for i := range messages {
		if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			starts = append(starts, i)
		}
	}
	if len(starts) <= keepSteps {
		return messages
	}
	cut := starts[len(starts)-keepSteps]
	out := make([]llm.Message, 0, len(messages)-cut+1)
	for i := 0; i < cut; i++ {
		if messages[i].Role == "tool" {
			continue
		}
		if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			continue
		}
		out = append(out, messages[i])
	}
	out = append(out, llm.Message{
		Role:    "system",
		Content: "（此前较早的工具执行记录已省略。已写入的文件都在项目目录中，需要时用 list_files / read_file 查看。）",
	})
	out = append(out, messages[cut:]...)
	return out
}

// runOffline 在 LLM 不可用时，用真实聊天分析与本地规划渲染可运行网站（明确标记为离线模式）。
func (a *Agent) runOffline(ctx context.Context, req RunRequest, an *analyzer.Analysis, plan *planner.SitePlan,
	dispatcher *tools.Dispatcher, executor *Executor, emit func(Event)) (string, error) {
	if req.Modify {
		err := errors.New("离线模式不支持增量修改。请配置可用 LLM 后重试")
		emit(Event{Type: "error", Text: err.Error(), Time: time.Now()})
		return "", err
	}
	emit(warnEvent("LLM 不可用，进入离线降级模式：网页内容仍来自聊天分析，但页面由本地结构化模板渲染，不是 LLM 生成。"))
	if a.Memory != nil {
		a.Memory.UpdateProjectState(req.Project.ID, req.Project.Name, plan.Summary, plan.TechStack, plan.PageNames(), "generating", "离线降级模式")
	}

	emit(statusEvent("初始化 React + TypeScript + Vite + Tailwind 项目…"))
	out, err := a.invoke(ctx, dispatcher, "scaffold_project", map[string]any{
		"stack": "react-ts-tailwind",
		"title": plan.SiteName,
	}, emit)
	if err != nil {
		return "", fmt.Errorf("创建项目骨架失败：%v\n%s", err, out)
	}

	if err := generator.BuildOffline(req.Project.Dir, an, plan); err != nil {
		return "", fmt.Errorf("渲染网页失败：%w", err)
	}
	executor.ChangedFiles["src/data/site.ts"] = true
	executor.ChangedFiles["src/App.tsx"] = true
	executor.ChangedFiles["src/pages/PageView.tsx"] = true

	if _, err := os.Stat(filepath.Join(req.Project.Dir, "node_modules")); err != nil {
		emit(statusEvent("安装 npm 依赖（首次较慢）…"))
		out, err := a.invoke(ctx, dispatcher, "shell_exec", map[string]any{
			"command":         "npm install --no-audit --no-fund --prefer-offline --loglevel=error",
			"timeout_seconds": 600,
		}, emit)
		if err != nil {
			return "", fmt.Errorf("npm install 失败：%v\n%s", err, textutil.Truncate(out, 2000))
		}
	}

	emit(statusEvent("阶段 5/7 启动开发服务器…"))
	out, err = a.invoke(ctx, dispatcher, "dev_server", map[string]any{"action": "start"}, emit)
	if err != nil {
		return "", fmt.Errorf("启动开发服务器失败：%v\n%s", err, out)
	}
	url := extractURL(out)

	if url != "" {
		emit(statusEvent("检查网页可访问性…"))
		testOut, testErr := a.invoke(ctx, dispatcher, "web_test", map[string]any{"url": url}, emit)
		if testErr != nil {
			return "", fmt.Errorf("网页检查失败：%v\n%s", testErr, testOut)
		}
		rr := generator.RenderCheck(ctx, url, plan.SiteName, 45*time.Second)
		if rr.Checked && !rr.OK {
			return "", fmt.Errorf("页面渲染检查未通过：%s", rr.Detail)
		}
	}

	final := fmt.Sprintf("已根据聊天内容生成网站《%s》（离线降级模式）。\n页面：%s\n内容来源：终端聊天分析；待补充信息已在页面中标注。",
		plan.SiteName, strings.Join(plan.PageNames(), "、"))
	if url != "" {
		final += "\n运行地址：" + url
	}
	return final, nil
}

// Open 只启动已有项目（安装缺失依赖、启动服务、测试），不调用大模型。
func (a *Agent) Open(ctx context.Context, p *project.Project, emit func(Event)) (string, error) {
	var emitMu sync.Mutex
	raw := emit
	emit = func(e Event) {
		emitMu.Lock()
		raw(e)
		emitMu.Unlock()
	}
	return a.openExisting(ctx, p, emit)
}

func (a *Agent) openExisting(ctx context.Context, p *project.Project, emit func(Event)) (string, error) {
	start := time.Now()
	if placeholderApp(p.Dir) {
		err := errors.New("该项目尚未完成：src/App.tsx 仍是脚手架占位页。请选择「输入修改要求」，让 Agent 补全入口页面")
		emit(Event{Type: "error", Text: err.Error(), Time: time.Now()})
		emit(Event{Type: "done", Text: err.Error(), Data: map[string]any{"mode": "open", "error": err.Error()}, Time: time.Now()})
		return "", err
	}
	_, dispatcher := a.buildTools(p)
	// 打开前先做 TypeScript 检查，避免直接打开一个必然白屏的项目。
	emit(statusEvent("检查项目 TypeScript 状态…"))
	tr := generator.TypeCheck(ctx, p.Dir, 120*time.Second, true)
	if !tr.OK {
		err := fmt.Errorf("项目存在 TypeScript 错误，已阻止打开空白页面：\n%s\n请选择「2. 输入修改要求」让 Agent 自动修复。", tr.ErrorText())
		_, _ = a.Projects.UpdateFunc(p.ID, func(pp *project.Project) { pp.Status = "error" })
		emit(Event{Type: "error", Text: err.Error(), Time: time.Now()})
		emit(Event{Type: "done", Text: err.Error(), Data: map[string]any{"mode": "open", "error": err.Error()}, Time: time.Now()})
		return "", err
	}
	// 只启动开发服务器：依赖缺失会自动安装；不做浏览器测试，避免无谓等待。
	out, err := dispatcher.Dispatch(ctx, "dev_server", `{"action":"start"}`)
	devURL := ""
	if proj, getErr := a.Projects.Get(p.ID); getErr == nil {
		devURL = proj.DevURL
	}
	if devURL == "" {
		devURL = firstURL(out)
	}
	// 真实浏览器渲染检查：确保打开后能看到内容，而不是空白页。
	if err == nil && devURL != "" {
		rr := generator.RenderCheck(ctx, devURL, "", 45*time.Second)
		if !rr.Checked {
			emit(warnEvent(rr.Detail))
		} else if !rr.OK {
			err = fmt.Errorf("页面渲染检查未通过：%s", rr.Detail)
			emit(Event{Type: "error", Text: err.Error(), Time: time.Now()})
			_, _ = a.Projects.UpdateFunc(p.ID, func(pp *project.Project) { pp.Status = "error" })
		} else {
			emit(statusEvent("渲染检查通过：" + rr.Detail))
		}
	}
	if err == nil && devURL != "" {
		emit(Event{Type: "dev_url", Text: devURL, Time: time.Now()})
		_, _ = a.Projects.UpdateFunc(p.ID, func(pp *project.Project) { pp.Status = "ready" })
	}
	text := "项目已启动（未调用大模型）。"
	if err != nil {
		text = "启动项目失败：" + err.Error()
	} else if devURL != "" {
		text = "项目已启动：" + devURL + "（未调用大模型）"
	}
	emit(Event{
		Type: "stats",
		Text: fmt.Sprintf("打开项目耗时 %dms（未调用大模型）", time.Since(start).Milliseconds()),
		Time: time.Now(),
	})
	emit(Event{
		Type: "done",
		Text: text,
		Data: map[string]any{"mode": "open", "dev_url": devURL, "error": errorString(err)},
		Time: time.Now(),
	})
	return devURL, err
}

func firstURL(s string) string {
	for _, field := range strings.Fields(s) {
		if strings.HasPrefix(field, "http://") || strings.HasPrefix(field, "https://") {
			return strings.TrimRight(field, "，。,.")
		}
	}
	return ""
}

// placeholderApp 判断入口文件是否仍是脚手架占位页。
func placeholderApp(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "src", "App.tsx"))
	return err == nil && strings.Contains(string(data), "页面正在生成中")
}

// missingLocalImports 找出被 import 但不存在的本地模块，避免 Vite 白屏/错误覆盖层。
func missingLocalImports(dir string) []string {
	return generator.CheckImports(dir)
}

const notFoundStub = `import { Link } from 'react-router-dom'

export default function NotFound() {
  return (
    <div className="mx-auto flex min-h-[60vh] max-w-2xl flex-col items-center justify-center gap-4 px-6 text-center">
      <p className="text-6xl font-bold text-slate-300">404</p>
      <h1 className="text-xl font-semibold text-slate-800">页面不存在</h1>
      <p className="text-sm text-slate-500">你访问的页面可能已被移动或删除。</p>
      <Link to="/" className="rounded-full bg-slate-900 px-5 py-2 text-sm text-white">
        返回首页
      </Link>
    </div>
  )
}
`

const pageStubTemplate = `export default function %s() {
  return (
    <div className="mx-auto max-w-3xl px-6 py-16">
      <h1 className="font-serif text-2xl text-slate-900">%s</h1>
      <p className="mt-3 text-sm text-slate-500">该页面内容尚未完整生成，可继续让 Agent 补全。</p>
    </div>
  )
}
`

// ensurePageStubs 为缺失的 pages/* 模块生成简化页面，保证项目仍可构建与预览。
func ensurePageStubs(dir string) []string {
	var created []string
	for _, item := range missingLocalImports(dir) {
		filePart, spec, ok := strings.Cut(item, " -> ")
		if !ok || !strings.Contains(filepath.ToSlash(spec), "/pages/") {
			continue
		}
		name := identName(filepath.Base(spec))
		if name == "" {
			continue
		}
		importer := filepath.Join(dir, filepath.FromSlash(filePart))
		target := filepath.Join(filepath.Dir(importer), filepath.FromSlash(spec))
		if filepath.Ext(target) == "" {
			target += ".tsx"
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			continue
		}
		content := fmt.Sprintf(pageStubTemplate, name, name)
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			continue
		}
		created = append(created, filepath.ToSlash(target))
	}
	return created
}

func identName(spec string) string {
	base := strings.TrimSuffix(filepath.Base(spec), filepath.Ext(spec))
	var b strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if name == "" {
		return ""
	}
	first := name[0]
	if first >= 'a' && first <= 'z' {
		name = string(first-32) + name[1:]
	}
	return name
}

var (
	routerHookRe = regexp.MustCompile(`use(Location|Navigate|Params|SearchParams|Outlet)|<Routes|<NavLink|<Link\b`)
	appTagRe     = regexp.MustCompile(`<App\s*/>`)
)

// ensureRouterWrap 修复“入口渲染 App 但外层没有 Router”的运行时崩溃：
// react-router 的 useLocation 等钩子必须在 <BrowserRouter> 内使用，否则整个应用白屏。
func ensureRouterWrap(dir string) bool {
	mainPath := filepath.Join(dir, "src", "main.tsx")
	raw, err := os.ReadFile(mainPath)
	if err != nil {
		return false
	}
	main := string(raw)
	if strings.Contains(main, "BrowserRouter") || strings.Contains(main, "HashRouter") || strings.Contains(main, "<Router") {
		return false
	}
	if !appTagRe.MatchString(main) {
		return false
	}
	app := ""
	for _, name := range []string{"App.tsx", "App.jsx"} {
		if data, readErr := os.ReadFile(filepath.Join(dir, "src", name)); readErr == nil {
			app = string(data)
			break
		}
	}
	if !strings.Contains(app, "react-router-dom") || !routerHookRe.MatchString(app) {
		return false
	}
	updated := appTagRe.ReplaceAllString(main, "<BrowserRouter>\n      <App />\n    </BrowserRouter>")
	for _, marker := range []string{`from './App'`, `from "./App"`} {
		if idx := strings.Index(updated, marker); idx >= 0 {
			lineStart := strings.LastIndex(updated[:idx], "\n") + 1
			updated = updated[:lineStart] + "import { BrowserRouter } from 'react-router-dom'\n" + updated[lineStart:]
			break
		}
	}
	if updated == main || !strings.Contains(updated, "BrowserRouter") {
		return false
	}
	return os.WriteFile(mainPath, []byte(updated), 0o644) == nil
}

// ensureOptionalPages 为缺失的 404 页面自动生成兜底组件，避免 Vite 解析失败导致白屏。
func ensureOptionalPages(dir string) []string {
	var created []string
	for _, item := range missingLocalImports(dir) {
		filePart, spec, ok := strings.Cut(item, " -> ")
		if !ok {
			continue
		}
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(spec), filepath.Ext(spec)))
		if base != "notfound" && base != "404" {
			continue
		}
		importer := filepath.Join(dir, filepath.FromSlash(filePart))
		target := filepath.Join(filepath.Dir(importer), filepath.FromSlash(spec))
		if filepath.Ext(target) == "" {
			target += ".tsx"
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(target, []byte(notFoundStub), 0o644); err != nil {
			continue
		}
		created = append(created, filepath.ToSlash(target))
	}
	return created
}

// isOpenOnlyIntent 识别“打开/启动/运行已有项目”这类无需模型参与的指令。
func isOpenOnlyIntent(chat string) bool {
	s := strings.TrimSpace(chat)
	if len([]rune(s)) > 30 {
		return false
	}
	hasOpen := false
	for _, kw := range []string{"打开", "启动", "运行", "预览"} {
		if strings.Contains(s, kw) {
			hasOpen = true
			break
		}
	}
	if !hasOpen {
		return false
	}
	return strings.Contains(s, "项目") || strings.Contains(s, "网站") || strings.Contains(s, "网页")
}

// ensureRunnable 确保依赖已安装、dev server 正在运行、网页可访问且真实渲染出内容。
func (a *Agent) ensureRunnable(ctx context.Context, p *project.Project, siteTitle string, dispatcher *tools.Dispatcher, emit func(Event)) (string, error) {
	if _, err := os.Stat(filepath.Join(p.Dir, "node_modules")); err != nil {
		emit(statusEvent("阶段 5/7 安装 npm 依赖…"))
		out, err := a.invoke(ctx, dispatcher, "shell_exec", map[string]any{
			"command":         "npm install --no-audit --no-fund --prefer-offline --loglevel=error",
			"timeout_seconds": 600,
		}, emit)
		if err != nil {
			return "", fmt.Errorf("npm install 失败：%v\n%s", err, textutil.Truncate(out, 2000))
		}
	}
	emit(statusEvent("阶段 5/7 启动开发服务器…"))
	out, err := a.invoke(ctx, dispatcher, "dev_server", map[string]any{"action": "start"}, emit)
	if err != nil {
		return "", fmt.Errorf("启动开发服务器失败：%v\n%s", err, out)
	}
	url := extractURL(out)
	if url == "" {
		if proj, err := a.Projects.Get(p.ID); err == nil {
			url = proj.DevURL
		}
	}
	if url == "" {
		return "", errors.New("未能获取开发服务器访问地址")
	}
	emit(statusEvent("阶段 6/7 测试网页可访问性…"))
	testOut, testErr := a.invoke(ctx, dispatcher, "web_test", map[string]any{"url": url}, emit)
	if testErr != nil {
		return url, fmt.Errorf("网页检查失败：%v\n%s", testErr, testOut)
	}
	// HTTP 可访问不代表页面有内容：再用本机浏览器无头渲染，拦截运行时白屏。
	emit(statusEvent("阶段 6/7 浏览器渲染检查…"))
	rr := generator.RenderCheck(ctx, url, siteTitle, 45*time.Second)
	if !rr.Checked {
		emit(warnEvent(rr.Detail))
	} else if !rr.OK {
		return url, fmt.Errorf("页面渲染检查未通过：%s", rr.Detail)
	} else {
		emit(statusEvent("渲染检查通过：" + rr.Detail))
	}
	return url, nil
}

func (a *Agent) invoke(ctx context.Context, dispatcher *tools.Dispatcher, name string, args map[string]any, emit func(Event)) (string, error) {
	argJSON, _ := json.Marshal(args)
	emit(Event{Type: "tool_call", Name: name, Args: string(argJSON), Time: time.Now()})
	start := time.Now()
	out, err := dispatcher.Dispatch(ctx, name, string(argJSON))
	display := out
	if err != nil {
		if display != "" {
			display += "\n"
		}
		display += "[ERROR] " + err.Error()
	}
	emit(Event{
		Type:   "tool_result",
		Name:   name,
		Text:   textutil.Truncate(display, 4000),
		Output: textutil.Truncate(display, 4000),
		OK:     err == nil,
		Data:   map[string]any{"duration_ms": time.Since(start).Milliseconds()},
		Time:   time.Now(),
	})
	return out, err
}

func (a *Agent) fileTree(projectID string) string {
	tree, err := a.Projects.Tree(projectID, 4)
	if err != nil {
		return ""
	}
	var b strings.Builder
	var walk func(node project.FileNode, prefix string, depth int)
	walk = func(node project.FileNode, prefix string, depth int) {
		if depth > 4 {
			return
		}
		if node.Name != "" {
			if node.IsDir {
				fmt.Fprintf(&b, "%s%s/\n", prefix, node.Name)
			} else {
				fmt.Fprintf(&b, "%s%s (%dB)\n", prefix, node.Name, node.Size)
			}
		}
		for _, child := range node.Children {
			walk(child, prefix+"  ", depth+1)
		}
	}
	walk(tree, "", 0)
	return b.String()
}

func countMCPTools(reg *tools.Registry) int {
	n := 0
	for _, t := range reg.List() {
		if strings.HasPrefix(t.Name(), "mcp__") {
			n++
		}
	}
	return n
}

var urlRe = regexp.MustCompile(`https?://[^\s"'<>）)]+`)

func extractURL(text string) string {
	return urlRe.FindString(text)
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
