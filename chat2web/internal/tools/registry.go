package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"chat2web/internal/llm"
	"chat2web/internal/textutil"
)

// Tool 是所有 Agent 工具的统一接口。
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, args map[string]any) (string, error)
}

type FuncTool struct {
	name   string
	desc   string
	params map[string]any
	fn     func(ctx context.Context, args map[string]any) (string, error)
}

func NewFuncTool(name, desc string, params map[string]any, fn func(ctx context.Context, args map[string]any) (string, error)) *FuncTool {
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return &FuncTool{name: name, desc: desc, params: params, fn: fn}
}

func (t *FuncTool) Name() string               { return t.name }
func (t *FuncTool) Description() string        { return t.desc }
func (t *FuncTool) Parameters() map[string]any { return t.params }
func (t *FuncTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	return t.fn(ctx, args)
}

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Name()]; !exists {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
	var order []string
	for _, n := range r.order {
		if n != name {
			order = append(order, n)
		}
	}
	r.order = order
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		if t, ok := r.tools[name]; ok {
			out = append(out, t)
		}
	}
	return out
}

func (r *Registry) Definitions() []llm.ToolDef {
	var defs []llm.ToolDef
	for _, t := range r.List() {
		defs = append(defs, llm.ToolDef{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Parameters(),
			},
		})
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Function.Name < defs[j].Function.Name })
	return defs
}

func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// CallLog 记录一次工具调用，便于在管理界面查看。
type CallLog struct {
	Time       time.Time `json:"time"`
	Tool       string    `json:"tool"`
	Args       string    `json:"args"`
	Output     string    `json:"output"`
	Error      string    `json:"error,omitempty"`
	DurationMs int64     `json:"duration_ms"`
}

// Dispatcher 负责解析工具参数、执行超时控制与结果记录。
type Dispatcher struct {
	registry *Registry
	mu       sync.Mutex
	logs     []CallLog
	timeout  time.Duration
}

func NewDispatcher(reg *Registry) *Dispatcher {
	return &Dispatcher{registry: reg, timeout: 6 * time.Minute}
}

func (d *Dispatcher) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	tool, ok := d.registry.Get(name)
	if !ok {
		return "", fmt.Errorf("工具不存在: %s", name)
	}
	args := map[string]any{}
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("工具参数不是合法 JSON（通常是模型输出被长度上限截断）: %w。请把文件拆小：单个文件 ≤120 行，多个小文件可在同一次响应中分别 write_file", err)
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	start := time.Now()
	out, err := tool.Execute(callCtx, args)
	entry := CallLog{
		Time:       time.Now(),
		Tool:       name,
		Args:       textutil.Truncate(argsJSON, 2000),
		Output:     textutil.Truncate(out, 4000),
		DurationMs: time.Since(start).Milliseconds(),
	}
	if err != nil {
		entry.Error = err.Error()
	}
	d.mu.Lock()
	d.logs = append(d.logs, entry)
	if len(d.logs) > 200 {
		d.logs = d.logs[len(d.logs)-200:]
	}
	d.mu.Unlock()
	return out, err
}

func (d *Dispatcher) Logs(limit int) []CallLog {
	d.mu.Lock()
	defer d.mu.Unlock()
	if limit <= 0 || limit > len(d.logs) {
		limit = len(d.logs)
	}
	out := make([]CallLog, limit)
	copy(out, d.logs[len(d.logs)-limit:])
	return out
}
