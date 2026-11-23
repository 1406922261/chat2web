package planner

import (
	"strings"

	"chat2web/internal/analyzer"
	"chat2web/internal/textutil"
)

// Heuristic 在离线状态下按内容类别动态组织页面，不使用统一的固定模板。
func Heuristic(a *analyzer.Analysis) *SitePlan {
	p := &SitePlan{
		SiteName:    siteName(a),
		WebsiteType: a.WebsiteType,
		Summary:     textutil.Truncate(a.Summary, 160),
		TechStack:   []string{"React", "TypeScript", "Vite", "Tailwind CSS"},
		Source:      "heuristic",
	}
	p.Style, p.Theme, p.Palette, p.Interactions = visuals(a.Category)
	p.Pages = pagesFor(a)
	_ = validate(p, a)
	return p
}

func siteName(a *analyzer.Analysis) string {
	name := strings.TrimSpace(a.Topic)
	if name == "" {
		name = a.Category
	}
	if a.Category != "" && !strings.Contains(name, a.Category) && a.Category != "综合主题" {
		name += " · " + a.Category
	}
	return name
}

func visuals(category string) (style, theme string, palette, interactions []string) {
	switch category {
	case "旅游攻略":
		return "清新通透，湖蓝与竹绿点缀，大量留白与大幅目的地视觉", "light",
			[]string{"#0f766e", "#14b8a6", "#f59e0b", "#f8fafc"},
			[]string{"行程标签切换", "景点/美食筛选", "预算折叠面板"}
	case "健身计划":
		return "力量感强，深色底配高对比荧光色，数据卡片与进度条突出训练节奏", "dark",
			[]string{"#0b0f19", "#22d3ee", "#f97316", "#e2e8f0"},
			[]string{"训练日切换", "训练打卡", "进度条"}
	case "学习计划":
		return "专注克制，蓝白配色，阶段路线与清单式排版强调可执行性", "light",
			[]string{"#1d4ed8", "#60a5fa", "#f59e0b", "#f8fafc"},
			[]string{"阶段切换", "任务勾选", "进度统计"}
	case "读书笔记":
		return "温润书卷感，米色纸张底与棕金点缀，适合长文阅读与摘录", "light",
			[]string{"#7c2d12", "#b45309", "#f5f5f4", "#1c1917"},
			[]string{"章节导航", "摘录折叠", "笔记筛选"}
	case "项目介绍":
		return "科技专业，深色背景与冷色高亮，模块化信息卡片与时间线", "dark",
			[]string{"#0f172a", "#38bdf8", "#a78bfa", "#e2e8f0"},
			[]string{"模块筛选", "计划时间线", "状态标签"}
	case "活动策划":
		return "热闹有仪式感，暖红与明黄配色，流程时间线与物料清单并重", "light",
			[]string{"#be123c", "#fb7185", "#facc15", "#fff7ed"},
			[]string{"流程时间线", "分工筛选", "物料清单勾选"}
	case "产品介绍":
		return "现代商务，深灰蓝配紫青渐变，卖点模块与用户画像并列", "dark",
			[]string{"#111827", "#6366f1", "#22d3ee", "#f9fafb"},
			[]string{"卖点切换", "用户场景筛选", "版本对比"}
	case "个人成长":
		return "温暖叙事，墨绿与暖黄，习惯打卡与里程碑时间线", "light",
			[]string{"#065f46", "#34d399", "#fbbf24", "#f0fdf4"},
			[]string{"习惯打卡", "周复盘切换", "里程碑进度"}
	case "会议整理":
		return "简洁高效，石板灰配蓝色强调，决议与待办分层清晰", "light",
			[]string{"#1e293b", "#3b82f6", "#94a3b8", "#f8fafc"},
			[]string{"议题筛选", "待办勾选", "结论折叠"}
	case "美食推荐":
		return "温暖食欲感，砖红与橙黄，图片化卡片与口味标签", "light",
			[]string{"#b91c1c", "#f97316", "#fde68a", "#fffbeb"},
			[]string{"口味筛选", "推荐清单收藏", "做法步骤折叠"}
	case "美食决策":
		return "轻快明快，暖橙与奶油白，大按钮与卡片式候选，突出随机决策的趣味感", "light",
			[]string{"#ea580c", "#fb923c", "#fde68a", "#fffbeb"},
			[]string{"随机抽取", "候选筛选", "多选投票", "结果重抽"}
	default:
		return "简洁现代，紫青点缀，信息层级清晰、留白充足", "light",
			[]string{"#7c3aed", "#a78bfa", "#22d3ee", "#f8fafc"},
			[]string{"内容筛选", "模块折叠"}
	}
}

