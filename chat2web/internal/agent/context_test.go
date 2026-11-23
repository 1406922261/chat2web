package agent

import (
	"strings"
	"testing"

	"chat2web/internal/config"
	"chat2web/internal/llm"
)

func TestShortMemoryDedupAndLimit(t *testing.T) {
	c := &ContextManager{Cfg: &config.Config{}}
	msgs := []llm.Message{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好，需要做什么？"},
		{Role: "tool", Content: "工具结果应被忽略"},
		{Role: "user", Content: "你好"},
		{Role: "user", Content: strings.Repeat("长", 2000)},
	}
	got := c.ShortMemory(msgs, 2)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Content != "你好，需要做什么？" {
		t.Fatalf("去重后应保留 assistant 消息，got %q", got[0].Content)
	}
	if len([]rune(got[1].Content)) > 830 {
		t.Fatalf("长消息未截断：%d", len([]rune(got[1].Content)))
	}
}

func TestTaskContextBudget(t *testing.T) {
	c := &ContextManager{Cfg: &config.Config{}}
	ctx := c.TaskContext(TaskInput{
		ProjectName: "早餐推荐",
		ProjectDir:  "workspace/demo",
		PlanBrief:   strings.Repeat("规划", 4000),
		TaskState:   "已写入 src/App.tsx",
		FileTree:    "src/\n  App.tsx",
	})
	if len([]rune(ctx)) > 3500 {
		t.Fatalf("任务上下文超预算：%d", len([]rune(ctx)))
	}
	if !strings.Contains(ctx, "早餐推荐") || !strings.Contains(ctx, "src/App.tsx") {
		t.Fatal("任务上下文缺少关键信息")
	}
}

func TestBuildPromptTrimsOversizedInput(t *testing.T) {
	c := &ContextManager{Cfg: &config.Config{ContextCharBudget: 4000}}
	in := c.BuildPrompt(PromptInput{
		Brief:       strings.Repeat("分析", 2000),
		MemoryBlock: strings.Repeat("记忆", 2000),
		SkillBlock:  strings.Repeat("技能", 3000),
		FileTree:    strings.Repeat("文件", 2000),
		TaskState:   strings.Repeat("状态", 2000),
	})
	total := len([]rune(in.Brief)) + len([]rune(in.MemoryBlock)) + len([]rune(in.SkillBlock)) +
		len([]rune(in.FileTree)) + len([]rune(in.TaskState))
	if total > 4000 {
		t.Fatalf("裁剪后仍超预算：%d", total)
	}
	if len([]rune(in.FileTree)) > 2000 || len([]rune(in.MemoryBlock)) > 1200 {
		t.Fatal("单层上限未生效")
	}
}

func TestLongMemoryDedup(t *testing.T) {
	c := &ContextManager{Cfg: &config.Config{}}
	got := c.LongMemory("偏好：清淡\n偏好：清淡\n主题：早餐")
	if strings.Count(got, "偏好：清淡") != 1 {
		t.Fatalf("长期记忆未去重：%q", got)
	}
}
