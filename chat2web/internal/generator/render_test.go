package generator

import (
	"strings"
	"testing"
)

func TestVisibleTextStripsHeadAndScripts(t *testing.T) {
	dom := `<html><head><title>午餐吃什么 · 今日决策器</title><script>var a = "<div>fake</div>"</script><style>.x{color:red}</style></head>
<body><div id="root"><h1>夏日衣橱</h1><p>早安 &amp; 欢迎</p></div><script src="/src/main.tsx"></script></body></html>`
	got := visibleText(dom)
	if !strings.Contains(got, "夏日衣橱") || !strings.Contains(got, "早安 & 欢迎") {
		t.Fatalf("visibleText 未保留正文: %q", got)
	}
	if strings.Contains(got, "fake") || strings.Contains(got, "color:red") || strings.Contains(got, "今日决策器") {
		t.Fatalf("visibleText 未剔除 head/脚本/样式: %q", got)
	}
}

func TestAnalyzeDOMDetectsBlankPage(t *testing.T) {
	// 只有 <title> 与空 root，属于典型的白屏。
	dom := `<html><head><title>午餐吃什么 · 今日决策器</title></head><body><div id="root"></div><script src="/src/main.tsx"></script></body></html>`
	res := analyzeDOM(dom, "", "午餐吃什么 · 今天中午吃面条")
	if res.OK {
		t.Fatal("空 root 应判定为渲染失败")
	}
}

func TestAnalyzeDOMDetectsConsoleError(t *testing.T) {
	dom := `<html><head><title>站点</title></head><body><div id="root"><header>午餐吃什么</header></div></body></html>`
	console := `Uncaught Error: You cannot render a <Router> inside another <Router>.`
	res := analyzeDOM(dom, console, "午餐吃什么")
	if res.OK || !strings.Contains(res.Detail, "Router") {
		t.Fatalf("应识别运行时异常，实际: %+v", res)
	}
}

func TestAnalyzeDOMDetectsViteOverlay(t *testing.T) {
	res := analyzeDOM(`<html><body><div id="root"></div><vite-error-overlay><p>Failed to resolve import "@/pages/Home"</p></vite-error-overlay></body></html>`, "", "")
	if res.OK || !strings.Contains(res.Detail, "Vite") {
		t.Fatalf("应识别 Vite 错误覆盖层，实际: %+v", res)
	}
}

func TestAnalyzeDOMPassesRealContent(t *testing.T) {
	dom := `<html><body><div id="root"><header>夏日衣橱 SUMMER</header><main><h1>夏日穿搭指南</h1><p>根据聊天内容整理的通勤与旅行穿搭方案，包含场景、清单与出门前检查。</p></main></div></body></html>`
	res := analyzeDOM(dom, "", "夏日衣橱")
	if !res.OK {
		t.Fatalf("正常内容应通过，实际: %+v", res)
	}
}

func TestAnalyzeDOMTitleMismatchStillPasses(t *testing.T) {
	// 页面标题与规划名不完全一致，但正文充足时不应误判为白屏。
	dom := `<html><head><title>午餐吃什么 · 今日决策器</title></head><body><div id="root"><header>午餐吃什么</header><main><h1>今天中午吃什么</h1><p>三个方案：面条、盖饭、轻食，并附带选择理由与营养搭配建议。</p></main></div></body></html>`
	res := analyzeDOM(dom, "", "午餐吃什么 · 今天中午吃面条")
	if !res.OK {
		t.Fatalf("正文充足时不应因标题不同而失败，实际: %+v", res)
	}
}
