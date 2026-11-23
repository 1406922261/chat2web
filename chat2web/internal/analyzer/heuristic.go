package analyzer

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"chat2web/internal/textutil"
)

type categoryRule struct {
	Category    string
	WebsiteType string
	Tone        string
	Keywords    []string
}

// 类别规则只用于“未配置 LLM”时的本地兜底，不限制正常情况下的动态分析。
var categoryRules = []categoryRule{
	{"旅游攻略", "travel_guide", "清新旅行", []string{"旅游", "旅行", "出游", "攻略", "景点", "行程", "自驾", "古镇", "西湖", "度假"}},
	{"健身计划", "fitness_plan", "活力运动", []string{"健身", "训练", "增肌", "减脂", "锻炼", "力量", "有氧", "卧推", "深蹲", "拉伸"}},
	{"学习计划", "study_plan", "专注学习", []string{"学习", "复习", "考试", "考研", "课程", "知识点", "刷题", "英语", "编程", "路线"}},
	{"读书笔记", "reading_notes", "书卷质感", []string{"读书", "阅读", "书单", "笔记", "读后感", "作者", "章节", "书"}},
	{"项目介绍", "project_showcase", "科技专业", []string{"项目", "开发", "功能模块", "需求", "上线", "架构", "迭代", "接口", "版本规划"}},
	{"活动策划", "event_plan", "热闹创意", []string{"活动", "策划", "年会", "婚礼", "聚会", "展览", "发布会", "流程", "物料"}},
	{"产品介绍", "product_intro", "现代商务", []string{"产品", "卖点", "目标用户", "发布", "客户", "竞品", "定价"}},
	{"个人成长", "growth_journal", "温暖叙事", []string{"成长", "复盘", "习惯", "自律", "反思", "日记", "里程碑"}},
	{"会议整理", "meeting_notes", "简洁高效", []string{"会议", "纪要", "讨论", "评审", "决议", "待办", "议程", "参会", "议题", "负责", "整理"}},
	{"美食决策", "food_decision", "轻松活泼", []string{"吃什么", "吃啥", "午饭", "午餐", "早饭", "早餐", "晚饭", "晚餐", "夜宵", "外卖", "点餐", "食堂", "下馆子"}},
	{"美食推荐", "food_guide", "温暖美食", []string{"美食", "菜谱", "餐厅", "小吃", "做饭", "食材", "探店", "口味"}},
}

var (
	prefixRe    = regexp.MustCompile(`^(用户|我|user|客户|AI|助手|客服|assistant)\s*[:：]\s*`)
	durationRe  = regexp.MustCompile(`(\d+|[一二三四五六七八九十两])\s*(天|日|周|个月|月|年)`)
	destRe      = regexp.MustCompile(`(?:去|到|在|想去|打算去)([\p{Han}]{2,8}?)(?:旅游|旅行|玩|游玩|出差|度假)`)
	bookRe      = regexp.MustCompile(`《([^》]{1,30})》`)
	quoteRe     = regexp.MustCompile(`[「“]([^」”]{1,30})[」”]`)
	sentenceSep = regexp.MustCompile(`[。！？!?\n；;]+`)
	listSplitRe = regexp.MustCompile(`[、,，/]`)
)

// Heuristic 是离线状态下的真实本地分析（不伪造 LLM 结果），用于保证链路可运行。
func Heuristic(chat string) *Analysis {
	chat = strings.ReplaceAll(chat, "\r\n", "\n")
	lines := collectUtterances(chat)
	all := strings.Join(lines, "。")
	lower := strings.ToLower(all)

	rule := pickCategory(lower)
	if rule == nil {
		rule = &categoryRule{Category: "综合主题", WebsiteType: "topic_summary", Tone: "简洁现代"}
	}

	duration := firstMatch(durationRe, all)
	dest := ""
	if m := destRe.FindStringSubmatch(all); len(m) > 1 {
		dest = m[1]
	}

	topic := buildTopic(lines, dest, duration, rule.Category)

	a := &Analysis{
		Topic:       topic,
		Category:    rule.Category,
		WebsiteType: rule.WebsiteType,
		Tone:        rule.Tone,
		Source:      "heuristic",
		Transcript:  chat,
	}

	a.KeyFacts = extractFacts(all)
	if duration != "" {
		a.KeyFacts = append(a.KeyFacts, Fact{Kind: "time", Key: "周期", Value: duration})
	}
	if dest != "" {
		a.KeyFacts = append(a.KeyFacts, Fact{Kind: "place", Key: "目的地", Value: dest})
	}
	a.Preferences = extractPreferences(lines)
	a.Entities = extractEntities(all, dest)
	if duration != "" {
		a.Entities = appendUnique(a.Entities, duration)
	}

	a.Sections = buildSections(a)
	a.Missing = detectMissing(a, chat)
	a.Summary = buildSummary(a, lines)

	if len(a.Sections) == 0 {
		a.Sections = []Section{{Title: "聊天要点", Points: firstN(lines, 5)}}
	}
	normalize(a)
	return a
}

