package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"chat2web/internal/analyzer"
	"chat2web/internal/planner"
	"chat2web/internal/textutil"
	"chat2web/internal/tools"
)

func nowString() string { return time.Now().Format("2006-01-02 15:04:05") }

// BuildOffline 在未配置可用 LLM 时，用聊天事实 + 规划结果渲染一个真实可运行的
// React + TypeScript + Vite + Tailwind 项目。它不伪造 LLM 输出，页面内容全部来自分析结果。
func BuildOffline(dir string, a *analyzer.Analysis, p *planner.SitePlan) error {
	if _, err := tools.WriteScaffold(dir, p.SiteName, "react-ts-tailwind"); err != nil {
		return err
	}
	data := toSiteData(a, p)
	files := map[string]string{
		"src/types.ts":           typesTemplate,
		"src/data/site.ts":       "import type { SiteData } from '../types'\n\nexport const site: SiteData = " + marshalJSON(data) + "\n",
		"src/pages/PageView.tsx": pageViewTemplate,
		"src/App.tsx":            appTemplate,
		"src/index.css":          fmt.Sprintf(indexCSS, themeBG(p.Theme), themeFG(p.Theme)),
		"CONTENT_NOTES.md":       contentNotes(a, p),
	}
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func themeBG(theme string) string {
	if theme == "dark" {
		return "#09090b"
	}
	return "#fbfaf7"
}

func themeFG(theme string) string {
	if theme == "dark" {
		return "#fafafa"
	}
	return "#1c1917"
}

func contentNotes(a *analyzer.Analysis, p *planner.SitePlan) string {
	var b strings.Builder
	b.WriteString("# 内容来源说明\n\n")
	b.WriteString("本网站由 Chat2Web AI 根据终端聊天记录生成（离线模板渲染模式，未调用 LLM）。\n\n")
	fmt.Fprintf(&b, "## 聊天主题\n\n%s（%s）\n\n", a.Topic, a.Category)
	fmt.Fprintf(&b, "## 聊天摘要\n\n%s\n\n", a.Summary)
	b.WriteString("## 页面结构\n\n")
	for _, page := range p.Pages {
		fmt.Fprintf(&b, "- %s（%s）：%s\n", page.Name, page.Path, page.Purpose)
	}
	b.WriteString("\n## 待补充信息\n\n")
	if len(a.Missing) == 0 {
		b.WriteString("- 无\n")
	} else {
		for _, m := range a.Missing {
			fmt.Fprintf(&b, "- %s\n", m)
		}
	}
	fmt.Fprintf(&b, "\n## 原始聊天记录\n\n```text\n%s\n```\n", textutil.Truncate(a.Transcript, 4000))
	return b.String()
}

const typesTemplate = `export interface SectionPlan {
  title: string
  description: string
  items: string[]
}

export interface PagePlan {
  name: string
  path: string
  purpose: string
  sections: SectionPlan[]
}

export interface SiteData {
  siteName: string
  websiteType: string
  summary: string
  style: string
  theme: 'light' | 'dark'
  palette: string[]
  pages: PagePlan[]
  interactions: string[]
  generatedAt: string
  source: string
}
`

const appTemplate = `import { BrowserRouter, NavLink, Route, Routes } from 'react-router-dom'
import { site } from './data/site'
import PageView from './pages/PageView'

const dark = site.theme === 'dark'
const shell = dark ? 'min-h-screen bg-zinc-950 text-zinc-100' : 'min-h-screen bg-stone-50 text-stone-900'
const muted = dark ? 'text-zinc-400' : 'text-stone-500'
const border = dark ? 'border-white/10' : 'border-black/10'

function App() {
  const accent = site.palette[1] || '#7c3aed'
  return (
    <BrowserRouter>
      <div className={shell}>
        <header className={'border-b ' + border}>
          <div className="mx-auto flex max-w-6xl flex-col gap-3 px-6 py-8 md:flex-row md:items-end md:justify-between">
            <div>
              <p className="text-xs uppercase tracking-[0.35em]" style={{ color: accent }}>
                {site.websiteType.replace(/_/g, ' ')}
              </p>
              <h1 className="mt-2 text-2xl font-semibold md:text-3xl">{site.siteName}</h1>
              <p className={'mt-3 max-w-2xl text-sm leading-relaxed ' + muted}>{site.summary}</p>
            </div>
            <p className={'text-xs ' + muted}>由 Chat2Web AI 根据聊天内容生成</p>
          </div>
          <nav className="mx-auto flex max-w-6xl gap-1 overflow-x-auto px-4 pb-4">
            {site.pages.map((page) => (
              <NavLink
                key={page.path}
                to={page.path}
                end={page.path === '/'}
                className={({ isActive }) =>
                  'whitespace-nowrap rounded-full px-4 py-2 text-sm transition ' +
                  (isActive ? 'font-medium text-white' : muted)
                }
                style={({ isActive }) => ({ backgroundColor: isActive ? accent : 'transparent' })}
              >
                {page.name}
              </NavLink>
            ))}
          </nav>
        </header>
        <main className="mx-auto max-w-6xl px-6 py-10">
          <Routes>
            {site.pages.map((page) => (
              <Route key={page.path} path={page.path} element={<PageView page={page} />} />
            ))}
            <Route path="*" element={<PageView page={site.pages[0]} />} />
          </Routes>
        </main>
        <footer className={'border-t ' + border}>
          <div className={'mx-auto max-w-6xl px-6 py-6 text-xs ' + muted}>
            <p>{site.style}</p>
            <p className="mt-1">生成时间：{site.generatedAt} · 内容仅来自聊天记录，待补充信息未做虚构填充。</p>
          </div>
        </footer>
      </div>
    </BrowserRouter>
  )
}

export default App
`

const pageViewTemplate = `import { useMemo, useState } from 'react'
import type { PagePlan } from '../types'
import { site } from '../data/site'

const dark = site.theme === 'dark'
const card = dark
  ? 'rounded-2xl border border-white/10 bg-white/[0.04] p-6'
  : 'rounded-2xl border border-black/10 bg-white p-6 shadow-sm'
const inputBorder = dark ? 'border-white/20' : 'border-black/20'

export default function PageView({ page }: { page: PagePlan }) {
  const [query, setQuery] = useState('')
  const accent = site.palette[2] || '#22d3ee'

  const sections = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return page.sections
    return page.sections
      .map((section) => ({
        ...section,
        items: section.items.filter(
          (item) => item.toLowerCase().includes(q) || section.title.toLowerCase().includes(q),
        ),
      }))
      .filter((section) => section.items.length > 0)
  }, [page, query])

  return (
    <div>
      <div className="mb-8">
        <p className="text-xs uppercase tracking-[0.3em]" style={{ color: accent }}>
          {page.path === '/' ? 'Overview' : 'Section'}
        </p>
        <h2 className="mt-2 text-3xl font-semibold">{page.name}</h2>
        <p className="mt-2 max-w-3xl text-sm opacity-70">{page.purpose}</p>
      </div>

      <div className="mb-6 flex flex-wrap items-center gap-3">
        <input
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="搜索本页内容…"
          className={'w-full max-w-sm rounded-full border bg-transparent px-4 py-2 text-sm outline-none ' + inputBorder}
        />
        <span className="text-xs opacity-60">共 {page.sections.length} 个模块</span>
      </div>

      {sections.length === 0 ? (
        <p className="text-sm opacity-60">没有匹配的内容。</p>
      ) : (
        <div className="grid gap-5 md:grid-cols-2">
          {sections.map((section) => (
            <section key={section.title} className={card}>
              <h3 className="text-lg font-medium">{section.title}</h3>
              {section.description ? <p className="mt-1 text-xs opacity-60">{section.description}</p> : null}
              <ul className="mt-4 space-y-2">
                {section.items.map((item, index) => (
                  <li key={index} className="flex gap-2 text-sm leading-relaxed">
                    <span
                      className="mt-2 h-1.5 w-1.5 shrink-0 rounded-full"
                      style={{ backgroundColor: accent }}
                    />
                    <span>{item}</span>
                  </li>
                ))}
              </ul>
            </section>
          ))}
        </div>
      )}

      <p className="mt-8 text-xs opacity-50">
        内容来源：终端聊天记录。标注“待补充”的信息尚未在聊天中出现，不做虚构填充。
      </p>
    </div>
  )
}
`

const indexCSS = `@tailwind base;
@tailwind components;
@tailwind utilities;

html, body, #root {
  min-height: 100%%;
}

body {
  margin: 0;
  font-family: Inter, system-ui, -apple-system, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', sans-serif;
  background: %s;
  color: %s;
  -webkit-font-smoothing: antialiased;
}

* {
  box-sizing: border-box;
}

a {
  color: inherit;
  text-decoration: none;
}
`