func pagesFor(a *analyzer.Analysis) []PagePlan {
	switch a.Category {
	case "旅游攻略":
		return []PagePlan{
			mkPage("首页", "/", "用最短时间了解这趟旅程",
				mkSec("旅程主题", "来自聊天内容的主题与摘要", []string{a.Topic, a.Summary}),
				mkSec("出行信息", "聊天中提到的时间与数量信息", facts(a, "待补充：出行时间与同行人数", "time", "number")),
				mkSec("亮点速览", "聊天中出现的关键地点与体验", entityItems(a))),
			mkPage("行程安排", "/itinerary", "按天组织聊天中提到的安排",
				mkSec("行程要点", "聊天中提到的目标与具体内容", facts(a, "待补充：每天的具体行程", "goal", "time", "item")),
				mkSec("节奏建议", "基于已知信息的组织方式", []string{"按日期拆分行程，保留机动时间", "待补充：交通方式与每日出发时间"})),
			mkPage("景点推荐", "/spots", "展示聊天中提到的目的地与体验",
				mkSec("推荐地点", "聊天中出现的地点与活动", entityItems(a)),
				mkSec("游览提示", "需要补充的实用信息", missingItems(a))),
			mkPage("美食推荐", "/food", "整理聊天中表达的美食偏好",
				mkSec("美食清单", "聊天中明确提到的偏好与内容", prefOrFacts(a, "待补充：具体美食与餐厅")),
				mkSec("口味偏好", "用户明确表达的饮食偏好", prefItems(a))),
			mkPage("预算与贴士", "/budget", "预算、限制与注意事项",
				mkSec("预算与限制", "聊天中提到的费用范围与限制", facts(a, "待补充：预算范围与消费偏好", "constraint")),
				mkSec("待补充信息", "网页中需要标注的内容", missingItems(a))),
		}
	case "健身计划":
		return []PagePlan{
			mkPage("首页", "/", "快速了解训练目标与整体节奏",
				mkSec("计划主题", "来自聊天内容的训练主题", []string{a.Topic, a.Summary}),
				mkSec("核心目标", "聊天中明确表达的健身目标", preferenceOrGoal(a))),
			mkPage("训练目标", "/goals", "明确要达成的身体与力量目标",
				mkSec("目标拆解", "聊天中提到的目标与重点部位", facts(a, "待补充：具体训练目标", "goal", "preference")),
				mkSec("限制条件", "时间、场地或身体限制", facts(a, "待补充：训练限制", "constraint"))),
			mkPage("每周安排", "/schedule", "把训练频次落到每周",
				mkSec("训练频次", "聊天中提到的时间与次数", facts(a, "待补充：每周训练天数与时长", "time", "number")),
				mkSec("安排建议", "按已知频次组织的训练节奏", []string{"每次训练聚焦 1-2 个目标部位", "待补充：具体训练日与休息日"})),
			mkPage("动作库", "/exercises", "展示聊天中提到的训练动作",
				mkSec("动作清单", "聊天中出现的动作与部位", entityItems(a)),
				mkSec("动作说明", "需要补充的动作细节", missingItems(a))),
			mkPage("进度记录", "/progress", "记录训练与身体变化",
				mkSec("记录项", "建议记录的维度", []string{"训练日期与动作", "重量、次数与组数", "身体状态与恢复情况"}),
				mkSec("待补充信息", "尚未提供的基线数据", missingItems(a))),
		}
	case "学习计划":
		return []PagePlan{
			mkPage("首页", "/", "了解学习目标与整体路线",
				mkSec("学习主题", "来自聊天内容的学习主题", []string{a.Topic, a.Summary}),
				mkSec("学习目标", "聊天中明确表达的目标", preferenceOrGoal(a))),
			mkPage("阶段路线", "/roadmap", "把目标拆成可执行的阶段",
				mkSec("阶段划分", "聊天中提到的周期与内容", facts(a, "待补充：学习阶段划分", "time", "number", "goal")),
				mkSec("阶段产出", "每个阶段应完成的内容", []string{"阶段笔记与练习", "待补充：阶段验收标准"})),
			mkPage("每日计划", "/daily", "把学习内容安排到每天",
				mkSec("每日任务", "聊天中提到的时间安排", facts(a, "待补充：每日学习时长与任务", "time", "number")),
				mkSec("学习内容", "聊天中出现的学习内容", entityItems(a))),
			mkPage("进度追踪", "/progress", "跟踪完成情况与薄弱点",
				mkSec("追踪指标", "建议记录的指标", []string{"完成任务数", "待补充：正确率与复习周期"}),
				mkSec("待补充信息", "尚未提供的信息", missingItems(a))),
		}
	case "读书笔记":
		return []PagePlan{
			mkPage("首页", "/", "呈现阅读主题与核心收获",
				mkSec("阅读主题", "来自聊天内容的阅读主题", []string{a.Topic, a.Summary}),
				mkSec("核心收获", "聊天中表达的理解与感受", preferenceOrGoal(a))),
			mkPage("读书笔记", "/notes", "整理聊天中形成的笔记",
				mkSec("笔记要点", "聊天中出现的观点与摘要", facts(a, "待补充：具体章节笔记", "item", "decision")),
				mkSec("个人思考", "聊天中表达的感受", prefItems(a))),
			mkPage("书籍与作者", "/books", "展示聊天中提到的书籍",
				mkSec("书籍清单", "聊天中出现的书名与作者", entityItems(a)),
				mkSec("待补充信息", "尚未提供的书籍信息", missingItems(a))),
			mkPage("摘录与行动", "/excerpts", "沉淀摘录与后续行动",
				mkSec("摘录", "值得保留的原句（需来自原文）", []string{"待补充：原文摘录"}),
				mkSec("行动清单", "阅读后的行动", []string{"待补充：阅读后的实践计划"})),
		}
	case "项目介绍":
		return []PagePlan{
			mkPage("首页", "/", "介绍项目定位与价值",
				mkSec("项目概览", "来自聊天内容的项目主题", []string{a.Topic, a.Summary}),
				mkSec("核心价值", "聊天中强调的目标", preferenceOrGoal(a))),
			mkPage("功能模块", "/modules", "拆分项目要提供的功能",
				mkSec("模块清单", "聊天中提到的功能与需求", facts(a, "待补充：功能模块清单", "item", "goal")),
				mkSec("模块关系", "模块之间的协作方式", []string{"待补充：模块依赖关系"})),
			mkPage("开发计划", "/roadmap", "安排开发节奏",
				mkSec("时间安排", "聊天中提到的周期与节点", facts(a, "待补充：开发周期与里程碑", "time", "number")),
				mkSec("任务拆解", "聊天中提到的具体任务", entityItems(a))),
			mkPage("风险与待办", "/risks", "记录风险与后续动作",
				mkSec("风险与限制", "聊天中提到的约束", facts(a, "待补充：项目风险", "constraint")),
				mkSec("待办事项", "需要继续确认的内容", missingItems(a))),
		}
	case "活动策划":
		return []PagePlan{
			mkPage("首页", "/", "呈现活动主题与关键信息",
				mkSec("活动主题", "来自聊天内容的活动主题", []string{a.Topic, a.Summary}),
				mkSec("活动信息", "聊天中提到的规模与时间", facts(a, "待补充：活动时间与地点", "time", "number", "place"))),
			mkPage("流程安排", "/flow", "把活动拆成时间流程",
				mkSec("流程节点", "聊天中提到的环节", facts(a, "待补充：活动流程节点", "item", "time", "decision")),
				mkSec("节奏提示", "流程组织建议", []string{"待补充：每个环节的时长与负责人"})),
			mkPage("分工与物料", "/logistics", "落实人员与物料",
				mkSec("分工", "聊天中提到的人员安排", facts(a, "待补充：人员分工", "person")),
				mkSec("物料清单", "活动所需物料", []string{"待补充：物料清单与数量"})),
			mkPage("预算与风险", "/budget", "控制预算与风险",
				mkSec("预算", "聊天中提到的费用信息", facts(a, "待补充：活动预算", "constraint")),
				mkSec("风险预案", "需要提前准备的风险应对", []string{"待补充：天气、人员与场地风险预案"})),
		}
	case "产品介绍":
		return []PagePlan{
			mkPage("首页", "/", "一句话讲清产品是什么",
				mkSec("产品定位", "来自聊天内容的产品主题", []string{a.Topic, a.Summary}),
				mkSec("核心卖点", "聊天中提到的优势", preferenceOrGoal(a))),
			mkPage("核心功能", "/features", "展示产品提供的能力",
				mkSec("功能清单", "聊天中提到的功能", facts(a, "待补充：核心功能清单", "item", "goal")),
				mkSec("使用场景", "功能对应的场景", entityItems(a))),
			mkPage("目标用户", "/users", "说明产品服务的人群",
				mkSec("用户画像", "聊天中提到的用户信息", facts(a, "待补充：目标用户画像", "person", "place")),
				mkSec("待验证信息", "需要市场验证的假设", missingItems(a))),
			mkPage("发布计划", "/launch", "安排发布节奏",
				mkSec("时间节点", "聊天中提到的时间信息", facts(a, "待补充：发布节点", "time", "number")),
				mkSec("落地动作", "发布前需要完成的工作", []string{"待补充：定价、渠道与推广计划"})),
		}
	case "个人成长":
		return []PagePlan{
			mkPage("首页", "/", "呈现成长主题与当前状态",
				mkSec("成长主题", "来自聊天内容的主题", []string{a.Topic, a.Summary}),
				mkSec("核心目标", "聊天中表达的成长目标", preferenceOrGoal(a))),
			mkPage("习惯清单", "/habits", "把目标落成每日习惯",
				mkSec("习惯项", "聊天中提到的习惯与频率", facts(a, "待补充：具体习惯与频率", "number", "time", "goal")),
				mkSec("执行提示", "习惯执行建议", []string{"待补充：触发条件与奖励机制"})),
			mkPage("每周复盘", "/review", "每周回看进展",
				mkSec("复盘维度", "建议每周回看的内容", []string{"完成了什么", "遇到的阻碍", "下周调整"}),
				mkSec("待补充信息", "尚未记录的数据", missingItems(a))),
			mkPage("里程碑", "/milestones", "展示关键节点",
				mkSec("里程碑", "聊天中提到的目标节点", facts(a, "待补充：阶段性里程碑", "time", "number")),
				mkSec("成长记录", "聊天中出现的关键事件", entityItems(a))),
		}
	case "会议整理":
		return []PagePlan{
			mkPage("首页", "/", "快速了解会议概况",
				mkSec("会议主题", "来自聊天内容的会议主题", []string{a.Topic, a.Summary}),
				mkSec("会议信息", "聊天中提到的参会与时间", facts(a, "待补充：参会人与会议时间", "person", "time"))),
			mkPage("讨论要点", "/topics", "整理讨论中的关键观点",
				mkSec("讨论内容", "聊天中出现的议题与观点", facts(a, "待补充：讨论议题", "item", "place")),
				mkSec("分歧与共识", "聊天中体现的结论", facts(a, "待补充：分歧与共识", "decision"))),
			mkPage("决议事项", "/decisions", "明确会议结论",
				mkSec("决议", "聊天中明确达成的结论", facts(a, "待补充：会议决议", "decision")),
				mkSec("待确认", "尚未确认的内容", missingItems(a))),
			mkPage("待办分工", "/actions", "把结论变成可执行任务",
				mkSec("待办事项", "聊天中提到的后续动作", facts(a, "待补充：待办与负责人", "goal", "person")),
				mkSec("时间节点", "待办的时间要求", facts(a, "待补充：完成时间", "time"))),
		}
	case "美食决策":
		return []PagePlan{
			mkPage("首页", "/", "一句话进入今天吃什么的选择",
				mkSec("决策主题", "来自聊天内容的主题与餐别", []string{a.Topic, a.Summary}),
				mkSec("开始决定", "用户可以直接操作的入口", []string{"点击随机抽取今日餐食", "待补充：候选清单与筛选条件"})),
			mkPage("候选清单", "/options", "展示聊天中提到的食物与餐厅",
				mkSec("候选内容", "聊天中出现的具体食物或餐厅", entityItems(a)),
				mkSec("偏好线索", "影响候选范围的信息", prefOrFacts(a, "待补充：口味、预算与忌口"))),
			mkPage("随机决定", "/decide", "用随机方式帮助用户做决定",
				mkSec("决策方式", "提供的选择方式", []string{"随机抽取", "多选投票", "再来一次"}),
				mkSec("结果说明", "结果如何解读", []string{"结果为随机建议，可重新抽取", "待补充：最终确认方式"})),
			mkPage("筛选条件", "/filters", "按口味、预算与距离缩小范围",
				mkSec("条件清单", "聊天中提到的限制", facts(a, "待补充：口味、预算、距离与忌口", "constraint", "place", "preference")),
				mkSec("待补充信息", "尚未提供的信息", missingItems(a))),
		}
	case "美食推荐":
		return []PagePlan{
			mkPage("首页", "/", "快速进入美食主题",
				mkSec("主题概览", "来自聊天内容的美食主题", []string{a.Topic, a.Summary}),
				mkSec("口味偏好", "聊天中表达的口味", prefItems(a))),
			mkPage("推荐清单", "/list", "展示聊天中提到的美食",
				mkSec("推荐内容", "聊天中出现的美食与地点", entityItems(a)),
				mkSec("补充信息", "需要进一步确认的内容", missingItems(a))),
			mkPage("做法与食材", "/recipe", "整理做法与所需食材",
				mkSec("食材与步骤", "聊天中提到的做法信息", facts(a, "待补充：具体食材与步骤", "item")),
				mkSec("关键提示", "制作时需要留意的事项", []string{"待补充：火候、份量与替代食材"})),
			mkPage("探店贴士", "/tips", "给出到店或选购建议",
				mkSec("实用信息", "聊天中提到的地点与限制", facts(a, "待补充：营业时间与人均消费", "place", "constraint")),
				mkSec("待补充信息", "尚未提供的细节", missingItems(a))),
		}
	default:
		return []PagePlan{
			mkPage("首页", "/", "呈现聊天主题与核心信息",
				mkSec("主题概览", "来自聊天内容的主题与摘要", []string{a.Topic, a.Summary}),
				mkSec("关键信息", "聊天中提取的要点", facts(a, "待补充：聊天的关键信息", "goal", "preference", "time", "number", "item"))),
			mkPage("核心内容", "/details", "展开聊天中的核心内容",
				mkSec("内容要点", "聊天中出现的关键内容", facts(a, "待补充：具体内容", "item", "decision")),
				mkSec("关键实体", "聊天中出现的实体", entityItems(a))),
			mkPage("行动建议", "/actions", "把聊天内容转成下一步行动",
				mkSec("建议", "基于聊天内容的行动建议", []string{"待补充：明确的下一步行动"}),
				mkSec("待补充信息", "尚未提供的信息", missingItems(a))),
		}
	}
}

