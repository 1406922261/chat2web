# Chat2Web AI

把终端里的聊天内容，变成一个真实可运行的网站。

Chat2Web AI 是一个以**终端为主要交互入口**的 AI 网页生成 Agent。它不会把聊天原文直接贴到页面上，而是先分析主题与关键信息，再规划网站类型与页面结构，匹配 Skill，通过工具循环写出 React 项目、安装依赖、启动开发服务器并检查可访问性。

```
用户终端聊天
   ↓ 聊天分析（主题 / 事实 / 偏好 / 缺失信息）
   ↓ 网页规划（类型 / 页面 / 模块 / 视觉风格）
   ↓ Skill 匹配 + 长期记忆检索
   ↓ LLM Tool Calling 循环（Shell / 文件 / MCP）
   ↓ 启动 dev server + 网页检查
   ↓ 保存项目、会话摘要与长期记忆
```

## 快速开始

环境要求：Go 1.22+、Node.js 18+（`npm` / `npx`）。

```powershell
cd chat2web
go run ./cmd/server
```

启动后进入菜单：

```
========================================
       Chat2Web AI
  Conversation to Website Agent
========================================

1. 新建网页生成任务
2. 打开 / 继续之前的项目
3. 查看历史生成记录
4. 记忆管理
5. 项目管理（保存 / 归档 / 恢复 / 清理）
6. 查看 Skill 列表
7. 查看 MCP 工具
8. 退出
```

新建任务时支持多行聊天输入：

- 输入 `END` 结束；
- `/cancel` 取消；
- `/file path/to/chat.txt` 读取文本文件中的聊天记录；
- 空输入会提示并取消，不会创建空项目。

生成完成后会打印项目目录、页面结构与访问地址，并自动用默认浏览器打开（设置 `CHAT2WEB_NO_OPEN=1` 可关闭）。

## LLM 配置

复制示例文件并填写：

```powershell
copy .env.example .env
```

```ini
LLM_BASE_URL=https://ark.cn-beijing.volces.com/api/v3
LLM_API_KEY=你的 Key
LLM_MODEL=你已开通的模型 ID
LLM_TEMPERATURE=0.4
```

也支持通用 OpenAI 兼容环境变量自动回退：`ARK_API_KEY`、`OPENAI_API_KEY`、`DEEPSEEK_API_KEY`、`MOONSHOT_API_KEY`、`DASHSCOPE_API_KEY`。程序会先读 `chat2web/.env`，再读工作区根目录 `.env`。

> **当前环境实测**：火山方舟 Key 可用，但账号未开通 `.env` 中配置的 `deepseek-v4-pro-ga-260813`，接口返回 `ModelNotOpen`。请到方舟控制台开通该模型，或改用其他已开通模型/OpenAI 兼容服务。

### 两种生成模式

| 模式 | 触发条件 | 行为 |
| --- | --- | --- |
| LLM Agent 模式 | LLM 可用 | 分析、规划、页面代码均由 LLM 通过 Tool Calling 自主完成 |
| 离线降级模式 | 未配置 Key，或 LLM 调用失败 | 用本地规则分析聊天 + 按类别动态规划结构，再由结构化模板渲染 React 网站；终端与项目内都会明确标注“不是 LLM 生成” |

离线模式没有伪造 LLM 输出：网页中的事实仍然来自聊天分析结果，缺失信息明确标注“待补充”，并且不支持对已有项目做增量修改（避免模板覆盖 LLM 生成的代码）。

## 核心能力

### 聊天分析引擎（`internal/agent/analyzer`）

- 优先用 LLM 输出动态 JSON：`topic / category / summary / key_facts / sections / preferences / entities / website_type / tone / missing`。
- 不写死旅游字段，`key_facts` 的 `kind/key` 由内容决定。
- LLM 失败时回退到本地规则分析，并在终端显式提示。
- 支持旅游、健身、学习、读书、项目、活动、产品、成长、会议、美食等类别，也允许模型创造新类别。

