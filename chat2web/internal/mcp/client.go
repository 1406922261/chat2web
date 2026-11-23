package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ServerConfig 对应 configs/mcp.json 中的一项。
type ServerConfig struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
	Enabled bool              `json:"enabled"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type LogEntry struct {
	Time      time.Time `json:"time"`
	Direction string    `json:"direction"`
	Message   string    `json:"message"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResp struct {
	Result json.RawMessage
	Error  *rpcError
}

// Client 是一个标准 MCP stdio 客户端，使用 JSON-RPC 2.0（换行分隔）与服务端通信。
type Client struct {
	cfg ServerConfig

	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	reader    *bufio.Reader
	nextID    int
	pending   map[int]chan rpcResp
	tools     []Tool
	status    string
	lastErr   string
	logs      []LogEntry
	serverCap map[string]any
	stopped   bool
}

func NewClient(cfg ServerConfig) *Client {
	return &Client{
		cfg:     cfg,
		pending: map[int]chan rpcResp{},
		status:  "disconnected",
	}
}

func (c *Client) Name() string { return c.cfg.Name }

func (c *Client) Config() ServerConfig { return c.cfg }

func (c *Client) Status() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *Client) LastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

func (c *Client) Tools() []Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Tool, len(c.tools))
	copy(out, c.tools)
	return out
}

func (c *Client) Logs(limit int) []LogEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if limit <= 0 || limit > len(c.logs) {
		limit = len(c.logs)
	}
	out := make([]LogEntry, limit)
	copy(out, c.logs[len(c.logs)-limit:])
	return out
}

func (c *Client) logLine(dir, msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, LogEntry{Time: time.Now(), Direction: dir, Message: msg})
	if len(c.logs) > 200 {
		c.logs = c.logs[len(c.logs)-200:]
	}
}

func (c *Client) setStatus(s, errMsg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = s
	if errMsg != "" {
		c.lastErr = errMsg
	}
}

func (c *Client) Connected() bool {
	return c.Status() == "connected"
}

