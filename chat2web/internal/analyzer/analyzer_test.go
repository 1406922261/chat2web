package analyzer

import (
	"strings"
	"testing"
)

const travelChat = `用户：我想去杭州旅游，计划玩三天。
AI：可以去西湖、灵隐寺和西溪湿地。
用户：我喜欢美食，也想体验当地特色。
AI：可以安排龙井虾仁、片儿川等杭州特色美食。`

const fitnessChat = `用户：我想制作一个健身计划。
AI：你希望训练多长时间？
用户：一个月，每周训练四次。
AI：你希望重点锻炼哪些部位？
用户：主要是胸部、背部和腿部。`

const meetingChat = `用户：帮我整理一下今天的会议。
AI：讨论了哪些议题？
用户：确定了下个版本要在周五上线，接口由小王负责。
用户：另外预算需要控制在五万以内。`

func TestHeuristicTravel(t *testing.T) {
	an := Heuristic(travelChat)
	if an.Category != "旅游攻略" {
		t.Fatalf("category = %q, want 旅游攻略", an.Category)
	}
	if an.WebsiteType != "travel_guide" {
		t.Fatalf("website type = %q", an.WebsiteType)
	}
	if !strings.Contains(an.Topic, "杭州") {
		t.Fatalf("topic should contain destination, got %q", an.Topic)
	}
	if len(an.KeyFacts) < 3 {
		t.Fatalf("key facts too few: %d", len(an.KeyFacts))
	}
	if len(an.Entities) == 0 {
		t.Fatal("entities should not be empty")
	}
	if !strings.Contains(an.Summary, "杭州") {
		t.Fatalf("summary should mention topic: %q", an.Summary)
	}
}

func TestHeuristicDomainsDiffer(t *testing.T) {
	travel := Heuristic(travelChat)
	fitness := Heuristic(fitnessChat)
	meeting := Heuristic(meetingChat)

	if fitness.Category != "健身计划" {
		t.Fatalf("fitness category = %q", fitness.Category)
	}
	if meeting.Category != "会议整理" {
		t.Fatalf("meeting category = %q", meeting.Category)
	}
	if travel.WebsiteType == fitness.WebsiteType || fitness.WebsiteType == meeting.WebsiteType {
		t.Fatal("different domains should produce different website types")
	}
	// 旅游字段不能出现在健身分析结果中
	for _, f := range fitness.KeyFacts {
		if strings.Contains(f.Key, "目的地") || strings.Contains(f.Key, "美食") {
			t.Fatalf("fitness analysis contains travel-specific field %q", f.Key)
		}
	}
}

func TestHeuristicMissingAndPreferences(t *testing.T) {
	an := Heuristic(fitnessChat)
	foundPref := false
	for _, p := range an.Preferences {
		if strings.Contains(p, "胸部") || strings.Contains(p, "背") || strings.Contains(p, "腿") {
			foundPref = true
		}
	}
	if !foundPref {
		t.Fatalf("preferences should capture training focus, got %v", an.Preferences)
	}
	short := Heuristic("用户：随便做个网页")
	if len(short.Missing) == 0 {
		t.Fatal("short chat should report missing information")
	}
}

func TestHeuristicEmptySafety(t *testing.T) {
	an := Heuristic("")
	if an == nil || an.Topic == "" {
		t.Fatal("empty chat should still produce a safe analysis")
	}
	if an.WebsiteType == "" {
		t.Fatal("website type should have a fallback")
	}
}

func TestHeuristicLunchDecision(t *testing.T) {
	an := Heuristic("给我生成今天中午吃什么的决策网页")
	if an.Category != "美食决策" {
		t.Fatalf("category = %q, want 美食决策", an.Category)
	}
	if an.WebsiteType != "food_decision" {
		t.Fatalf("website type = %q, want food_decision", an.WebsiteType)
	}
}