### 网页规划引擎（`internal/agent/planner`）

- 自主决定网站名称、类型、页面数量、页面结构、视觉风格、配色、交互与技术栈。
- 不同主题生成不同结构，例如：
  - 健身：首页 / 训练目标 / 每周安排 / 动作库 / 进度记录
  - 旅游：首页 / 行程安排 / 景点推荐 / 美食推荐 / 预算与贴士
  - 读书：首页 / 读书笔记 / 书籍与作者 / 摘录与行动
- 规划结果先经过校验（页面唯一、模块非空、缺失内容标记），再进入开发。

### Skill 技能系统（`skills/`）

自动扫描 `skills/*/SKILL.md`，读取 frontmatter（name / description / triggers），按阶段真匹配并注入系统提示词：

| Skill | 职责 |
| --- | --- |
| conversation-analyzer | 分析聊天上下文与主题 |
| content-extractor | 提取关键信息、实体、偏好并去重 |
| webpage-planner | 规划页面与信息架构 |
| ui-designer | 视觉风格、配色、排版、交互 |
| frontend-developer | React / TS / Vite / Tailwind 实现规范 |
| webpage-tester | 浏览器测试与修复循环 |

新增 Skill 只需创建目录与 `SKILL.md`，重启后自动生效。

### 长期记忆（`data/memory/` + `data/sessions/`）

```
data/
  memory/
    memory.md          # 全局长期记忆（跨项目偏好，可手动编辑手动备注）
    index.json         # 轻量检索索引（记忆类型 / 项目 / 文件 / 关键词）
  projects/{id}/
    memory.md          # 项目长期记忆（跟随项目目录）
  sessions/YYYY-MM/    # 每个项目一份会话摘要，按月归档
  backups/             # 迁移与清理备份，永不自动删除
```

- 记忆分两层：全局长期记忆（`data/memory/memory.md`）与项目长期记忆（`data/projects/{id}/memory.md`）；会话摘要仅作过程记录。
- 会话摘要写入 `data/sessions/`，与全局记忆解耦；同一项目的多次会话合并为一份，历史原文不再永久保留。
- 新任务只按关键词检索相关记忆并限制注入预算，不会把所有历史塞进 Prompt。
- 项目专属偏好会自动迁入该项目记忆，避免跨项目污染。
- 临时数据分两类：运行日志与截图在 `data/logs/runtime/`（Playwright 日志保留 3 天），报告成果在 `data/exports/reports/`（最多 10 份），任务状态在 `data/sessions/tasks/`（保留 7 天）；长期记忆不自动过期。

### MCP Client（`configs/mcp.json`）

使用标准 MCP stdio JSON-RPC：`initialize → notifications/initialized → tools/list → tools/call`，支持多 Server、工具发现、参数透传与真实调用。

默认配置：

- `filesystem`（启用，npx）— 实测连接成功，发现 16 个工具；
- `playwright`（启用，npx）— 实测连接成功，发现 25 个工具，用于浏览器测试；
- `fetch`（默认停用，`uvx mcp-server-fetch`）；
- `git`（默认停用，`uvx mcp-server-git`）。

