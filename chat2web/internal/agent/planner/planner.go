package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"chat2web/internal/agent/analyzer"
	"chat2web/internal/llm"
	"chat2web/internal/project"
)

type SectionPlan struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Items       []string `json:"items"`
}

type PagePlan struct {
	Name     string        `json:"name"`
	Path     string        `json:"path"`
	Purpose  string        `json:"purpose"`
	Sections []SectionPlan `json:"sections"`
}

// SitePlan 是网页规划引擎的输出：网站类型、页面结构、视觉风格与交互由内容决定。
type SitePlan struct {
	SiteName     string     `json:"site_name"`
	WebsiteType  string     `json:"website_type"`
	Summary      string     `json:"summary"`
	Style        string     `json:"style"`
	Theme        string     `json:"theme"`
	Palette      []string   `json:"palette"`
	Pages        []PagePlan `json:"pages"`
	Interactions []string   `json:"interactions"`
	TechStack    []string   `json:"tech_stack"`
	NeedsBackend bool       `json:"needs_backend"`
	Source       string     `json:"source"`
}

type Planner struct {
	LLM *llm.Client
}

func New(client *llm.Client) *Planner { return &Planner{LLM: client} }

const systemPrompt = `你是 Chat2Web AI 的网页规划引擎。你会收到聊天分析结果（含主题、事实、偏好、缺失信息）与用户长期偏好。
请自主决定要生成什么网站，输出严格 JSON：
{
  "site_name": "网站名称",
  "website_type": "网站类型（沿用或细化分析中的 website_type）",
  "summary": "一句话网站定位",
  "style": "整体视觉风格：配色、排版、氛围、图像语言",
  "theme": "light 或 dark",
  "palette": ["#主色", "#辅助色", "#强调色", "#背景色"],
  "pages": [
    {
      "name": "页面名称",
      "path": "/",
      "purpose": "该页面要解决的问题",
      "sections": [
        {"title": "模块标题", "description": "模块说明", "items": ["来自聊天事实/实体的具体内容"]}
      ]
    }
  ],
  "interactions": ["可交互功能，如筛选、标签切换、进度条、折叠面板"],
  "tech_stack": ["React", "TypeScript", "Vite", "Tailwind CSS"],
  "needs_backend": false
}
要求：
1. 页面数量 2-6 个，页面结构与模块顺序必须由内容决定；不同主题必须使用不同结构，禁止套用同一模板。
2. items 只能来自分析结果中的事实、实体、偏好；缺失内容写“待补充：具体缺什么”，严禁编造。
3. 模块标题与 items 必须围绕聊天中的具体对象命名：用户说“想吃涮羊肉”，首页就应有“涮羊肉部位 / 锅底 / 小料”这类模块，不能退化成“美食类型 / 餐厅推荐”这类上位类别；聊天明确出现的具体对象视为已确认事实，不得再写成“待补充”。
4. 首页必须能一眼看懂具体主题；其余页面承载不同信息维度（如安排、清单、进度、预算、总结）。
5. 视觉风格要与主题匹配（旅行清新、健身力量、读书温润、项目科技等），palette 使用 4 个十六进制色值。
6. tech_stack 默认为 React + TypeScript + Vite + Tailwind CSS，除非聊天明确指定其他技术。`

