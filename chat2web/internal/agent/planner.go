package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"chat2web/internal/agent/planner"
)

// writeBaselineSite 在模型未完成入口（App.tsx 仍为占位）时，依据规划生成可构建的基线站点。
// 仅在 App.tsx 仍是占位页时生效，绝不覆盖已生成的入口。
func writeBaselineSite(dir string, plan *planner.SitePlan) ([]string, bool) {
	if plan == nil || !placeholderApp(dir) {
		return nil, false
	}
	siteName := escapeJSXText(plan.SiteName)
	if siteName == "" {
		siteName = "我的网站"
	}

	type page struct {
		component string
		path      string
		label     string
		purpose   string
		sections  []string
	}
	var pages []page
	for i, p := range plan.Pages {
		path := strings.TrimSpace(p.Path)
		if !strings.HasPrefix(path, "/") || strings.Contains(path, ":") || strings.Contains(path, "*") {
			if i == 0 {
				path = "/"
			} else {
				path = fmt.Sprintf("/page%d", i+1)
			}
		}
		label := escapeJSXText(p.Name)
		if label == "" {
			label = fmt.Sprintf("页面 %d", i+1)
		}
		var sections []string
		for _, s := range p.Sections {
			if len(sections) >= 4 {
				break
			}
			if name := escapeJSXText(s.Title); name != "" {
				sections = append(sections, name)
			}
		}
		pages = append(pages, page{
			component: fmt.Sprintf("Page%d", i+1),
			path:      path,
			label:     label,
			purpose:   escapeJSXText(p.Purpose),
			sections:  sections,
		})
	}
	if len(pages) == 0 {
		pages = []page{{component: "Home", path: "/", label: "首页"}}
	}

	var created []string
	for _, p := range pages {
		var b strings.Builder
		fmt.Fprintf(&b, "export default function %s() {\n  return (\n    <section className=\"mx-auto max-w-4xl px-6 py-16\">\n", p.component)
		fmt.Fprintf(&b, "      <h1 className=\"text-3xl font-semibold text-stone-900\">%s</h1>\n", p.label)
		if p.purpose != "" {
			fmt.Fprintf(&b, "      <p className=\"mt-3 text-sm leading-7 text-stone-600\">%s</p>\n", p.purpose)
		}
		if len(p.sections) > 0 {
			b.WriteString("      <ul className=\"mt-8 grid gap-3 sm:grid-cols-2\">\n")
			for _, s := range p.sections {
				fmt.Fprintf(&b, "        <li className=\"rounded-xl border border-stone-200 bg-white p-4 text-sm text-stone-700\">%s</li>\n", s)
			}
			b.WriteString("      </ul>\n")
		}
		b.WriteString("    </section>\n  )\n}\n")
		target := filepath.Join(dir, "src", "pages", p.component+".tsx")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(target, []byte(b.String()), 0o644); err != nil {
			continue
		}
		created = append(created, filepath.ToSlash(target))
	}

	var app strings.Builder
	app.WriteString("import { NavLink, Route, Routes, Link } from 'react-router-dom'\n")
	for _, p := range pages {
		fmt.Fprintf(&app, "import %s from './pages/%s'\n", p.component, p.component)
	}
	app.WriteString("\nconst links = [\n")
	for _, p := range pages {
		fmt.Fprintf(&app, "  { to: '%s', label: '%s' },\n", p.path, p.label)
	}
	app.WriteString("]\n\nexport default function App() {\n  return (\n")
	app.WriteString("    <div className=\"min-h-screen bg-stone-50 text-stone-900\">\n")
	app.WriteString("      <header className=\"border-b border-stone-200 bg-white/80 backdrop-blur\">\n")
	app.WriteString("        <div className=\"mx-auto flex max-w-5xl flex-wrap items-center justify-between gap-3 px-6 py-4\">\n")
	fmt.Fprintf(&app, "          <Link to=\"/\" className=\"text-lg font-semibold\">%s</Link>\n", siteName)
	app.WriteString("          <nav className=\"flex flex-wrap gap-4 text-sm\">\n")
	app.WriteString("            {links.map((l) => (\n")
	app.WriteString("              <NavLink key={l.to} to={l.to} className={({ isActive }) => (isActive ? 'text-amber-600' : 'text-stone-500 hover:text-stone-900')}>\n")
	app.WriteString("                {l.label}\n")
	app.WriteString("              </NavLink>\n")
	app.WriteString("            ))}\n")
	app.WriteString("          </nav>\n        </div>\n      </header>\n      <main>\n        <Routes>\n")
	for i, p := range pages {
		path := p.path
		if i == 0 && path != "/" {
			path = "/"
		}
		fmt.Fprintf(&app, "          <Route path=\"%s\" element={<%s />} />\n", path, p.component)
	}
	fmt.Fprintf(&app, "          <Route path=\"*\" element={<%s />} />\n", pages[0].component)
	app.WriteString("        </Routes>\n      </main>\n")
	fmt.Fprintf(&app, "      <footer className=\"border-t border-stone-200 px-6 py-8 text-center text-xs text-stone-400\">%s · 由 Chat2Web 生成</footer>\n", siteName)
	app.WriteString("    </div>\n  )\n}\n")

	appPath := filepath.Join(dir, "src", "App.tsx")
	if err := os.WriteFile(appPath, []byte(app.String()), 0o644); err != nil {
		return created, false
	}
	created = append(created, filepath.ToSlash(appPath))
	return created, true
}

// escapeJSXText 避免规划文本中的 JSX 特殊字符破坏编译。
func escapeJSXText(s string) string {
	s = strings.TrimSpace(s)
	replacer := strings.NewReplacer("<", "＜", ">", "＞", "{", "｛", "}", "｝", "`", "'")
	return replacer.Replace(s)
}
