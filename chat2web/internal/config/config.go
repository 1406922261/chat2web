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
	ReportsRoot   string
	TasksRoot     string
	MemoryRoot    string
	RuntimeRoot   string

	RetentionPlaywrightDays int
	RetentionTaskDays       int
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
		WorkspaceRoot:  filepath.Join(root, "workspace"),
		ProjectsRoot:   filepath.Join(root, "workspace", "projects"),
		DataRoot:       filepath.Join(root, "data"),
		SkillsRoot:     filepath.Join(root, "skills"),
		ConfigsRoot:    filepath.Join(root, "configs"),
		MCPConfigPath:  filepath.Join(root, "configs", "mcp.json"),
		ReportsRoot:    filepath.Join(root, "data", "reports"),
		TasksRoot:      filepath.Join(root, "data", "tasks"),
		MemoryRoot:     filepath.Join(root, "data", "memory"),
		RuntimeRoot:    filepath.Join(root, "data", "runtime"),

		RetentionPlaywrightDays: envInt("MEMORY_RETENTION_PLAYWRIGHT_DAYS", 3),
		RetentionTaskDays:       envInt("MEMORY_RETENTION_TASK_DAYS", 7),
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
		cfg.WorkspaceRoot,
		cfg.ProjectsRoot,
		filepath.Join(cfg.MemoryRoot, "sessions"),
		filepath.Join(cfg.MemoryRoot, "projects"),
		filepath.Join(cfg.MemoryRoot, "backup"),
		filepath.Join(cfg.RuntimeRoot, "playwright"),
		filepath.Join(cfg.RuntimeRoot, "shell"),
		filepath.Join(cfg.RuntimeRoot, "screenshots"),
		filepath.Join(cfg.RuntimeRoot, "tmp"),
		filepath.Join(cfg.RuntimeRoot, "reports-tmp"),
		filepath.Join(cfg.RuntimeRoot, "tasks-tmp"),
		filepath.Join(cfg.DataRoot, "projects"),
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
	// 允许从 chat2web 或其 cmd 子目录启动 go run ./cmd/main.go
	switch strings.ToLower(filepath.Base(wd)) {
	case "cmd":
		return filepath.Dir(wd)
	case "chat2web":
		return wd
	default:
		return wd
	}
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
