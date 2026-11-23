# Chat2Web AI 测试报告

测试日期：2026-11-21  
环境：Windows / Go 1.26.5 / Node v24.18.0 / npm 11.16.0  
工作目录：`d:\Agent项目\网页生成Agent\chat2web`

## 1. 构建、静态检查与单元测试

```powershell
gofmt -l .                     # 无输出
go build ./...                 # 通过
go vet ./...                   # 通过
go test ./...                  # 全部 ok
gopls check -severity=hint ... # 0 条诊断
```

覆盖包：`analyzer`、`memory`、`mcp`、`project`、`shell`、`skill`、`tools`。

分析器测试用例：

- 旅游聊天 → `旅游攻略 / travel_guide`，主题包含“杭州”，关键事实 ≥ 3，实体非空；
- 健身聊天 → `健身计划 / fitness_plan`，偏好能捕获“胸部 / 背 / 腿”；
- 会议聊天 → `会议整理 / meeting_notes`；
- 不同领域产生不同 `website_type`，健身结果不含旅游字段；
- 空输入不会崩溃。

本轮同时修复了 gopls 报告的 46 条 warning 与 23 条 hint（字符串拼接、Scanner 错误检查、`slices.Contains`、`strings.Cut`、内置 `min` 等），行为不变。

## 2. LLM 配置（真实调用）

```env
LLM_BASE_URL=https://api.scnet.cn/api/llm/v1
LLM_MODEL=DeepSeek-V4.1-Flash-1017-Event
LLM_API_KEY=<已在本地 .env 配置，未写入本报告>
```

端到端运行中分析与规划均走真实 LLM，日志明确输出 `来源：llm`，未回退本地规则。

## 3. MCP 真实连接

| Server | 状态 | 发现工具数 | 说明 |
| --- | --- | --- | --- |
| filesystem | connected | 16 | `npx @modelcontextprotocol/server-filesystem` |
| playwright | connected | 25 | `npx @playwright/mcp@latest --browser msedge --headless` |
| fetch | 停用 | 0 | 需要 `uvx` |
| git | 停用 | 0 | 需要 `uvx` |

`web_test` 真实调用 Playwright MCP 的 `browser_navigate` 与 `browser_snapshot`，返回页面标题、控制台统计与 DOM 快照。

## 4. 端到端任务 A：杭州三日游

终端输入（多行，`END` 结束）：

```
用户：我想去杭州旅游，计划玩三天。
AI：可以去西湖、灵隐寺和西溪湿地。
用户：我喜欢美食，也想体验当地特色。
AI：可以安排龙井虾仁、片儿川等杭州特色美食。
END
```

实际结果：

- 分析：主题“杭州三日游攻略网站”，类别“旅游攻略”，`travel_guide`，来源 `llm`；
- 规划：站点《杭州三日 · 湖畔食光》，4 个页面（`/`、`/itinerary`、`/food-photo`、`/budget-stay`），来源 `llm`；
- 项目：`workspace/generated-websites/project-20261121-082357-1770b9`；
- Tool Calling：`scaffold_project` → `write_file` → `npm install` → `npm run build` → `dev_server` → `web_test`；
- 开发服务器：`http://127.0.0.1:56590/`，HTTP 200；
- 该轮 Playwright 仍指向未安装的 Chrome，`web_test` 退化为 HTTP 检查并明确说明；
- 生产构建：`tsc && vite build` 成功（36 modules）。

## 5. 端到端任务 B：30 天居家减脂（完整 LLM 链路）

终端输入：

```
用户：我想做一个 30 天健身计划网站。目标是一个月内减脂 3 公斤，每周训练四次。
AI：你希望重点练哪些部位？每次训练大概多久？
用户：主要练胸、背、腿，每次 45 分钟，家里只有一对哑铃。
用户：希望有每周训练表、动作库和饮食建议，手机上也要方便看。
END
```

实际结果：

- 分析：主题“30天居家减脂健身计划”，类别“健身计划”，`fitness_plan`，来源 `llm`；7 条关键信息、12 个实体、6 项待补充；
- 6 个 Skill 全部按阶段命中并注入（conversation-analyzer / content-extractor / webpage-planner / ui-designer / frontend-developer / webpage-tester）；
- 规划：站点《哑铃30天 · 居家减脂计划》，5 个页面（`/`、`/schedule`、`/exercises`、`/nutrition`、`/progress`），深墨蓝 + 暖白 + 橙色行动色，移动端底部导航；
- Tool Calling 共执行 32 步，模型自主写文件、安装依赖、构建、启动与测试；
- `npm install` 成功；`npm run build`（`tsc && vite build`）成功；
- `dev_server`：`http://127.0.0.1:58425/`；
- `web_test`：**Playwright + Edge 无头浏览器真实渲染成功**，页面标题「哑铃30天 · 居家减脂计划」；
- 首次快照发现 favicon 404（1 error），模型自行修复后复测为 0 error / 2 warning；
- 项目：`workspace/generated-websites/project-20261121-084838-86003a`，状态 `ready`；
- 整轮耗时 5m26s，退出时开发服务器被停止。

## 6. 不同主题的结构差异

| 杭州旅游（travel_guide） | 健身计划（fitness_plan） |
| --- | --- |
| 首页 | 计划总览 |
| 三日行程 `/itinerary` | 每周训练表 `/schedule` |
| 美食与拍照 `/food-photo` | 动作库 `/exercises` |
| 预算与住宿 `/budget-stay` | 饮食建议 `/nutrition` |
| — | 进度追踪 `/progress` |

页面数量、路径、模块、配色与交互均不同，由 LLM 依据聊天内容决定，未套用统一模板。

## 7. 长期记忆持久化

```
data/memory/user_profile.md
data/memory/long_term.json
data/memory/sessions/session-project-*.json
data/memory/summaries/20261121-*.md
data/memory/projects/project-*.json
```

- 每次任务结束写入会话摘要、项目记忆与长期偏好；
- 启动新任务时先检索长期记忆并注入分析/规划上下文；
- 菜单 3/4 可读取历史项目、状态、页面、运行地址与记忆内容。

## 8. 工作区清理

- 删除 3 个旧测试项目及其项目记录/会话/摘要；
- 删除上一版 WebForge 的全部代码与运行时目录；
- 删除测试临时文件（`chat2web.exe`、`e2e_input.txt`、`e2e_llm.log`、预览截图）；
- 停止遗留的 WebForge 服务与 MCP 进程，避免目录占用。

当前工作区仅保留 `chat2web/`，生成项目保留 2 个（旅游、健身）。

## 9. 未完成验证项

- **增量修改**：`2. 继续之前的项目` 已实现（读取文件树、最小改动提示、修改后重启与复测），但尚未用 LLM 实测一轮完整修改；
- **浏览器自动打开**：`rundll32 url.dll,FileProtocolHandler` 已实现，可用 `CHAT2WEB_NO_OPEN=1` 关闭；测试中未以日志确认弹窗；
- **fetch / git MCP**：默认停用，需要本机安装 `uv` 后启用；
- **Playwright 依赖**：本机未安装 Chrome，已在 `configs/mcp.json` 固定使用 Edge；换机器时需确保 Edge 存在。