启用 fetch/git 需要安装 [uv](https://docs.astral.sh/uv/)，或把 `command/args` 换成你环境中的等价服务。MCP 工具在 Agent 中统一以 `mcp__server__tool` 命名注册。

### Shell 执行（`internal/tools/shell`）

- 基于 `os/exec`，限制在项目根 `data/projects/` 内；
- 命令白名单（npm / npx / node / git 等）；
- 危险命令与越界路径拦截；
- 执行超时、输出长度限制；
- 开发服务器进程管理（启动 / 停止 / 日志）。

`npm install`、`npm run dev` 均由 Agent 真实执行，不使用模拟结果。

## 目录结构

```
chat2web/
  cmd/server/main.go          # 终端菜单、多行输入、事件渲染、浏览器打开
  cmd/admin/                  # 存储审计与迁移工具（默认只读，-apply 执行）
  internal/
    agent/                    # Agent 主循环、工具执行器、系统提示词
      analyzer/               # 聊天分析（LLM + 本地规则）
      planner/                # 网页规划（LLM + 本地规则）
    memory/                   # 全局记忆 / 项目记忆 / 摘要 / 检索 / 迁移
    project/                  # 项目元数据、生命周期、统一目录、版本、回收站
    session/                  # 会话摘要文件的落盘位置与读写
    tools/                    # 工具目录
      mcp/                    # MCP Client 与多 Server 管理
      shell/                  # 安全 Shell 执行与进程管理
      skill/                  # SKILL.md 扫描、匹配、上下文注入
    generator/                # 内容简报、离线渲染、文件校验
    config/                   # .env 加载与目录初始化
    llm/                      # OpenAI 兼容流式 Chat + Tool Calling
    task/                     # 每个会话的任务状态
    textutil/                 # 文本清洗与关键词提取
    perf/                     # 性能计时辅助
  skills/                     # 6 个技能文档
  configs/mcp.json
  data/                       # 运行时数据（见下）
  README.md
  go.mod
```

运行时数据统一在 `data/` 下，`data/projects` 是唯一的项目存储位置：

```
data/
  memory/
    memory.md      # 全局长期记忆
    index.json     # 轻量检索索引
  projects/
    project-xxx/
      assets/      # 素材
      source/      # 网页源代码（React / Vite）
      output/      # 最终构建成果
      memory.md    # 项目记忆
      project.json # 项目元数据（唯一来源）
  sessions/        # 会话摘要（YYYY-MM/）与任务状态（tasks/）
  logs/            # 运行日志与截图（runtime/）
  exports/         # 报告等产出（reports/）
  backups/         # 迁移与清理备份、回收站（trash/）
```

每个项目使用统一目录，反复修改同一个网页会复用同一个项目，不会不断新建：

```
data/projects/{project-id}/
  project.json   # 项目元数据（唯一来源）
  memory.md      # 项目记忆
  source/        # 网页源代码（React / Vite）
  assets/        # 用户上传或需要保留的素材
  output/        # 最终构建成果（source/dist 复制而来）
```

项目生命周期：`temporary`（临时生成）→ `active`（正在修改）→ `saved`（用户明确保存，永久保留）→ `archived`（主动归档）。归档与删除的项目目录会进入 `data/backups/trash/`，可随时恢复；迁移前会写入 `data/backups/migrate-{ts}/`。

## 生成出的项目

默认模板：React 18 + TypeScript + Vite 5 + Tailwind CSS 3 + react-router-dom。

```powershell
cd data/projects/project-xxxx/source
npm install
npm run dev
npm run build
```

生成记录、页面列表、运行地址、会话摘要都会写入 `data/`，可通过菜单 2 / 3 / 4 / 5 查看。

## 安全说明

- Agent 只能读写 `data/projects/{id}/source` 下的项目文件（长期记忆目录由 Agent 专用接口写入）。
- Shell 命令受白名单、超时与输出截断限制。
- `data/`、`workspace/`、`.env` 已在 `.gitignore` 中排除。
- 浏览器自动打开在沙箱环境中可能被拦截；程序会打印 URL 供手动访问。

## 测试

```powershell
go build ./...
go test ./...
```

## 已知限制

1. **增量修改需要 LLM**：`继续之前的项目` 依赖 LLM 读取现有文件并做最小改动；离线模式会拒绝修改，避免覆盖原项目。
2. **网页管理面板未实现**：需求中为可选项（“可以额外开发”），当前交付聚焦终端主入口。
3. **Playwright 在受限沙箱中可能无法启动 Chrome**：`web_test` 会退化为 HTTP 可访问性检查，并明确标注未做浏览器渲染测试。
