package analyzer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"chat2web/internal/llm"
)

// Fact 是从聊天中提取的一条结构化事实。
type Fact struct {
	Kind  string `json:"kind"` // goal | preference | constraint | item | number | time | place | person | decision
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Section 是分析阶段对聊天信息的自然分组，供后续规划参考。
type Section struct {
	Title  string   `json:"title"`
	Points []string `json:"points"`
}

// Analysis 是聊天分析引擎的输出。字段结构由内容动态生成，不针对某一类主题写死。
type Analysis struct {
	Topic       string    `json:"topic"`
	Category    string    `json:"category"`
	Summary     string    `json:"summary"`
	KeyFacts    []Fact    `json:"key_facts"`
	Sections    []Section `json:"sections"`
	Preferences []string  `json:"preferences"`
	Entities    []string  `json:"entities"`
	WebsiteType string    `json:"website_type"`
	Tone        string    `json:"tone"`
	Missing     []string  `json:"missing"`
	Source      string    `json:"source"` // llm | heuristic
	Transcript  string    `json:"-"`
}

// Analyzer 负责真正理解多轮聊天内容并输出结构化分析。
type Analyzer struct {
	LLM *llm.Client
}

func New(client *llm.Client) *Analyzer { return &Analyzer{LLM: client} }

const systemPrompt = `你是 Chat2Web AI 的聊天内容分析引擎。输入是用户与 AI 的多轮聊天记录，可能包含“用户：”“AI：”等前缀。
请真正理解聊天内容，而不是复述原文，输出严格 JSON：
{
  "topic": "聊天主题，简短名词，如：杭州三日游 / 一个月增肌计划 / 读书会分享",
  "category": "主题类别，自由命名，如：旅游攻略/健身计划/学习计划/读书笔记/项目介绍/活动策划/产品介绍/个人成长/会议整理/美食推荐",
  "summary": "120 字以内的结构化摘要，说明聊天目标、关键内容与结论",
  "key_facts": [
    {"kind": "goal|preference|constraint|item|number|time|place|person|decision", "key": "该信息的字段名", "value": "保持聊天原意的内容"}
  ],
  "sections": [{"title": "信息分组名称", "points": ["要点 1", "要点 2"]}],
  "preferences": ["用户明确表达的偏好，如风格、口味、训练重点、时间安排"],
  "entities": ["聊天中出现的关键实体：地点、菜品、书名、人物、项目名、动作名等"],
  "website_type": "适合生成的网页类型，英文 snake_case，如 travel_guide/fitness_plan/study_roadmap/reading_notes/project_showcase/event_plan/product_launch/growth_journal/meeting_notes/food_guide/topic_summary",
  "tone": "适合网页的语气与视觉氛围，如 清新旅行、专业商务、活力运动",
  "missing": ["聊天中缺少、但网页需要的信息；这些内容必须在网页中标注为待补充"]
}
要求：
1. 不得把旅游字段写死。key_facts 的 kind/key 必须根据实际聊天内容动态决定。
2. 只提取聊天中真实出现或可直接推断的信息，严禁编造事实、数字、地点或结论。
3. 若聊天包含多个主题，选择最主要的一个，其他内容放入 key_facts/sections。
4. 内容字段使用中文；website_type 使用英文 snake_case。
5. sections 至少 2 组、key_facts 至少 3 条（信息不足时提取已知部分并在 missing 说明）。`

// Analyze 优先调用 LLM 做语义分析；失败或未配置时回退到本地启发式分析，并返回可读的提示错误。
func (a *Analyzer) Analyze(ctx context.Context, chat string) (*Analysis, string, *llm.Usage) {
	chat = strings.TrimSpace(chat)
	if chat == "" {
		return nil, "聊天内容为空，无法分析", nil
	}
	if a.LLM != nil && a.LLM.Configured() {
		var out Analysis
		user := "聊天记录：\n" + chat
		usage, err := a.LLM.CompleteJSONUsage(ctx, systemPrompt, user, &out)
		if err != nil {
			fallback := Heuristic(chat)
			return fallback, "LLM 聊天分析失败，已回退到本地规则分析：" + err.Error(), usage
		}
		out.Source = "llm"
		out.Transcript = chat
		normalize(&out)
		return &out, "", usage
	}
	fallback := Heuristic(chat)
	return fallback, "LLM 未配置（" + a.LLM.StatusMessage() + "），当前使用本地规则分析", nil
}

func normalize(a *Analysis) {
	if strings.TrimSpace(a.Topic) == "" {
		a.Topic = a.Category
	}
	if a.KeyFacts == nil {
		a.KeyFacts = []Fact{}
	}
	if a.Sections == nil {
		a.Sections = []Section{}
	}
	if a.Preferences == nil {
		a.Preferences = []string{}
	}
	if a.Entities == nil {
		a.Entities = []string{}
	}
	if a.Missing == nil {
		a.Missing = []string{}
	}
	if a.WebsiteType == "" {
		a.WebsiteType = "topic_summary"
	}
}

// JSON 返回适合注入提示词的精简 JSON。
func (a *Analysis) JSON() string {
	type view struct {
		Topic       string    `json:"topic"`
		Category    string    `json:"category"`
		Summary     string    `json:"summary"`
		KeyFacts    []Fact    `json:"key_facts"`
		Sections    []Section `json:"sections"`
		Preferences []string  `json:"preferences"`
		Entities    []string  `json:"entities"`
		WebsiteType string    `json:"website_type"`
		Tone        string    `json:"tone"`
		Missing     []string  `json:"missing"`
	}
	data, err := json.MarshalIndent(view{
		Topic: a.Topic, Category: a.Category, Summary: a.Summary, KeyFacts: a.KeyFacts,
		Sections: a.Sections, Preferences: a.Preferences, Entities: a.Entities,
		WebsiteType: a.WebsiteType, Tone: a.Tone, Missing: a.Missing,
	}, "", "  ")
	if err != nil {
		return fmt.Sprintf("{\"topic\":%q}", a.Topic)
	}
	return string(data)
}

// FactsOf 返回指定 kind 的事实值，便于规划阶段按需取用。
func (a *Analysis) FactsOf(kinds ...string) []string {
	set := map[string]bool{}
	for _, k := range kinds {
		set[k] = true
	}
	var out []string
	for _, f := range a.KeyFacts {
		if set[f.Kind] && strings.TrimSpace(f.Value) != "" {
			out = append(out, f.Value)
		}
	}
	return out
}