func pickCategory(lower string) *categoryRule {
	best := -1
	var found *categoryRule
	for i := range categoryRules {
		r := &categoryRules[i]
		score := 0
		for _, kw := range r.Keywords {
			score += strings.Count(lower, kw)
		}
		if score > best {
			best = score
			found = r
		}
	}
	if best <= 0 {
		return nil
	}
	return found
}

func collectUtterances(chat string) []string {
	raw := strings.Split(chat, "\n")
	var user, all []string
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" || line == "END" {
			continue
		}
		isUser := strings.HasPrefix(strings.ToLower(line), "用户") || strings.HasPrefix(line, "我：") ||
			strings.HasPrefix(line, "我:") || strings.HasPrefix(strings.ToLower(line), "user")
		clean := strings.TrimSpace(prefixRe.ReplaceAllString(line, ""))
		if clean == "" {
			continue
		}
		all = append(all, clean)
		if isUser {
			user = append(user, clean)
		}
	}
	if len(user) > 0 {
		return user
	}
	return all
}

func buildTopic(lines []string, dest, duration, category string) string {
	topic := ""
	if len(lines) > 0 {
		base := lines[0]
		for _, p := range []string{"帮我", "请", "我想", "我要", "想", "麻烦", "制作一个", "制作", "做一个", "做", "生成一个", "生成", "整理一下", "整理", "一个", "一份", "关于"} {
			base = strings.TrimPrefix(base, p)
		}
		base = strings.TrimSpace(base)
		topic = textutil.Truncate(base, 20)
	}
	if dest != "" && (category == "旅游攻略" || category == "美食推荐") {
		topic = dest
		if duration != "" {
			topic += duration
		}
		if category == "旅游攻略" {
			topic += "游"
		}
	} else if duration != "" && topic != "" && !strings.Contains(topic, duration) {
		topic = duration + topic
	}
	if strings.TrimSpace(topic) == "" {
		topic = category
	}
	return strings.Trim(topic, "。！？!?，, \t")
}

func extractFacts(text string) []Fact {
	sentences := sentenceSep.Split(text, -1)
	var facts []Fact
	counters := map[string]int{}
	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if utf8.RuneCountInString(s) < 4 {
			continue
		}
		kind, key := classifyFact(s)
		counters[key]++
		facts = append(facts, Fact{
			Kind:  kind,
			Key:   key + itoa(counters[key]),
			Value: textutil.Truncate(s, 120),
		})
		if len(facts) >= 24 {
			break
		}
	}
	return facts
}

func classifyFact(s string) (kind, key string) {
	switch {
	case containsAny(s, "喜欢", "偏好", "希望", "注重", "爱吃", "优先", "风格", "重点", "主要"):
		return "preference", "偏好"
	case containsAny(s, "预算", "费用", "元", "块", "以内", "限制", "不要", "避免", "不能", "忌口"):
		return "constraint", "限制"
	case containsAny(s, "每周", "每天", "每月", "小时", "分钟", "次"):
		return "number", "时间/数量"
	case containsAny(s, "天", "周", "个月", "月", "年"):
		return "time", "时间"
	case containsAny(s, "计划", "目标", "打算", "想要", "需要", "希望"):
		return "goal", "目标"
	case containsAny(s, "决定", "确定", "选择", "结论"):
		return "decision", "结论"
	case containsAny(s, "去", "到", "在", "景点", "地点", "地方"):
		return "place", "地点"
	default:
		return "item", "内容"
	}
}

