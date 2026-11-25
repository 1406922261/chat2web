package agent

import (
	"fmt"
	"strconv"
	"strings"

	"chat2web/internal/config"
	"chat2web/internal/llm"
	"chat2web/internal/project"
	"chat2web/internal/textutil"
)

// ContextManager 负责构建四层上下文：
// 1) System Context：固定规则（稳定前缀，利于 Prompt Caching）
// 2) Task Context：任务目标、TaskPlan、执行状态、项目结构
// 3) Short Memory：最近必要的对话
// 4) Long Memory：相关历史检索结果
type ContextManager struct {
	Cfg *config.Config
}

type PromptInput struct {
	Project        *project.Project
	ProjectDir     string
	Brief          string
	MemoryBlock    string
	SkillBlock     string
	SkillSummaries string
	FileTree       string
	TaskState      string
	MCPTools       int
	Modify         bool
	Offline        bool
}

const systemCore = `你是 Chat2Web AI，一个能够把终端聊天内容转化为真实可运行网站的全栈工程师与网页设计师。

## 工作方式
你通过「思考 → 调用工具 → 观察结果 → 再决策」的循环独立推进任务。禁止只把代码以文本形式回复给用户，必须调用 write_file 把真实代码写入项目文件。

## 标准流程
1. 项目骨架已由系统自动创建（package.json / vite / tailwind 配置齐全），直接开始 write_file；不要再调用 scaffold_project，也不要重写配置文件。
2. 按任务上下文中的规划用 write_file 写出完整实现：src/App.tsx、src/pages/*、src/components/*、样式文件。页面必须有真实内容与可用交互，禁止空白占位。
3. 需要外部资料时，可调用已提供的 MCP 工具；MCP 结果必须真实，不得编造。
4. 不要手动执行 npm install：dev_server 启动时会自动安装缺失依赖。
5. 所有页面写完后停止调用工具并给出简短说明；系统会自动执行 TypeScript 检查与构建，失败时会把精简错误发给你做定向修复。

## 效率要求（重要）
- 一次响应可以同时返回多个工具调用：互不依赖的文件（多个页面、多个组件）应在同一响应中一起 write_file，减少往返次数。
- 脚手架已生成 package.json、vite.config.ts、tailwind.config.js、postcss.config.js、index.html、src/main.tsx，除非发现错误，不要重写这些配置文件。
- 单个源码文件控制在 120 行以内；多个互不依赖的小文件（如 2-3 个页面/组件）应在同一次响应中一起 write_file，减少往返轮次。文件过大时模型输出会被长度上限截断，导致工具参数不是合法 JSON。
- 系统最多允许 {{MAX_STEPS}} 轮决策：请优先用 {{TARGET_STEPS}} 轮写完所有页面与组件，不要在已完成后反复检查或重写同一批文件。
- 先写 src/types.ts 与 src/data 下的数据文件，把它们当作唯一数据源；页面与组件只能引用其中**已经导出**的成员和字段，禁止假设未定义的属性（否则 tsc 报 TS2305/TS2339）。数据文件与页面必须同一次写入完成，避免两边命名脱节。
- 在 src/components 中复用共享组件与统一样式，不要在每个页面重复大段代码。
- 修改项目源码统一使用内置 write_file（写完整文件）；不要用 MCP filesystem 的 edit_file 修改源码。
- 在 App.tsx / 页面组件中 import 的每个本地文件都必须真实存在；先创建文件，再写 import，避免 Vite 报 Failed to resolve import。
- 依赖安装后直接启动 dev_server，不要反复执行 npm run build；最终验证由系统统一执行。
- 当前运行在 Windows（cmd）：不要使用 /dev/null、ls、cat 等 Linux 命令；重定向请用 NUL。
- 同一条命令连续失败两次后必须更换方法或工具，不要重复尝试相同命令。
- 不要重复调用 dev_server / web_test，也不要反复 npm install。

## 增量修改
- 修改已有网站前先参考任务上下文中的文件结构，只 read_file 与本次需求直接相关的文件，不要逐个读取全部文件。
- 禁止重建项目或重写全部页面：只修改 1-3 个相关文件，其他文件保持不动。
- 聊天中出现的具体对象（菜品、城市、类型等）视为已确认事实，必须体现在站点标题、首屏文案与板块结构中；旧主题的可见文案必须全部替换，不能保留“聊天里只出现了旧主题”之类过期结论。
- 如果用户只是要求“打开 / 查看 / 运行”项目，不要修改任何代码，直接确认已有运行地址。
- 如果 src/App.tsx 里仍包含“页面正在生成中”，说明入口文件还是占位页，必须先补全 src/App.tsx 再处理其他修改。
- 保留原项目的可用功能，修改后重新验证。

## 内容红线
- 网页中的事实、数字、地点、人物、书名、菜品等只能来自任务上下文中的聊天分析结果。
- 信息不足时在页面中明确写“待补充：…”，严禁编造。
- 工具失败或测试失败必须如实说明，不能宣称成功。

## 设计要求
- 默认技术栈 React + TypeScript + Vite + Tailwind CSS，路由使用 react-router-dom，图标可用 lucide-react。
- Router 只能包裹一次：脚手架 main.tsx 可能已包含 BrowserRouter。若 App.tsx 中写了 <BrowserRouter>，必须同时删除 main.tsx 中的 Router 包装；否则运行时抛出 “You cannot render a <Router> inside another <Router>” 并整页白屏。两者只保留一处。
- 不同主题使用不同的页面结构、视觉语言与交互，禁止所有网站套用同一模板。
- 视觉风格、配色与规划中的 style/palette 保持一致；响应式适配移动端。
- 导航、筛选、标签、进度、折叠等交互要有真实逻辑。
`

