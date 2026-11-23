package generator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"chat2web/internal/analyzer"
	"chat2web/internal/planner"
)

// Brief 生成注入 LLM 的内容简报：聊天事实 + 网页规划，明确禁止编造。
func Brief(a *analyzer.Analysis, p *planner.SitePlan) string {
	var b strings.Builder
	b.WriteString("## 聊天分析结果（网页内容的事实来源）\n")
	b.WriteString(a.JSON())
	b.WriteString("\n\n## 网页规划（页面结构以此为准，可按实际内容微调）\n")
	b.WriteString(p.JSON())
	b.WriteString("\n\n## 内容红线\n")
	b.WriteString("- 网页中的事实、数字、地点、人物、书名、菜品等只能来自上面的分析结果。\n")
	b.WriteString("- 信息不足时，在页面中明确写出“待补充：…”，禁止编造细节。\n")
	b.WriteString("- 不要输出与聊天主题无关的通用占位文案。\n")
	return b.String()
}

// RequiredFiles 是生成完成必须存在的核心文件。
var RequiredFiles = []string{"package.json", "index.html", "src/main.tsx", "src/App.tsx"}

// Validate 校验项目是否已生成可运行的网页骨架。
func Validate(dir string) error {
	var missing []string
	for _, rel := range RequiredFiles {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Stat(p)
		if err != nil || info.IsDir() || info.Size() == 0 {
			missing = append(missing, rel)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少关键文件: %s", strings.Join(missing, ", "))
	}
	return nil
}

type siteData struct {
	SiteName     string             `json:"siteName"`
	WebsiteType  string             `json:"websiteType"`
	Summary      string             `json:"summary"`
	Style        string             `json:"style"`
	Theme        string             `json:"theme"`
	Palette      []string           `json:"palette"`
	Pages        []planner.PagePlan `json:"pages"`
	Interactions []string           `json:"interactions"`
	GeneratedAt  string             `json:"generatedAt"`
	Source       string             `json:"source"`
}

func toSiteData(a *analyzer.Analysis, p *planner.SitePlan) siteData {
	return siteData{
		SiteName:     p.SiteName,
		WebsiteType:  p.WebsiteType,
		Summary:      p.Summary,
		Style:        p.Style,
		Theme:        p.Theme,
		Palette:      p.Palette,
		Pages:        p.Pages,
		Interactions: p.Interactions,
		GeneratedAt:  nowString(),
		Source:       "chat transcript: " + a.Topic,
	}
}

func marshalJSON(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}
