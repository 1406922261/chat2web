package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 以下模板用于生成可靠的 React + TypeScript + Vite + Tailwind 项目骨架。
// Agent 会在骨架之上生成真实的页面与组件。

const packageJSONTemplate = `{
  "name": "%s",
  "private": true,
  "version": "0.1.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc && vite build",
    "preview": "vite preview"
  },
  "dependencies": {
    "lucide-react": "^0.454.0",
    "react": "^18.3.1",
    "react-dom": "^18.3.1",
    "react-router-dom": "^6.26.2"
  },
  "devDependencies": {
    "@types/react": "^18.3.11",
    "@types/react-dom": "^18.3.1",
    "@vitejs/plugin-react": "^4.3.2",
    "autoprefixer": "^10.4.20",
    "postcss": "^8.4.47",
    "tailwindcss": "^3.4.14",
    "typescript": "^5.6.3",
    "vite": "^5.4.9"
  }
}
`

const viteConfigTemplate = `import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    host: '127.0.0.1',
    port: 5173,
    strictPort: false,
  },
})
`

const tsconfigTemplate = `{
  "compilerOptions": {
    "target": "ES2020",
    "useDefineForClassFields": true,
    "lib": ["ES2020", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "skipLibCheck": true,
    "moduleResolution": "bundler",
    "allowImportingTsExtensions": true,
    "resolveJsonModule": true,
    "isolatedModules": true,
    "noEmit": true,
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": false,
    "noUnusedParameters": false,
    "noFallthroughCasesInSwitch": true
  },
  "include": ["src"]
}
`

const indexHTMLTemplate = `<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>%s</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
`

const postcssTemplate = `export default {
  plugins: {
    tailwindcss: {},
    autoprefixer: {},
  },
}
`

const tailwindTemplate = `/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      fontFamily: {
        sans: ['Inter', 'system-ui', '-apple-system', 'BlinkMacSystemFont', 'Segoe UI', 'sans-serif'],
      },
    },
  },
  plugins: [],
}
`

const mainTSXTemplate = `import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import './index.css'

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
`

const indexCSSTemplate = `@tailwind base;
@tailwind components;
@tailwind utilities;

:root {
  color-scheme: dark;
}

html, body, #root {
  min-height: 100%;
}

body {
  margin: 0;
  font-family: Inter, system-ui, -apple-system, 'Segoe UI', sans-serif;
  background: #09090b;
  color: #fafafa;
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

const appTSXTemplate = `function App() {
  return (
    <div className="min-h-screen flex items-center justify-center bg-zinc-950 text-zinc-100 px-6">
      <div className="w-full max-w-md rounded-2xl border border-white/10 bg-white/5 p-10 text-center">
        <div className="mx-auto mb-6 h-12 w-12 animate-spin rounded-full border-2 border-violet-500/30 border-t-violet-400" />
        <h1 className="text-xl font-semibold">页面正在生成中</h1>
        <p className="mt-3 text-sm text-zinc-400">
          Chat2Web AI Agent 正在根据你的需求编写页面，请稍候查看实时预览。
        </p>
      </div>
    </div>
  )
}

export default App
`

const viteEnvTemplate = `/// <reference types="vite/client" />
`

const gitignoreTemplate = `node_modules
dist
.vite
*.local
.DS_Store
`

const staticHTMLTemplate = `<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>%s</title>
    <link rel="stylesheet" href="./styles.css" />
  </head>
  <body>
    <main class="container">
      <h1>%s</h1>
      <p>页面正在生成中，请稍候。</p>
    </main>
    <script src="./script.js"></script>
  </body>
</html>
`

const staticCSSTemplate = `* { box-sizing: border-box; }
body {
  margin: 0;
  font-family: system-ui, -apple-system, 'Segoe UI', sans-serif;
  background: #0b0b0f;
  color: #f5f5f5;
  min-height: 100vh;
  display: grid;
  place-items: center;
}
.container {
  max-width: 640px;
  padding: 48px 24px;
  text-align: center;
}
h1 { font-size: 28px; margin-bottom: 12px; }
p { color: #a1a1aa; }
`

const staticJSTemplate = `console.log('Chat2Web generated site loaded');
`

// WriteScaffold 将项目骨架写入 dir，返回写入的基础文件数量。
// 供 scaffold_project 工具与离线降级生成器共用。
func WriteScaffold(dir, title, stack string) (int, error) {
	if strings.TrimSpace(title) == "" {
		title = "Chat2Web 网站"
	}
	files := map[string]string{}
	switch stack {
	case "static":
		files["index.html"] = fmt.Sprintf(staticHTMLTemplate, title, title)
		files["styles.css"] = staticCSSTemplate
		files["script.js"] = staticJSTemplate
	default:
		slug := slugify(title)
		files["package.json"] = fmt.Sprintf(packageJSONTemplate, slug)
		files["vite.config.ts"] = viteConfigTemplate
		files["tsconfig.json"] = tsconfigTemplate
		files["postcss.config.js"] = postcssTemplate
		files["tailwind.config.js"] = tailwindTemplate
		files["index.html"] = fmt.Sprintf(indexHTMLTemplate, title)
		files["src/main.tsx"] = mainTSXTemplate
		files["src/App.tsx"] = appTSXTemplate
		files["src/index.css"] = indexCSSTemplate
		files["src/vite-env.d.ts"] = viteEnvTemplate
		files[".gitignore"] = gitignoreTemplate
	}
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return 0, err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return 0, err
		}
	}
	return len(files), nil
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if r == '-' || r == '_' || r == ' ' {
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = fmt.Sprintf("chat2web-site-%d", len(s))
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}