// SystemCore 返回固定不变的系统规则（不含任何动态内容）。
func (c *ContextManager) SystemCore() string {
	steps := 0
	if c.Cfg != nil {
		steps = c.Cfg.MaxAgentSteps
	}
	if steps <= 0 {
		steps = 6
	}
	target := steps / 3
	if target < 2 {
		target = 2
	}
	out := strings.Replace(systemCore, "{{MAX_STEPS}}", strconv.Itoa(steps), 1)
	return strings.Replace(out, "{{TARGET_STEPS}}", fmt.Sprintf("%d-%d", target, target+2), 1)
}

// TaskInput 是任务上下文所需的动态信息。
type TaskInput struct {
	ProjectName string
	ProjectDir  string
	PlanBrief   string
	TaskState   string
	FileTree    string
	Modify      bool
	MCPTools    int
}

// TaskContext 构建任务目标、计划、执行状态与项目结构，总长不超过 3500 字符。
func (c *ContextManager) TaskContext(in TaskInput) string {
	var b strings.Builder
	b.WriteString("## 当前任务\n")
	if in.ProjectName != "" {
		fmt.Fprintf(&b, "- 项目：%s\n", in.ProjectName)
	}
	if in.ProjectDir != "" {
		fmt.Fprintf(&b, "- 工作目录：%s（所有文件路径都相对于此目录）\n", in.ProjectDir)
	}
	if in.MCPTools > 0 {
		fmt.Fprintf(&b, "- 已接入 %d 个 MCP 工具，可按需真实调用（工具名以 mcp__ 开头）\n", in.MCPTools)
	}
	if in.Modify {
		b.WriteString("- 任务类型：增量修改（只改相关文件，不要重建项目）\n")
	} else {
		b.WriteString("- 任务类型：新建网站\n")
		b.WriteString("- 执行要求：第一轮优先写出 src/App.tsx 与所有页面文件（src/pages/*.tsx），第二轮再补组件与样式；每轮写 2-4 个文件，单文件 ≤100 行。本轮不提供 read_file/list_files，脚手架内容已知，禁止探索，直接 write_file。\n")
	}
	if strings.TrimSpace(in.TaskState) != "" {
		b.WriteString("\n## 执行状态\n")
		b.WriteString(textutil.Truncate(in.TaskState, 1200))
		b.WriteString("\n")
	}
	if strings.TrimSpace(in.PlanBrief) != "" {
		b.WriteString("\n## 任务计划（JSON，事实与页面结构以此为准）\n")
		b.WriteString(in.PlanBrief)
		b.WriteString("\n")
	}
	if strings.TrimSpace(in.FileTree) != "" {
		b.WriteString("\n## 当前项目文件结构\n")
		b.WriteString(textutil.Truncate(in.FileTree, 2000))
		b.WriteString("\n")
	}
	out := b.String()
	if len([]rune(out)) > 3500 {
		out = string([]rune(out)[:3500])
	}
	return out
}