func extractPreferences(lines []string) []string {
	var out []string
	for _, line := range lines {
		for _, part := range sentenceSep.Split(line, -1) {
			part = strings.TrimSpace(part)
			if utf8.RuneCountInString(part) < 4 {
				continue
			}
			if containsAny(part, "喜欢", "偏好", "希望", "注重", "爱吃", "优先", "风格", "重点", "主要", "想要") {
				out = appendUnique(out, textutil.Truncate(part, 60))
			}
		}
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func extractEntities(text, dest string) []string {
	var out []string
	if dest != "" {
		out = appendUnique(out, dest)
	}
	for _, re := range []*regexp.Regexp{bookRe, quoteRe} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			if len(m) > 1 {
				out = appendUnique(out, strings.TrimSpace(m[1]))
			}
		}
	}
	for _, line := range sentenceSep.Split(text, -1) {
		if !containsAny(line, "推荐", "包括", "例如", "比如", "以及", "还有") {
			continue
		}
		for _, token := range listSplitRe.Split(line, -1) {
			token = strings.TrimSpace(token)
			if n := utf8.RuneCountInString(token); n >= 2 && n <= 10 {
				out = appendUnique(out, token)
			}
		}
	}
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

func buildSections(a *Analysis) []Section {
	groups := []struct {
		title string
		kinds []string
	}{
		{"目标与需求", []string{"goal", "decision"}},
		{"时间与数量", []string{"time", "number"}},
		{"偏好与限制", []string{"preference", "constraint"}},
		{"关键内容", []string{"item", "place", "person"}},
	}
	var out []Section
	for _, g := range groups {
		pts := a.FactsOf(g.kinds...)
		if len(pts) == 0 {
			continue
		}
		out = append(out, Section{Title: g.title, Points: dedupe(pts, 8)})
	}
	if len(a.Entities) > 0 {
		out = append(out, Section{Title: "关键实体", Points: append([]string{}, a.Entities...)})
	}
	if len(a.Missing) > 0 {
		out = append(out, Section{Title: "待补充信息", Points: append([]string{}, a.Missing...)})
	}
	return out
}

func detectMissing(a *Analysis, chat string) []string {
	var out []string
	if len(a.FactsOf("goal", "decision")) == 0 {
		out = append(out, "聊天未明确网页的核心目标")
	}
	if len(a.Preferences) == 0 {
		out = append(out, "聊天未说明视觉风格或内容偏好")
	}
	if len(a.FactsOf("time", "number")) == 0 {
		out = append(out, "聊天未提供时间、周期或数量等量化信息")
	}
	if len(a.Entities) == 0 {
		out = append(out, "聊天中缺少可展示的具体条目（地点/名称/动作等）")
	}
	if utf8.RuneCountInString(chat) < 60 {
		out = append(out, "聊天信息较少，网页中的细节需要标注为待补充")
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func buildSummary(a *Analysis, lines []string) string {
	parts := firstN(lines, 2)
	desc := strings.Join(parts, "；")
	if utf8.RuneCountInString(desc) > 90 {
		desc = string([]rune(desc)[:90]) + "…"
	}
	if desc == "" {
		return "聊天内容围绕" + a.Topic + "展开，属于" + a.Category + "。"
	}
	return "聊天围绕" + a.Topic + "展开（" + a.Category + "）：" + desc
}

func firstMatch(re *regexp.Regexp, s string) string {
	m := re.FindString(s)
	return strings.TrimSpace(m)
}

func firstN(lines []string, n int) []string {
	if len(lines) > n {
		lines = lines[:n]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, textutil.Truncate(l, 120))
		}
	}
	return out
}

func containsAny(s string, subs ...string) bool {
	s = strings.ToLower(s)
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func appendUnique(list []string, v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return list
	}
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

func dedupe(items []string, max int) []string {
	var out []string
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			out = appendUnique(out, item)
		}
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func itoa(n int) string {
	if n <= 0 {
		return ""
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + itoa(n%10)
}
