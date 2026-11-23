package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"chat2web/internal/llm"
	"chat2web/internal/textutil"
	"chat2web/internal/tools"
)

// Executor 执行单次工具调用，记录变更文件并输出过程事件。
type Executor struct {
	Dispatcher        *tools.Dispatcher
	MaxCalls          int
	Calls             int
	Errors            int
	ConsecutiveErrors int
	LLMCalls          int
	LLMTime           time.Duration
	ToolTime          time.Duration
	PromptTokens      int
	CompletionTokens  int
	CachedTokens      int
	Retries           int
	Stage             string
	ChangedFiles      map[string]bool
	Writes            map[string]int
	Reads             map[string]int
}

func NewExecutor(dispatcher *tools.Dispatcher, maxCalls int) *Executor {
	if maxCalls <= 0 {
		maxCalls = 40
	}
	return &Executor{
		Dispatcher:   dispatcher,
		MaxCalls:     maxCalls,
		ChangedFiles: map[string]bool{},
		Writes:       map[string]int{},
		Reads:        map[string]int{},
	}
}

// AddUsage 累计一次 LLM 调用的 Token 用量（含缓存命中）。
func (e *Executor) AddUsage(u *llm.Usage) {
	if u == nil {
		return
	}
	e.PromptTokens += u.PromptTokens
	e.CompletionTokens += u.CompletionTokens
	e.CachedTokens += u.Cached()
}

func (e *Executor) Execute(ctx context.Context, call llm.ToolCall, emit func(Event)) (string, bool) {
	e.Calls++
	args := call.Function.Arguments
	if len(args) > 1600 {
		args = args[:1600] + "…"
	}
	emit(Event{Type: "tool_call", Name: call.Function.Name, Args: args})

	// 同一文件最多成功写入 2 次（初次生成 + 修复），阻止模型反复重写同一批文件浪费轮次。
	writePath := ""
	if call.Function.Name == "write_file" {
		writePath = argPath(call.Function.Arguments)
		if writePath != "" && e.Writes[writePath] >= 2 {
			msg := fmt.Sprintf("拒绝重复写入：%s 已成功写入 %d 次。请改为写入缺失文件或修复构建错误，不要重写已完成内容。", writePath, e.Writes[writePath])
			emit(Event{Type: "tool_result", Name: call.Function.Name, Text: msg, Output: msg, OK: false})
			e.Errors++
			e.ConsecutiveErrors++
			return "[ERROR] " + msg, false
		}
	}
	// 同一文件只读取一次：内容已在上文中，重复读取只会浪费轮次与 Token。
	readPath := ""
	if call.Function.Name == "read_file" {
		readPath = argPath(call.Function.Arguments)
		if readPath != "" && e.Reads[readPath] >= 1 {
			msg := fmt.Sprintf("拒绝重复读取：%s 本次任务已读取过，内容已在上文。请直接 write_file 修复，或读取其他尚未看过的文件。", readPath)
			emit(Event{Type: "tool_result", Name: call.Function.Name, Text: msg, Output: msg, OK: false})
			e.Errors++
			e.ConsecutiveErrors++
			return "[ERROR] " + msg, false
		}
	}

	start := time.Now()
	out, err := e.Dispatcher.Dispatch(ctx, call.Function.Name, call.Function.Arguments)
	duration := time.Since(start).Milliseconds()
	e.ToolTime += time.Since(start)

	ok := err == nil
	if ok {
		e.ConsecutiveErrors = 0
		if writePath != "" {
			e.Writes[writePath]++
		}
		if readPath != "" {
			e.Reads[readPath]++
		}
	} else {
		e.Errors++
		e.ConsecutiveErrors++
	}
	e.trackChange(call)

	display := out
	if err != nil {
		if display != "" {
			display += "\n"
		}
		display += "[ERROR] " + err.Error()
	}
	emit(Event{
		Type:   "tool_result",
		Name:   call.Function.Name,
		Text:   textutil.Truncate(display, 4000),
		Output: textutil.Truncate(display, 4000),
		OK:     ok,
		Data:   map[string]any{"duration_ms": duration},
	})
	return display, ok
}

func (e *Executor) trackChange(call llm.ToolCall) {
	switch call.Function.Name {
	case "write_file", "delete_file", "scaffold_project":
	default:
		return
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return
	}
	if path, ok := args["path"].(string); ok && path != "" {
		e.ChangedFiles[path] = true
		return
	}
	if call.Function.Name == "scaffold_project" {
		e.ChangedFiles["(项目骨架)"] = true
	}
}

func (e *Executor) Exhausted() bool { return e.Calls >= e.MaxCalls }

// argPath 从工具参数中提取 path 字段。
func argPath(argsJSON string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	p, _ := args["path"].(string)
	return p
}

// Summary 返回本轮工具执行的汇总。
func (e *Executor) Summary() string {
	return fmt.Sprintf("调用工具 %d 次，失败 %d 次，修改 %d 个文件", e.Calls, e.Errors, len(e.ChangedFiles))
}
