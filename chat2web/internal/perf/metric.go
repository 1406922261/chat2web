// Package perf 收集单次任务的 LLM 调用、阶段耗时与 Token 统计，并输出性能报告。
package perf

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RoundMetric 是一次 LLM 调用的监控记录。
type RoundMetric struct {
	Round        int    `json:"round"`
	Stage        string `json:"stage"`
	Model        string `json:"model"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	CachedTokens int    `json:"cached_tokens"`
	DurationMs   int64  `json:"duration_ms"`
	Success      bool   `json:"success"`
}

// Report 是一次任务的完整性能报告。
type Report struct {
	Task        string           `json:"task"`
	Model       string           `json:"model"`
	Rounds      []RoundMetric    `json:"rounds"`
	StageMs     map[string]int64 `json:"stage_ms"`
	LLMCalls    int              `json:"llm_calls"`
	ToolCalls   int              `json:"tool_calls"`
	Retries     int              `json:"retries"`
	TotalMs     int64            `json:"total_ms"`
	TotalInput  int              `json:"total_input_tokens"`
	TotalOutput int              `json:"total_output_tokens"`
	TotalCached int              `json:"total_cached_tokens"`
	StartedAt   time.Time        `json:"started_at"`
	FinishedAt  time.Time        `json:"finished_at"`
}

// UncachedInput 返回实际未命中缓存的输入 Token。
func (r *Report) UncachedInput() int {
	n := r.TotalInput - r.TotalCached
	if n < 0 {
		return 0
	}
	return n
}

// CacheHitRate 返回输入 Token 的缓存命中率（0~1）；无输入时返回 0。
func (r *Report) CacheHitRate() float64 {
	if r.TotalInput <= 0 {
		return 0
	}
	if r.TotalCached > r.TotalInput {
		return 1
	}
	return float64(r.TotalCached) / float64(r.TotalInput)
}

// AvgInput 返回每轮平均输入 Token（只统计接口返回了 usage 的轮次）。
func (r *Report) AvgInput() float64 {
	n := r.tokenRounds()
	if n <= 0 {
		return 0
	}
	return float64(r.TotalInput) / float64(n)
}

// AvgOutput 返回每轮平均输出 Token（只统计接口返回了 usage 的轮次）。
func (r *Report) AvgOutput() float64 {
	n := r.tokenRounds()
	if n <= 0 {
		return 0
	}
	return float64(r.TotalOutput) / float64(n)
}

func (r *Report) tokenRounds() int {
	n := 0
	for _, m := range r.Rounds {
		if m.InputTokens > 0 || m.OutputTokens > 0 {
			n++
		}
	}
	return n
}

// Text 输出中文性能报告。
func (r *Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "任务：%s\n", orDash(r.Task))
	if r.Model != "" {
		fmt.Fprintf(&b, "模型：%s\n", r.Model)
	}
	fmt.Fprintf(&b, "LLM 调用：%d 次｜工具调用：%d 次｜自动重试：%d 次\n", r.LLMCalls, r.ToolCalls, r.Retries)
	fmt.Fprintf(&b, "输入 Token：累计 %d（未缓存 %d / 缓存 %d，命中率 %.1f%%）\n",
		r.TotalInput, r.UncachedInput(), r.TotalCached, r.CacheHitRate()*100)
	fmt.Fprintf(&b, "输出 Token：%d｜每轮平均输入 %.0f / 输出 %.0f\n", r.TotalOutput, r.AvgInput(), r.AvgOutput())
	fmt.Fprintf(&b, "总耗时：%dms\n", r.TotalMs)
	if len(r.StageMs) > 0 {
		keys := make([]string, 0, len(r.StageMs))
		for k := range r.StageMs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("阶段耗时：")
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %dms", k, r.StageMs[k]))
		}
		b.WriteString(strings.Join(parts, "｜"))
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "轮次明细：\n")
	for _, m := range r.Rounds {
		status := "成功"
		if !m.Success {
			status = "失败"
		}
		fmt.Fprintf(&b, "  #%d [%s] %s：输入 %d（缓存 %d）/ 输出 %d / %dms / %s\n",
			m.Round, orDash(m.Stage), orDash(m.Model), m.InputTokens, m.CachedTokens, m.OutputTokens, m.DurationMs, status)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Collector 在任务执行过程中累计指标。
type Collector struct {
	mu     sync.Mutex
	report Report
}

func NewCollector(task, model string) *Collector {
	return &Collector{report: Report{
		Task:      task,
		Model:     model,
		StageMs:   map[string]int64{},
		StartedAt: time.Now(),
	}}
}

// Add 记录一次 LLM 调用。
func (c *Collector) Add(m RoundMetric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.report.Rounds = append(c.report.Rounds, m)
	c.report.LLMCalls++
	c.report.TotalInput += m.InputTokens
	c.report.TotalOutput += m.OutputTokens
	c.report.TotalCached += m.CachedTokens
}

// AddStage 累加某阶段耗时。
func (c *Collector) AddStage(name string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.report.StageMs[name] += d.Milliseconds()
}

// SetStage 覆盖某阶段耗时（用于不重复累计的场景）。
func (c *Collector) SetStage(name string, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.report.StageMs[name] = d.Milliseconds()
}

func (c *Collector) AddTool() {
	c.mu.Lock()
	c.report.ToolCalls++
	c.mu.Unlock()
}

func (c *Collector) AddToolN(n int) {
	if n <= 0 {
		return
	}
	c.mu.Lock()
	c.report.ToolCalls += n
	c.mu.Unlock()
}

func (c *Collector) AddRetry() {
	c.mu.Lock()
	c.report.Retries++
	c.mu.Unlock()
}

// Finish 结束统计并返回报告快照。
func (c *Collector) Finish() *Report {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.report.FinishedAt = time.Now()
	if c.report.TotalMs <= 0 {
		c.report.TotalMs = c.report.FinishedAt.Sub(c.report.StartedAt).Milliseconds()
	}
	if c.report.StageMs == nil {
		c.report.StageMs = map[string]int64{}
	}
	cp := c.report
	cp.Rounds = append([]RoundMetric(nil), c.report.Rounds...)
	cp.StageMs = make(map[string]int64, len(c.report.StageMs))
	for k, v := range c.report.StageMs {
		cp.StageMs[k] = v
	}
	return &cp
}

// Write 把报告写入 dir/<taskID>.json，返回文件路径。
func (r *Report) Write(dir, taskID string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("报告目录为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := sanitize(taskID)
	if name == "" {
		name = fmt.Sprintf("task-%d", time.Now().Unix())
	}
	path := filepath.Join(dir, name+".json")
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
