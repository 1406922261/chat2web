---
name: frontend-developer
description: 使用 React + TypeScript + Vite + Tailwind CSS 实现真实可运行的网页项目，包括路由、页面组件、数据文件、交互逻辑与依赖安装。
triggers: 前端开发, React, TypeScript, Vite, Tailwind, 组件, 页面实现, 路由, 前端工程, frontend, react, typescript, vite, tailwind, component
tags: react, typescript, vite, tailwind, implementation
---

# 前端实现规范

## 技术基线

- React 18 + TypeScript + Vite + Tailwind CSS。
- 多页面使用 `react-router-dom` 的 `BrowserRouter`。
- 图标可用 `lucide-react`；不引入体积过大的 UI 框架。
- 所有内容数据集中放在 `src/data/`，页面组件只负责渲染，便于增量修改。

## 项目文件

```
package.json
index.html
vite.config.ts
tailwind.config.js
src/main.tsx
src/App.tsx
src/index.css
src/types.ts
src/data/site.ts
src/pages/*.tsx
src/components/*.tsx
```

## 工作流程

1. 项目没有 `package.json` 时先调用 `scaffold_project`（stack=react-ts-tailwind）。
2. 用 `write_file` 写入完整文件内容；不要只写片段，也不要让文件停在 TODO 状态。
3. 需要查看已有代码时先 `read_file` / `list_files`，修改时保持其余页面不变。
4. 依赖缺失时调用 `shell_exec` 执行 `npm install`。
5. 用 `dev_server` 启动，拿到真实 URL；不要自己猜测端口。
6. 完成后确保 `npm run build` 或 dev server 至少能正常编译。

## 编码要求

- **内容来自事实**：页面文案与数据取自聊天分析结果；缺失信息写“待补充：…”。
- **禁止空白占位**：不能出现 `Lorem ipsum`、“这里是内容”之类占位文本。
- **交互真实**：筛选、标签、折叠、搜索必须有实际状态与过滤逻辑。
- **组件拆分**：页面超过 120 行时拆出组件；重复渲染逻辑抽成组件。
- **类型安全**：数据文件定义 interface，避免 `any`。
- **样式一致**：颜色从 palette 读取，间距与圆角统一；深色/浅色主题二选一，不要混用。
- **响应式**：使用 `md:` / `lg:` 断点，移动端单列布局。

## 增量修改

- 先读目标文件，再做最小改动。
- 不要因为新增一个模块而重写整个 `App.tsx`。
- 修改数据结构时同步更新引用处。
- 改完重新运行 dev server / 测试，确认没有破坏原有页面。

## 常见错误

- 使用未安装的依赖 → 先安装再引用。
- 路由 path 重复 → 保证唯一，首页用 `/`。
- 文件路径大小写不一致 → Linux 构建会失败。
- 只写 `src/App.tsx` 却忘了 `src/main.tsx` 的挂载。
