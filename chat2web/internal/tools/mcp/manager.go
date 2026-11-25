package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type CallLog struct {
	Time       time.Time `json:"time"`
	Server     string    `json:"server"`
	Tool       string    `json:"tool"`
	Args       string    `json:"args"`
	Result     string    `json:"result"`
	Error      string    `json:"error,omitempty"`
	DurationMs int64     `json:"duration_ms"`
}

type ServerTool struct {
	Server string `json:"server"`
	Tool   Tool   `json:"tool"`
}

type ServerStatus struct {
	Name      string    `json:"name"`
	Command   string    `json:"command"`
	Args      []string  `json:"args"`
	Enabled   bool      `json:"enabled"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	ToolCount int       `json:"tool_count"`
	Tools     []Tool    `json:"tools"`
	UpdatedAt time.Time `json:"updated_at"`
}

type configFile struct {
	Servers []ServerConfig `json:"servers"`
}

// Manager 管理多个 MCP Server 连接、工具发现与调用日志。
type Manager struct {
	cfgPath   string
	root      string
	workspace string

	mu      sync.RWMutex
	servers map[string]*Client
	order   []string
	logs    []CallLog

	failures  map[string]int
	openUntil map[string]time.Time
	calls     int
	timeouts  int

	// toolsCache 缓存稳定工具定义，避免每轮重新遍历所有已连接 Server。
	toolsCache    []ServerTool
	toolsCachedAt time.Time
}

func NewManager(cfgPath, root, workspace string) *Manager {
	return &Manager{
		cfgPath:   cfgPath,
		root:      root,
		workspace: workspace,
		servers:   map[string]*Client{},
		failures:  map[string]int{},
		openUntil: map[string]time.Time{},
	}
}

func (m *Manager) Load() error {
	data, err := os.ReadFile(m.cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			_ = os.MkdirAll(filepath.Dir(m.cfgPath), 0o755)
			return os.WriteFile(m.cfgPath, []byte("{\n  \"servers\": []\n}\n"), 0o644)
		}
		return err
	}
	var cfg configFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("解析 mcp.json 失败: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.servers = map[string]*Client{}
	m.order = nil
	m.invalidateTools()
	for _, sc := range cfg.Servers {
		sc = m.expand(sc)
		client := NewClient(sc)
		m.servers[sc.Name] = client
		m.order = append(m.order, sc.Name)
	}
	return nil
}

func (m *Manager) expand(sc ServerConfig) ServerConfig {
	replace := func(s string) string {
		s = strings.ReplaceAll(s, "${ROOT}", m.root)
		s = strings.ReplaceAll(s, "${WORKSPACE}", m.workspace)
		return s
	}
	sc.Command = replace(sc.Command)
	for i := range sc.Args {
		sc.Args[i] = replace(sc.Args[i])
	}
	for k, v := range sc.Env {
		sc.Env[k] = replace(v)
	}
	return sc
}

func (m *Manager) saveConfig() error {
	m.mu.RLock()
	servers := make([]ServerConfig, 0, len(m.order))
	for _, name := range m.order {
		if c, ok := m.servers[name]; ok {
			servers = append(servers, c.Config())
		}
	}
	m.mu.RUnlock()
	data, err := json.MarshalIndent(configFile{Servers: servers}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.cfgPath, append(data, '\n'), 0o644)
}

// StartEnabled 异步连接所有启用的 MCP Server，避免阻塞服务启动。
func (m *Manager) StartEnabled(ctx context.Context) {
	m.mu.RLock()
	var names []string
	for name, c := range m.servers {
		if c.Config().Enabled {
			names = append(names, name)
		}
	}
	m.mu.RUnlock()
	for _, name := range names {
		go func(n string) {
			c := m.get(n)
			if c == nil {
				return
			}
			cctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			_ = c.Start(cctx)
		}(name)
	}
}

func (m *Manager) get(name string) *Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.servers[name]
}

func (m *Manager) Connect(ctx context.Context, name string) error {
	c := m.get(name)
	if c == nil {
		return fmt.Errorf("MCP Server %s 不存在", name)
	}
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	err := c.Start(cctx)
	m.mu.Lock()
	m.invalidateTools()
	m.mu.Unlock()
	return err
}

// Client 返回指定名称的 Server 客户端。
func (m *Manager) Client(name string) *Client {
	return m.get(name)
}

func (m *Manager) Disconnect(name string) error {
	c := m.get(name)
	if c == nil {
		return fmt.Errorf("MCP Server %s 不存在", name)
	}
	c.Stop()
	m.mu.Lock()
	m.invalidateTools()
	m.mu.Unlock()
	return nil
}

func (m *Manager) Statuses() []ServerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ServerStatus, 0, len(m.order))
	for _, name := range m.order {
		c, ok := m.servers[name]
		if !ok {
			continue
		}
		cfg := c.Config()
		tools := c.Tools()
		if tools == nil {
			tools = []Tool{}
		}
		out = append(out, ServerStatus{
			Name:      cfg.Name,
			Command:   cfg.Command,
			Args:      cfg.Args,
			Enabled:   cfg.Enabled,
			Status:    c.Status(),
			Error:     c.LastError(),
			ToolCount: len(tools),
			Tools:     tools,
			UpdatedAt: time.Now(),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Manager) Tools() []ServerTool {
	// 60 秒快照，避免每轮 LLM 决策都重新发现工具。
	m.mu.RLock()
	if m.toolsCache != nil && time.Since(m.toolsCachedAt) < 60*time.Second {
		out := append([]ServerTool(nil), m.toolsCache...)
		m.mu.RUnlock()
		return out
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.toolsCache != nil && time.Since(m.toolsCachedAt) < 60*time.Second {
		return append([]ServerTool(nil), m.toolsCache...)
	}
	var out []ServerTool
	for _, name := range m.order {
		c, ok := m.servers[name]
		if !ok || !c.Connected() {
			continue
		}
		for _, t := range c.Tools() {
			out = append(out, ServerTool{Server: name, Tool: t})
		}
	}
	if len(out) > 0 {
		m.toolsCache = out
		m.toolsCachedAt = time.Now()
	}
	return append([]ServerTool(nil), out...)
}

// invalidateTools 在连接状态变化时失效工具缓存（调用方需持有写锁）。
func (m *Manager) invalidateTools() {
	m.toolsCache = nil
	m.toolsCachedAt = time.Time{}
}

func (m *Manager) CallTool(ctx context.Context, server, tool string, args map[string]any) (string, error) {
	c := m.get(server)
	if c == nil {
		return "", fmt.Errorf("MCP Server %s 不存在", server)
	}
	if !c.Connected() {
		return "", fmt.Errorf("MCP Server %s 未连接，请先在 Agent 管理中心连接", server)
	}
	m.mu.Lock()
	if until, ok := m.openUntil[server]; ok && time.Now().Before(until) {
		left := int(time.Until(until).Seconds())
		m.mu.Unlock()
		return "", fmt.Errorf("MCP Server %s 连续失败已熔断，约 %d 秒后自动恢复；请改用内置工具继续", server, left)
	}
	m.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	start := time.Now()
	argBytes, _ := json.Marshal(args)
	result, err := c.CallTool(cctx, tool, args)
	entry := CallLog{
		Time:       time.Now(),
		Server:     server,
		Tool:       tool,
		Args:       truncate(string(argBytes), 1200),
		Result:     truncate(result, 3000),
		DurationMs: time.Since(start).Milliseconds(),
	}
	if err != nil {
		entry.Error = err.Error()
	}
	m.mu.Lock()
	m.calls++
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timeout") {
			m.timeouts++
		}
		m.failures[server]++
		if m.failures[server] >= 3 {
			m.openUntil[server] = time.Now().Add(90 * time.Second)
			m.failures[server] = 0
			entry.Error += "（连续 3 次失败，已熔断 90 秒）"
		}
	} else {
		m.failures[server] = 0
		delete(m.openUntil, server)
	}
	m.logs = append(m.logs, entry)
	if len(m.logs) > 200 {
		m.logs = m.logs[len(m.logs)-200:]
	}
	m.mu.Unlock()
	return result, err
}

// Available 判断 Server 当前是否可调用（已连接且未熔断）。
func (m *Manager) Available(server string) (bool, string) {
	c := m.get(server)
	if c == nil {
		return false, "Server 不存在"
	}
	if !c.Connected() {
		return false, "未连接"
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if until, ok := m.openUntil[server]; ok && time.Now().Before(until) {
		return false, fmt.Sprintf("连续失败已熔断，约 %d 秒后恢复", int(time.Until(until).Seconds()))
	}
	return true, ""
}

// MarkUnavailable 在本次任务内快速熔断某 Server（例如 Playwright 无法启动）。
func (m *Manager) MarkUnavailable(server, reason string, cooldown time.Duration) {
	if cooldown <= 0 {
		cooldown = 10 * time.Minute
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.openUntil[server] = time.Now().Add(cooldown)
	m.failures[server] = 0
	m.logs = append(m.logs, CallLog{
		Time:   time.Now(),
		Server: server,
		Tool:   "health_check",
		Result: "unavailable",
		Error:  reason + "（本次任务内不再尝试）",
	})
	if len(m.logs) > 200 {
		m.logs = m.logs[len(m.logs)-200:]
	}
}

// Stats 返回 MCP 调用的累计统计，用于性能报告。
func (m *Manager) Stats() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return map[string]any{
		"calls":    m.calls,
		"timeouts": m.timeouts,
	}
}

func (m *Manager) Logs(limit int) []CallLog {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if limit <= 0 || limit > len(m.logs) {
		limit = len(m.logs)
	}
	out := make([]CallLog, limit)
	copy(out, m.logs[len(m.logs)-limit:])
	return out
}

func (m *Manager) AddServer(sc ServerConfig, connect bool) error {
	if strings.TrimSpace(sc.Name) == "" {
		return fmt.Errorf("Server 名称不能为空")
	}
	if strings.TrimSpace(sc.Command) == "" {
		return fmt.Errorf("启动命令不能为空")
	}
	m.mu.Lock()
	if _, exists := m.servers[sc.Name]; exists {
		m.mu.Unlock()
		return fmt.Errorf("Server %s 已存在", sc.Name)
	}
	client := NewClient(sc)
	m.servers[sc.Name] = client
	m.order = append(m.order, sc.Name)
	m.invalidateTools()
	m.mu.Unlock()
	if err := m.saveConfig(); err != nil {
		return err
	}
	if connect {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		go func() { _ = client.Start(ctx) }()
	}
	return nil
}

func (m *Manager) RemoveServer(name string) error {
	m.mu.Lock()
	c, ok := m.servers[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("Server 不存在")
	}
	delete(m.servers, name)
	var order []string
	for _, n := range m.order {
		if n != name {
			order = append(order, n)
		}
	}
	m.order = order
	m.invalidateTools()
	m.mu.Unlock()
	c.Stop()
	return m.saveConfig()
}

func (m *Manager) SetEnabled(name string, enabled bool) error {
	m.mu.Lock()
	c, ok := m.servers[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("Server 不存在")
	}
	c.cfg.Enabled = enabled
	m.invalidateTools()
	m.mu.Unlock()
	if err := m.saveConfig(); err != nil {
		return err
	}
	if enabled {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		go func() { _ = c.Start(ctx) }()
	} else {
		c.Stop()
	}
	return nil
}

// FindServer 按名称或工具名查找已连接的服务（用于 Playwright 测试等能力探测）。
func (m *Manager) FindServer(hint string) *Client {
	hint = strings.ToLower(hint)
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, name := range m.order {
		c, ok := m.servers[name]
		if !ok || !c.Connected() {
			continue
		}
		if strings.Contains(strings.ToLower(name), hint) {
			return c
		}
		for _, t := range c.Tools() {
			if strings.Contains(strings.ToLower(t.Name), hint) {
				return c
			}
		}
	}
	return nil
}

var invalidNameChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// ToolName 生成符合 LLM 工具命名规范的唯一名称。
func ToolName(server, tool string) string {
	raw := "mcp__" + server + "__" + tool
	return invalidNameChars.ReplaceAllString(raw, "_")
}

// NormalizeSchema 补齐 JSON Schema 的 type/properties，兼容部分服务端省略字段的情况。
func NormalizeSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	if _, ok := schema["type"]; !ok {
		schema["type"] = "object"
	}
	if _, ok := schema["properties"]; !ok {
		schema["properties"] = map[string]any{}
	}
	return schema
}
