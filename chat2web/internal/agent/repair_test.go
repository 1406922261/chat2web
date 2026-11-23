package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingLocalImports(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "src", "App.tsx"), `import Home from "./pages/Home";
import NotFound from "./pages/NotFound";
import Layout from "./components/Layout";
`)
	mustWrite(t, filepath.Join(dir, "src", "pages", "Home.tsx"), `export default function Home() { return null }`)
	mustWrite(t, filepath.Join(dir, "src", "components", "Layout.tsx"), `export default function Layout() { return null }`)

	missing := missingLocalImports(dir)
	if len(missing) != 1 || !strings.Contains(missing[0], "pages/NotFound") {
		t.Fatalf("missing = %v, want only pages/NotFound", missing)
	}

	mustWrite(t, filepath.Join(dir, "src", "pages", "NotFound.tsx"), `export default function NotFound() { return null }`)
	if got := missingLocalImports(dir); len(got) != 0 {
		t.Fatalf("after creating file, missing = %v", got)
	}
}

func TestEnsureOptionalPages(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "src", "App.tsx"), `import NotFound from "./pages/NotFound";
import Details from "./pages/Details";`)
	mustWrite(t, filepath.Join(dir, "src", "pages", "Details.tsx"), `export default function Details() { return null }`)

	created := ensureOptionalPages(dir)
	if len(created) != 1 {
		t.Fatalf("created = %v, want 1 NotFound stub", created)
	}
	if got := missingLocalImports(dir); len(got) != 0 {
		t.Fatalf("missing after stub = %v", got)
	}
}

func TestEnsurePageStubs(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "src", "App.tsx"), `import Taste from "./pages/Taste";`)
	created := ensurePageStubs(dir)
	if len(created) != 1 {
		t.Fatalf("created = %v, want 1 page stub", created)
	}
	if got := missingLocalImports(dir); len(got) != 0 {
		t.Fatalf("missing after stub = %v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "src", "pages", "Taste.tsx"))
	if err != nil || !strings.Contains(string(data), "export default function Taste") {
		t.Fatalf("stub content invalid: %s (%v)", string(data), err)
	}
}

func TestPlaceholderApp(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "src", "App.tsx"), "页面正在生成中")
	if !placeholderApp(dir) {
		t.Fatal("placeholder should be detected")
	}
	mustWrite(t, filepath.Join(dir, "src", "App.tsx"), "export default function App() { return null }")
	if placeholderApp(dir) {
		t.Fatal("real entry should not be treated as placeholder")
	}
}

func TestEnsureRouterWrap(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "src", "main.tsx"), `import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(<App />)
`)
	mustWrite(t, filepath.Join(dir, "src", "App.tsx"), `import { useLocation } from 'react-router-dom'

export default function App() {
  const { pathname } = useLocation()
  return <div>{pathname}</div>
}
`)
	if !ensureRouterWrap(dir) {
		t.Fatal("missing Router should be detected and fixed")
	}
	data, err := os.ReadFile(filepath.Join(dir, "src", "main.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "import { BrowserRouter } from 'react-router-dom'") || !strings.Contains(got, "<BrowserRouter>") {
		t.Fatalf("main.tsx not wrapped: %s", got)
	}
	if ensureRouterWrap(dir) {
		t.Fatal("already wrapped main.tsx should not be modified again")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
