package mcp

// ToolsByServer 按服务端归类工具，供管理界面与 Agent 工具注册使用。
func (m *Manager) ToolsByServer() map[string][]Tool {
	out := map[string][]Tool{}
	for _, st := range m.Tools() {
		out[st.Server] = append(out[st.Server], st.Tool)
	}
	return out
}

// ConnectedCount 返回已连接的 Server 数量。
func (m *Manager) ConnectedCount() int {
	n := 0
	for _, s := range m.Statuses() {
		if s.Status == "connected" {
			n++
		}
	}
	return n
}

// TotalToolCount 返回已发现的 MCP 工具总数。
func (m *Manager) TotalToolCount() int {
	return len(m.Tools())
}
