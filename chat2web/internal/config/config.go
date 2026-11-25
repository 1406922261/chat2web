package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config 保存服务端运行配置。所有密钥来自环境变量或根目录 .env，不硬编码。
type Config struct {
	Port           string
	LLMBaseURL     string
	LLMAPIKey      string
	LLMModel       string
	LLMTemperature float64
	LLMMaxTokens   int
	LLMTimeout     time.Duration

	Root          string
	WorkspaceRoot string
	ProjectsRoot  string
	DataRoot      string
	SkillsRoot    string
	ConfigsRoot   string
	MCPConfigPath string
	MemoryRoot    string
	SessionsRoot  string
	BackupsRoot   string
	ReportsRoot   string
	TasksRoot     string
	RuntimeRoot   string

	RetentionPlaywrightDays int
	RetentionTaskDays       int
	RetentionSessionDays    int
	ReportsKeep             int
	MemoryTokenBudget       int

	MaxAgentSteps     int
	MaxToolCalls      int
	MaxRepairs        int
	ContextCharBudget int
	BuildTimeout      time.Duration
	ShellTimeout      time.Duration
	MaxOutputChars    int
}

// Load 读取配置并确保运行时目录存在。
func Load() *Config {
	root := detectRoot()
	// 优先读取 chat2web/.env；若不存在则回退到工作区根目录的 .env（便于复用已有 Key）。
	_ = loadDotEnv(filepath.Join(root, ".env"))
	_ = loadDotEnv(filepath.Join(root, "..", ".env"))

	cfg := &Config{
		Port:           env("PORT", "8787"),
		LLMBaseURL:     env("LLM_BASE_URL", ""),
		LLMAPIKey:      env("LLM_API_KEY", ""),
		LLMModel:       env("LLM_MODEL", ""),
		LLMTemperature: envFloat("LLM_TEMPERATURE", 0.4),
		LLMMaxTokens:   envInt("LLM_MAX_TOKENS", 8192),
		LLMTimeout:     time.Duration(envInt("LLM_TIMEOUT_SECONDS", 240)) * time.Second,
		Root:           root,
		// 项目统一存放在 data/projects/{project_id}，一个项目只有一个目录。
		// WorkspaceRoot 作为 Shell/MCP 的工作根，指向同一位置。
		WorkspaceRoot: filepath.Join(root, "data", "projects"),
		ProjectsRoot:  filepath.Join(root, "data", "projects"),
		DataRoot:      filepath.Join(root, "data"),
		SkillsRoot:    filepath.Join(root, "skills"),
		ConfigsRoot:   filepath.Join(root, "configs"),
		MCPConfigPath: filepath.Join(root, "configs", "mcp.json"),
		// 记忆分层：全局记忆在 data/memory，会话摘要与备份分别落在 data/sessions、data/backups。
		MemoryRoot:   filepath.Join(root, "data", "memory"),
		SessionsRoot: filepath.Join(root, "data", "sessions"),
		BackupsRoot:  filepath.Join(root, "data", "backups"),
		// 临时数据只保留在两处：报告成果归 data/exports，运行日志归 data/logs。
		ReportsRoot: filepath.Join(root, "data", "exports", "reports"),
		TasksRoot:   filepath.Join(root, "data", "sessions", "tasks"),
		RuntimeRoot: filepath.Join(root, "data", "logs", "runtime"),

		RetentionPlaywrightDays: envInt("MEMORY_RETENTION_PLAYWRIGHT_DAYS", 3),
		RetentionTaskDays:       envInt("MEMORY_RETENTION_TASK_DAYS", 7),
		RetentionSessionDays:    envInt("MEMORY_SESSION_DAYS", 7),
		ReportsKeep:             envInt("MEMORY_REPORTS_KEEP", 10),
		MemoryTokenBudget:       envInt("MEMORY_TOKEN_BUDGET", 2400),

		MaxAgentSteps:     envInt("MAX_AGENT_STEPS", 6),
		MaxToolCalls:      envInt("MAX_TOOL_CALLS", 40),
		MaxRepairs:        envInt("MAX_REPAIRS", 2),
		ContextCharBudget: envInt("CONTEXT_CHAR_BUDGET", 24000),
		BuildTimeout:      time.Duration(envInt("BUILD_TIMEOUT_SECONDS", 180)) * time.Second,
		ShellTimeout:      time.Duration(envInt("SHELL_TIMEOUT_SECONDS", 300)) * time.Second,
		MaxOutputChars:    envInt("MAX_OUTPUT_CHARS", 24000),
	}

	// 兼容主流模型服务商的 Key 命名
	if cfg.LLMAPIKey == "" {
		candidates := []struct{ key, base string }{
			{"ARK_API_KEY", "https://ark.cn-beijing.volces.com/api/v3"},
			{"OPENAI_API_KEY", "https://api.openai.com/v1"},
			{"DEEPSEEK_API_KEY", "https://api.deepseek.com/v1"},
			{"MOONSHOT_API_KEY", "https://api.moonshot.cn/v1"},
			{"DASHSCOPE_API_KEY", "https://dashscope.aliyuncs.com/compatible-mode/v1"},
		}
		for _, c := range candidates {
			if v := os.Getenv(c.key); v != "" {
				cfg.LLMAPIKey = v
				if cfg.LLMBaseURL == "" {
					cfg.LLMBaseURL = c.base
				}
				break
			}
		}
	}
	if cfg.LLMBaseURL == "" {
		cfg.LLMBaseURL = "https://ark.cn-beijing.volces.com/api/v3"
	}
	cfg.LLMBaseURL = strings.TrimRight(cfg.LLMBaseURL, "/")

	for _, dir := range []string{
		cfg.DataRoot,
		cfg.ProjectsRoot,
		cfg.MemoryRoot,
		cfg.SessionsRoot,
		cfg.BackupsRoot,
		filepath.Join(cfg.RuntimeRoot, "playwright"),
		filepath.Join(cfg.RuntimeRoot, "shell"),
		filepath.Join(cfg.RuntimeRoot, "screenshots"),
		filepath.Join(cfg.RuntimeRoot, "tmp"),
		filepath.Join(cfg.RuntimeRoot, "reports-tmp"),
		filepath.Join(cfg.RuntimeRoot, "tasks-tmp"),
		filepath.Join(cfg.RuntimeRoot, "tools"),
		filepath.Join(cfg.DataRoot, "logs"),
		filepath.Join(cfg.DataRoot, "exports"),
		cfg.ReportsRoot,
		cfg.TasksRoot,
		cfg.SkillsRoot,
		cfg.ConfigsRoot,
	} {
		_ = os.MkdirAll(dir, 0o755)
	}

	return cfg
}

func detectRoot() string {
	if v := os.Getenv("CHAT2WEB_ROOT"); v != "" {
		return v
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	// 从当前目录向上查找同时带 go.mod 的目录，兼容 go run ./cmd/server 与在 cmd/server 内 go run .
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return wd
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
	return scanner.Err()
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := env(key, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := env(key, ""); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return n
		}
	}
	return def
}
