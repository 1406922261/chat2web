package tools

import (
	"context"
	"fmt"

	"chat2web/internal/llm"
	"chat2web/internal/mcp"
)

// MCPTool 将 MCP Server 提供的工具包装成 Agent 可调用的工具。
type MCPTool struct {
	server string
	tool   mcp.Tool
	client *mcp.Client
	reg    *Registry
}

func (t *MCPTool) Name() string { return mcp.ToolName(t.server, t.tool.Name) }

func (t *MCPTool) Description() string {
	desc := t.tool.Description
	if desc == "" {
		desc = "MCP 工具 " + t.tool.Name
	}
	return fmt.Sprintf("[MCP:%s] %s", t.server, desc)
}

func (t *MCPTool) Parameters() map[string]any {
	return mcp.NormalizeSchema(t.tool.InputSchema)
}

func (t *MCPTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if !t.client.Connected() {
		return "", fmt.Errorf("MCP Server %s 已断开，请重新连接", t.server)
	}
	return t.client.CallTool(ctx, t.tool.Name, args)
}

// RegisterMCPTools 把所有已连接 MCP Server 的工具注册到本次运行的注册中心。
func RegisterMCPTools(reg *Registry, mgr *mcp.Manager) int {
	return RegisterMCPToolsFiltered(reg, mgr, nil)
}

// RegisterMCPToolsFiltered 只注册指定 Server 的工具；servers 为空时等价于全量注册。
// 用于按任务意图筛选工具，避免把几十个无关 Schema 每轮发送给模型。
func RegisterMCPToolsFiltered(reg *Registry, mgr *mcp.Manager, servers map[string]bool) int {
	if mgr == nil {
		return 0
	}
	count := 0
	for _, st := range mgr.Tools() {
		if len(servers) > 0 && !servers[st.Server] {
			continue
		}
		client := mgr.Client(st.Server)
		if client == nil {
			continue
		}
		reg.Register(&MCPTool{server: st.Server, tool: st.Tool, client: client, reg: reg})
		count++
	}
	return count
}

// MCPDefinitions 便于调试：查看注册中心中的 MCP 工具定义。
func MCPDefinitions(reg *Registry) []llm.ToolDef {
	var out []llm.ToolDef
	for _, t := range reg.List() {
		if mt, ok := t.(*MCPTool); ok && mt != nil {
			out = append(out, llm.ToolDef{Type: "function", Function: llm.ToolFunction{
				Name: t.Name(), Description: t.Description(), Parameters: t.Parameters(),
			}})
		}
	}
	return out
}