func mkPage(name, path, purpose string, sections ...SectionPlan) PagePlan {
	return PagePlan{Name: name, Path: path, Purpose: purpose, Sections: sections}
}

func mkSec(title, desc string, items []string) SectionPlan {
	return SectionPlan{Title: title, Description: desc, Items: items}
}

func facts(a *analyzer.Analysis, fallback string, kinds ...string) []string {
	vals := a.FactsOf(kinds...)
	vals = dedupeStrings(vals, 6)
	if len(vals) == 0 {
		return []string{fallback}
	}
	return vals
}

func preferenceOrGoal(a *analyzer.Analysis) []string {
	vals := dedupeStrings(a.Preferences, 4)
	if len(vals) == 0 {
		vals = facts(a, "待补充：用户明确的目标", "goal", "preference", "decision")
	}
	return vals
}

func prefItems(a *analyzer.Analysis) []string {
	vals := dedupeStrings(a.Preferences, 6)
	if len(vals) == 0 {
		return []string{"待补充：聊天中未明确表达偏好"}
	}
	return vals
}

func prefOrFacts(a *analyzer.Analysis, fallback string) []string {
	vals := dedupeStrings(append(append([]string{}, a.Preferences...), a.FactsOf("preference", "item")...), 8)
	if len(vals) == 0 {
		return []string{fallback}
	}
	return vals
}

func entityItems(a *analyzer.Analysis) []string {
	if len(a.Entities) == 0 {
		return []string{"待补充：聊天中未出现可展示的具体条目"}
	}
	return dedupeStrings(a.Entities, 10)
}

func missingItems(a *analyzer.Analysis) []string {
	if len(a.Missing) == 0 {
		return []string{"当前聊天信息较完整，页面细节可在后续对话中继续补充。"}
	}
	return dedupeStrings(a.Missing, 6)
}

func dedupeStrings(items []string, max int) []string {
	var out []string
	seen := map[string]bool{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}