// ShortMemory 只保留最近 limit 条非工具消息，去重并截断，避免重复发送全部历史。
func (c *ContextManager) ShortMemory(msgs []llm.Message, limit int) []llm.Message {
	if limit <= 0 {
		limit = 2
	}
	var filtered []llm.Message
	seen := map[string]bool{}
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		content = textutil.Truncate(content, 800)
		key := m.Role + "\x00" + content
		if seen[key] {
			continue
		}
		seen[key] = true
		filtered = append(filtered, llm.Message{Role: m.Role, Content: content})
	}
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	return filtered
}

// LongMemory 裁剪并去重长期记忆检索结果（≤1200 字符）。
func (c *ContextManager) LongMemory(block string) string {
	block = strings.TrimSpace(block)
	if block == "" {
		return ""
	}
	var lines []string
	seen := map[string]bool{}
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		lines = append(lines, line)
	}
	return textutil.Truncate(strings.Join(lines, "\n"), 1200)
}

// BuildPrompt 按动态 Token 预算裁剪各层上下文，超预算时先裁剪文件树与长记忆。
func (c *ContextManager) BuildPrompt(in PromptInput) PromptInput {
	budget := 24000
	if c.Cfg != nil && c.Cfg.ContextCharBudget > 0 {
		budget = c.Cfg.ContextCharBudget
	}
	in.FileTree = textutil.Truncate(in.FileTree, 2000)
	in.MemoryBlock = c.LongMemory(in.MemoryBlock)
	in.SkillBlock = textutil.Truncate(in.SkillBlock, 3000)
	in.TaskState = textutil.Truncate(in.TaskState, 1200)

	fixed := len([]rune(systemCore)) + len([]rune(in.Brief)) + len([]rune(in.TaskState)) + 1200
	used := fixed + len([]rune(in.FileTree)) + len([]rune(in.MemoryBlock)) + len([]rune(in.SkillBlock))
	if used <= budget {
		return in
	}
	// 依次裁剪：文件树 → 长记忆 → 技能正文 → Brief
	over := used - budget
	for _, field := range []struct {
		value *string
		min   int
	}{
		{&in.FileTree, 400},
		{&in.MemoryBlock, 300},
		{&in.SkillBlock, 600},
		{&in.Brief, 1200},
	} {
		cur := len([]rune(*field.value))
		if cur <= field.min || over <= 0 {
			continue
		}
		cut := cur - field.min
		if cut > over {
			cut = over
		}
		*field.value = string([]rune(*field.value)[:cur-cut])
		over -= cut
	}
	return in
}

// SystemPrompt 兼容旧调用：固定核心 + 各层上下文（已按预算裁剪）。
func (c *ContextManager) SystemPrompt(in PromptInput) string {
	in = c.BuildPrompt(in)
	var b strings.Builder
	b.WriteString(c.SystemCore())
	b.WriteString("\n")
	b.WriteString(c.TaskContext(TaskInput{
		ProjectName: projectName(in.Project),
		ProjectDir:  in.ProjectDir,
		PlanBrief:   in.Brief,
		TaskState:   in.TaskState,
		FileTree:    in.FileTree,
		Modify:      in.Modify,
		MCPTools:    in.MCPTools,
	}))
	if in.SkillSummaries != "" {
		b.WriteString("\n## 可用技能（按需调用 skill_load 加载完整内容）\n")
		b.WriteString(textutil.Truncate(in.SkillSummaries, 1200))
		b.WriteString("\n")
	}
	if in.SkillBlock != "" {
		b.WriteString("\n")
		b.WriteString(in.SkillBlock)
		b.WriteString("\n")
	}
	if in.MemoryBlock != "" {
		b.WriteString("\n## 长期记忆（仅参考与当前主题相关的偏好，禁止混入无关历史内容）\n")
		b.WriteString(in.MemoryBlock)
		b.WriteString("\n")
	}
	return b.String()
}

func projectName(p *project.Project) string {
	if p == nil {
		return ""
	}
	return p.Name
}

// History 兼容旧调用：等价于 ShortMemory。
func (c *ContextManager) History(msgs []llm.Message, limit int) []llm.Message {
	return c.ShortMemory(msgs, limit)
}
