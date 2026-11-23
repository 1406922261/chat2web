package perf

import (
	"testing"
	"time"
)

func TestReportTokenAccounting(t *testing.T) {
	c := NewCollector("早餐推荐网页", "test-model")
	c.Add(RoundMetric{Round: 1, Stage: "generate", InputTokens: 1000, OutputTokens: 200, CachedTokens: 600, DurationMs: 10, Success: true})
	c.Add(RoundMetric{Round: 2, Stage: "generate", InputTokens: 500, OutputTokens: 100, CachedTokens: 0, DurationMs: 20, Success: true})
	c.AddStage("analysis", 1500*time.Millisecond)
	c.AddStage("analysis", 500*time.Millisecond)
	c.AddTool()
	c.AddToolN(3)
	c.AddRetry()

	r := c.Finish()
	if r.LLMCalls != 2 {
		t.Fatalf("LLMCalls = %d, want 2", r.LLMCalls)
	}
	if r.TotalInput != 1500 || r.TotalOutput != 300 || r.TotalCached != 600 {
		t.Fatalf("token totals = %d/%d/%d", r.TotalInput, r.TotalOutput, r.TotalCached)
	}
	if r.UncachedInput() != 900 {
		t.Fatalf("UncachedInput = %d, want 900", r.UncachedInput())
	}
	if got := r.CacheHitRate(); got < 0.39 || got > 0.41 {
		t.Fatalf("CacheHitRate = %v, want ~0.4", got)
	}
	if r.StageMs["analysis"] != 2000 {
		t.Fatalf("stage analysis = %d, want 2000", r.StageMs["analysis"])
	}
	if r.ToolCalls != 4 {
		t.Fatalf("ToolCalls = %d, want 4", r.ToolCalls)
	}
	if r.Retries != 1 {
		t.Fatalf("Retries = %d, want 1", r.Retries)
	}
	if r.Text() == "" {
		t.Fatal("Text() 不应为空")
	}
}

func TestCacheHitRateEmpty(t *testing.T) {
	r := &Report{}
	if r.CacheHitRate() != 0 || r.UncachedInput() != 0 {
		t.Fatal("空报告应返回 0")
	}
}

func TestWriteReport(t *testing.T) {
	c := NewCollector("demo", "m")
	c.Add(RoundMetric{Round: 1, Stage: "generate", InputTokens: 10, OutputTokens: 5, Success: true})
	r := c.Finish()
	path, err := r.Write(t.TempDir(), "project-1")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if path == "" {
		t.Fatal("报告路径为空")
	}
}