// Plan 优先用 LLM 决策；失败或未配置时使用按类别动态组织的本地规划，并返回提示信息。
func (p *Planner) Plan(ctx context.Context, a *analyzer.Analysis, memoryBlock string, proj *project.Project, modify bool) (*SitePlan, string, *llm.Usage) {
	if a == nil {
		return nil, "缺少聊天分析结果，无法规划网页", nil
	}
	if p.LLM != nil && p.LLM.Configured() {
		var out SitePlan
		user := "聊天分析：\n" + a.JSON()
		if memoryBlock != "" {
			user += "\n\n用户长期偏好（只参考相关部分，不要混入无关历史）：\n" + memoryBlock
		}
		if proj != nil {
			user += "\n\n当前项目：" + proj.Name + "（状态 " + proj.Status + "）"
			if len(proj.Pages) > 0 {
				user += "，已有页面：" + strings.Join(proj.Pages, "、")
			}
		}
		if modify {
			user += "\n\n这是对已有网站的增量修改：保持原有页面主体，只调整与本次聊天要求相关的页面与模块。"
		}
		usage, err := p.LLM.CompleteJSONUsage(ctx, systemPrompt, user, &out)
		if err != nil {
			fallback := Heuristic(a)
			return fallback, "LLM 网页规划失败，已回退到本地规则规划：" + err.Error(), usage
		}
		out.Source = "llm"
		if err := validate(&out, a); err != nil {
			fallback := Heuristic(a)
			return fallback, "LLM 规划结果不完整（" + err.Error() + "），已回退到本地规则规划", usage
		}
		return &out, "", usage
	}
	fallback := Heuristic(a)
	return fallback, "LLM 未配置，当前使用本地规则规划网页结构", nil
}

func validate(p *SitePlan, a *analyzer.Analysis) error {
	if strings.TrimSpace(p.SiteName) == "" {
		p.SiteName = a.Topic
	}
	if len(p.Pages) == 0 {
		return fmt.Errorf("没有页面")
	}
	seen := map[string]bool{}
	for i := range p.Pages {
		page := &p.Pages[i]
		if strings.TrimSpace(page.Name) == "" {
			page.Name = fmt.Sprintf("页面 %d", i+1)
		}
		if strings.TrimSpace(page.Path) == "" {
			if i == 0 {
				page.Path = "/"
			} else {
				page.Path = fmt.Sprintf("/page-%d", i+1)
			}
		}
		if seen[page.Path] {
			page.Path = fmt.Sprintf("%s-%d", strings.TrimRight(page.Path, "/"), i+1)
		}
		seen[page.Path] = true
		if len(page.Sections) == 0 {
			return fmt.Errorf("页面 %s 没有内容模块", page.Name)
		}
		for j := range page.Sections {
			if len(page.Sections[j].Items) == 0 {
				page.Sections[j].Items = []string{"待补充：" + page.Sections[j].Title}
			}
		}
	}
	if len(p.Palette) == 0 {
		p.Palette = []string{"#7c3aed", "#a78bfa", "#22d3ee", "#0f172a"}
	}
	if p.Theme == "" {
		p.Theme = "dark"
	}
	if len(p.TechStack) == 0 {
		p.TechStack = []string{"React", "TypeScript", "Vite", "Tailwind CSS"}
	}
	return nil
}

func (p *SitePlan) PageNames() []string {
	out := make([]string, 0, len(p.Pages))
	for _, page := range p.Pages {
		out = append(out, page.Name)
	}
	return out
}

// JSON 返回适合注入提示词的精简 JSON。
func (p *SitePlan) JSON() string {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"site_name":%q}`, p.SiteName)
	}
	return string(data)
}

// ToText 生成终端可读的规划摘要。
func (p *SitePlan) ToText() string {
	var b strings.Builder
	b.WriteString("网站名称：")
	b.WriteString(p.SiteName)
	b.WriteString("\n")
	b.WriteString("网站类型：")
	b.WriteString(p.WebsiteType)
	b.WriteString("\n")
	b.WriteString("定位：")
	b.WriteString(p.Summary)
	b.WriteString("\n")
	fmt.Fprintf(&b, "视觉风格：%s（%s）\n", p.Style, p.Theme)
	if len(p.TechStack) > 0 {
		fmt.Fprintf(&b, "技术栈：%s\n", strings.Join(p.TechStack, " / "))
	}
	b.WriteString("页面结构：\n")
	for _, page := range p.Pages {
		fmt.Fprintf(&b, "  - %s（%s）", page.Name, page.Path)
		if page.Purpose != "" {
			fmt.Fprintf(&b, "：%s", page.Purpose)
		}
		b.WriteString("\n")
		for _, sec := range page.Sections {
			fmt.Fprintf(&b, "      · %s\n", sec.Title)
		}
	}
	if len(p.Interactions) > 0 {
		fmt.Fprintf(&b, "交互：%s\n", strings.Join(p.Interactions, "、"))
	}
	return b.String()
}
