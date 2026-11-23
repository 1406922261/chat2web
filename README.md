# Chat2Web AI

把一段聊天内容，自动变成一个可运行的网站。

Chat2Web 是一个用 Go 编写的终端网页生成 Agent：读取你的聊天/需求文本，完成主题分析、页面规划、React + Vite + Tailwind 代码生成、构建与本地预览，并通过分层长期记忆与项目生命周期管理，保证同一网页反复修改时始终落在同一个项目里。

> 详细模块说明见 [chat2web/README.md](chat2web/README.md)。

## 功能

- 聊天内容 → 网站：主题/类别/实体抽取，页面规划，工具调用式增量修改
- 多技能协作：conversation-analyzer、content-extractor、webpage-planner、ui-designer、frontend-developer、webpage-tester
- MCP 工具接入：Playwright、filesystem 等，可在 `configs/mcp.json` 中扩展
- 安全 Shell：命令限制在工作区目录内，超时与输出截断
- 分层长期记忆：用户记忆 / 项目记忆 / 会话摘要 / 临时执行数据
- 项目生命周期：`temporary` → `active` → `saved` → `archived`，支持归档、恢复与回收站
- 自动清理：Playwright 日志 3 天、临时任务 7 天、临时报告最多 10 份
- 统一项目目录：`project.json` + `memory.md` + `source/` + `assets/` + `output/`

## 快速开始

要求：Go 1.22+、Node.js 18+、npm。

```powershell
git clone https://github.com/<your-name>/chat2web.git
cd chat2web
Copy-Item chat2web\.env.example chat2web\.env
# 编辑 chat2web\.env，填入 LLM 配置
go run ./chat2web
```

`.env` 关键配置：

```ini
LLM_BASE_URL=https://api.openai.com/v1
LLM_API_KEY=sk-...
LLM_MODEL=gpt-4o-mini
```

未配置 LLM 时会进入离线降级模式：仍可生成网站，但内容来自本地聊天分析与模板，而非大模型。

## 主菜单

```text
1. 新建网页生成任务
2. 打开 / 继续之前的项目
3. 查看历史生成记录
4. 记忆管理
5. 项目管理（保存 / 归档 / 恢复 / 清理）
6. 查看 Skill 列表
7. 查看 MCP 工具
8. 退出
```

## 项目结构

```text
.
├── go.work
├── chat2web/
│   ├── cmd/                    # 主程序与数据维护工具
│   │   ├── main.go
│   │   └── admin/              # 一次性盘点、清理与目录迁移
│   ├── internal/
│   │   ├── agent/              # 任务编排、上下文、工具执行
│   │   ├── analyzer/           # 聊天内容分析
│   │   ├── planner/            # 页面规划
│   │   ├── generator/          # React/Vite 脚手架与构建
│   │   ├── memory/             # 分层记忆、索引、摘要、清理
│   │   ├── project/            # 项目生命周期、目录、文件树、回收站
│   │   ├── tools/              # 内置工具与模板
│   │   ├── shell/              # 安全命令执行
│   │   ├── mcp/                # MCP 客户端
│   │   └── config/             # 配置与目录初始化
│   ├── skills/                 # 6 个技能文档
│   ├── configs/mcp.json
│   └── README.md
├── data/                       # 本地运行数据（已忽略，不入库）
└── workspace/                  # 生成的项目（已忽略，不入库）
```

每个生成项目使用统一目录：

```text
workspace/projects/{project-id}/
├── project.json   # 项目基本信息
├── memory.md      # 项目记忆
├── source/        # 网页源代码
├── assets/        # 用户素材（永不自动清理）
└── output/        # 最终构建成果
```

## 记忆与数据

- `data/memory/MEMORY.md`：跨项目用户偏好
- `workspace/projects/{id}/memory.md`：项目记忆，项目之间互相隔离
- `data/memory/sessions/YYYY-MM/`：每个项目一份会话摘要
- `data/memory/index.json`：轻量关键词索引，按需检索，不整体注入 Prompt
- `data/runtime/`：Playwright/Shell 日志、调试截图、临时任务，按策略自动过期
- `data/memory/backup/`：迁移与清理备份，不会自动删除

删除项目时，项目目录（含记忆、源码、素材、成果）会移入 `workspace/.trash/`，可通过菜单 5 恢复。

## 开发与测试

```powershell
go build ./...
go test ./...
```

数据盘点与清理工具：

```powershell
go run ./chat2web/cmd/admin          # 预演清理计划
go run ./chat2web/cmd/admin -apply   # 执行清理（自动备份）
```

## 使用与防爬声明

本项目源码公开仅用于学习、研究与个人非商业用途。

- 禁止任何形式的自动化爬虫、批量采集、镜像站点；
- 禁止将本项目代码或生成的页面内容用于训练 AI/LLM 模型；
- 禁止未经授权的商业使用与再分发。

仓库根目录的 `robots.txt` 声明了禁止抓取；对部署后的站点副本同样适用。如需商业授权，请联系仓库所有者。

详见 [LICENSE](LICENSE)。