// Start 启动 MCP Server 进程并完成 initialize 握手与工具发现。
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.status == "connected" || c.status == "connecting" {
		c.mu.Unlock()
		return nil
	}
	c.status = "connecting"
	c.lastErr = ""
	c.stopped = false
	c.pending = map[int]chan rpcResp{}
	c.tools = nil
	c.mu.Unlock()

	cmd := buildServerCommand(c.cfg)
	cmd.Env = os.Environ()
	for k, v := range c.cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.setStatus("error", err.Error())
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		c.setStatus("error", err.Error())
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		c.setStatus("error", err.Error())
		return err
	}
	if err := cmd.Start(); err != nil {
		c.setStatus("error", fmt.Sprintf("启动 MCP Server 失败: %v", err))
		return fmt.Errorf("启动 MCP Server %s 失败: %w", c.cfg.Name, err)
	}

	c.mu.Lock()
	c.cmd = cmd
	c.stdin = stdin
	c.reader = bufio.NewReaderSize(stdout, 1<<20)
	c.mu.Unlock()

	go c.readLoop()
	go c.drainStderr(stderr)

	initCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var initResult map[string]any
	if err := c.call(initCtx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "chat2web-ai", "version": "1.0.0"},
	}, &initResult); err != nil {
		c.setStatus("error", err.Error())
		c.Stop()
		return err
	}
	c.serverCap = initResult

	_ = c.notify("notifications/initialized", map[string]any{})

	var listResult struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := c.call(initCtx, "tools/list", map[string]any{}, &listResult); err != nil {
		c.setStatus("error", err.Error())
		return err
	}
	tools := make([]Tool, 0, len(listResult.Tools))
	for _, t := range listResult.Tools {
		tools = append(tools, Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	c.mu.Lock()
	c.tools = tools
	c.mu.Unlock()
	c.setStatus("connected", "")
	return nil
}

func (c *Client) readLoop() {
	for {
		c.mu.Lock()
		reader := c.reader
		c.mu.Unlock()
		if reader == nil {
			return
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *int            `json:"id"`
			Method  string          `json:"method"`
			Result  json.RawMessage `json:"result"`
			Error   *rpcError       `json:"error"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			c.logLine("stdout", line)
			continue
		}
		if msg.ID != nil && msg.Method == "" {
			c.mu.Lock()
			ch, ok := c.pending[*msg.ID]
			if ok {
				delete(c.pending, *msg.ID)
			}
			c.mu.Unlock()
			if ok {
				select {
				case ch <- rpcResp{Result: msg.Result, Error: msg.Error}:
				default:
				}
			}
			continue
		}
		if msg.ID != nil && msg.Method != "" {
			// 服务端反向请求，本客户端不提供采样能力
			c.respondError(*msg.ID, -32601, "method not supported by client")
			continue
		}
		c.logLine("notify", msg.Method)
	}
	c.setStatus("disconnected", "MCP Server 连接已关闭")
	c.failPending()
}

func (c *Client) drainStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 32*1024), 512*1024)
	for scanner.Scan() {
		c.logLine("stderr", scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		c.logLine("stderr", "读取 stderr 失败: "+err.Error())
	}
}

func (c *Client) failPending() {
	c.mu.Lock()
	pending := c.pending
	c.pending = map[int]chan rpcResp{}
	c.mu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- rpcResp{Error: &rpcError{Code: -32000, Message: "连接已断开"}}:
		default:
		}
	}
}

func (c *Client) nextRequestID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return c.nextID
}

func (c *Client) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	stdin := c.stdin
	c.mu.Unlock()
	if stdin == nil {
		return errors.New("MCP Server 未连接")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := stdin.Write(append(data, '\n')); err != nil {
		return err
	}
	c.logLineLocked("send", string(data))
	return nil
}

func (c *Client) logLineLocked(dir, msg string) {
	c.logs = append(c.logs, LogEntry{Time: time.Now(), Direction: dir, Message: truncate(msg, 2000)})
	if len(c.logs) > 200 {
		c.logs = c.logs[len(c.logs)-200:]
	}
}

func (c *Client) notify(method string, params any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *Client) respondError(id int, code int, message string) {
	_ = c.send(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	id := c.nextRequestID()
	ch := make(chan rpcResp, 1)
	c.mu.Lock()
	if c.status == "disconnected" || c.status == "error" {
		c.mu.Unlock()
		return errors.New("MCP Server 未连接")
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("MCP 调用超时: %s", method)
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("MCP 错误 %d: %s", resp.Error.Code, resp.Error.Message)
		}
		if out != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("解析 MCP 响应失败: %w", err)
			}
		}
		return nil
	}
}

// CallTool 调用服务端工具并返回文本结果。
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if !c.Connected() {
		return "", fmt.Errorf("MCP Server %s 未连接", c.cfg.Name)
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool            `json:"isError"`
		Raw     json.RawMessage `json:"-"`
	}
	if err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &result); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, item := range result.Content {
		if item.Text != "" {
			b.WriteString(item.Text)
			b.WriteString("\n")
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		data, _ := json.Marshal(result)
		out = string(data)
	}
	if result.IsError {
		return out, fmt.Errorf("MCP 工具返回错误: %s", out)
	}
	return out, nil
}

func (c *Client) Stop() {
	c.mu.Lock()
	c.stopped = true
	cmd := c.cmd
	c.stdin = nil
	c.reader = nil
	c.status = "disconnected"
	c.mu.Unlock()
	c.failPending()
	if cmd != nil && cmd.Process != nil {
		if runtime.GOOS == "windows" {
			_ = exec.Command("taskkill", "/PID", fmt.Sprintf("%d", cmd.Process.Pid), "/T", "/F").Run()
		} else {
			_ = cmd.Process.Kill()
		}
	}
}

func buildServerCommand(cfg ServerConfig) *exec.Cmd {
	args := make([]string, 0, len(cfg.Args))
	args = append(args, cfg.Args...)
	if runtime.GOOS == "windows" {
		parts := []string{cfg.Command}
		for _, a := range args {
			parts = append(parts, quoteArg(a))
		}
		return exec.Command("cmd", "/C", strings.Join(parts, " "))
	}
	return exec.Command(cfg.Command, args...)
}

func quoteArg(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
