package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFilesystemMCPRoundTrip 真实启动 Filesystem MCP Server，发现工具并调用 tools/call。
// 默认跳过；使用 CHAT2WEB_MCP_TEST=1 go test ./internal/mcp -run TestFilesystemMCPRoundTrip 执行。
func TestFilesystemMCPRoundTrip(t *testing.T) {
	if os.Getenv("CHAT2WEB_MCP_TEST") != "1" {
		t.Skip("设置 CHAT2WEB_MCP_TEST=1 以运行真实 MCP 集成测试")
	}
	workspace := t.TempDir()
	target := filepath.Join(workspace, "hello.txt")
	if err := os.WriteFile(target, []byte("hello chat2web mcp"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := ServerConfig{
		Name:    "fs-smoke",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", workspace},
		Enabled: true,
	}
	data, _ := json.Marshal(map[string]any{"servers": []ServerConfig{cfg}})
	cfgPath := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewManager(cfgPath, t.TempDir(), workspace)
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	m.StartEnabled(ctx)
	defer func() { _ = m.Disconnect("fs-smoke") }()

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		sts := m.Statuses()
		if len(sts) == 1 && sts[0].Status == "connected" && sts[0].ToolCount > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	sts := m.Statuses()
	if len(sts) != 1 || sts[0].Status != "connected" {
		t.Fatalf("MCP 未连接: %+v", sts)
	}
	t.Logf("已连接 %s，发现工具 %d 个", sts[0].Name, sts[0].ToolCount)

	tools := m.Tools()
	if len(tools) == 0 {
		t.Fatal("未发现任何 MCP 工具")
	}

	out, err := m.CallTool(ctx, "fs-smoke", "read_text_file", map[string]any{"path": target})
	if err != nil {
		t.Fatalf("tools/call 失败: %v", err)
	}
	if !strings.Contains(out, "hello chat2web mcp") {
		t.Fatalf("工具返回内容不符合预期: %s", out)
	}
	t.Logf("tools/call 返回: %s", strings.TrimSpace(out))
}
