package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Message 是 OpenAI 兼容的对话消息。
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Usage      *Usage     `json:"-"`
}

// Usage 是服务端返回的 token 用量，接口未提供时为 nil。
type Usage struct {
	PromptTokens        int                 `json:"prompt_tokens"`
	CompletionTokens    int                 `json:"completion_tokens"`
	TotalTokens         int                 `json:"total_tokens"`
	PromptTokensDetails *PromptTokenDetails `json:"prompt_tokens_details,omitempty"`
}

// PromptTokenDetails 是输入 Token 的细分（部分服务商支持 Prompt Caching）。
type PromptTokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// Cached 返回命中缓存的输入 Token，nil 安全。
func (u *Usage) Cached() int {
	if u == nil || u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CachedTokens
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolDef struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// Client 调用支持 Tool Calling 的 OpenAI 兼容接口（火山方舟 / OpenAI / DeepSeek 等）。
type Client struct {
	BaseURL     string
	APIKey      string
	Model       string
	Temperature float64
	MaxTokens   int
	HTTP        *http.Client
	// OnProgress 在调用方没有接入流式输出时周期性上报等待进度，避免终端长时间无反馈。
	OnProgress func(string)
}

func New(baseURL, apiKey, model string, temperature float64, maxTokens int) *Client {
	if temperature <= 0 {
		temperature = 0.4
	}
	return &Client{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		APIKey:      apiKey,
		Model:       model,
		Temperature: temperature,
		MaxTokens:   maxTokens,
		HTTP:        &http.Client{Timeout: 10 * time.Minute},
	}
}

func (c *Client) Configured() bool {
	return c != nil && c.APIKey != "" && c.Model != "" && c.BaseURL != ""
}

func (c *Client) StatusMessage() string {
	if c == nil || c.APIKey == "" {
		return "未配置 LLM_API_KEY：请在项目根目录 .env 中填写 LLM_API_KEY（或 ARK_API_KEY / OPENAI_API_KEY / DEEPSEEK_API_KEY）"
	}
	if c.Model == "" {
		return "未配置 LLM_MODEL：请在 .env 中填写模型名称（如 doubao-seed-1-6-250615 / gpt-4o-mini）"
	}
	return ""
}

type chatChunk struct {
	Usage   *Usage `json:"usage"`
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// Chat 发起流式对话；网络中断、429 或 5xx 时自动重试，避免整个任务因一次断流失败。
func (c *Client) Chat(ctx context.Context, msgs []Message, tools []ToolDef, onDelta func(string)) (*Message, error) {
	if !c.Configured() {
		return nil, errors.New(c.StatusMessage())
	}
	payload := map[string]any{
		"model":       c.Model,
		"messages":    msgs,
		"temperature": c.Temperature,
		"stream":      true,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
		payload["tool_choice"] = "auto"
	}
	if c.MaxTokens > 0 {
		payload["max_tokens"] = c.MaxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := range 3 {
		msg, err := c.streamOnce(ctx, body, onDelta)
		if err == nil {
			return msg, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, ErrOutputTruncated) {
			return nil, err
		}
		lastErr = err
		var se *httpStatusError
		if errors.As(err, &se) && se.Code != http.StatusTooManyRequests && se.Code < 500 {
			return nil, err
		}
		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			if onDelta != nil {
				onDelta("\n[连接中断，已自动重试…]\n")
			} else if c.OnProgress != nil {
				c.OnProgress(fmt.Sprintf("LLM 调用中断，正在重试（%d/3）：%v", attempt+1, err))
			}
			// 重试时不再重复输出增量，避免终端出现重复内容。
			onDelta = nil
		}
	}
	return nil, fmt.Errorf("LLM 请求连续失败（已重试 3 次）: %w", lastErr)
}

type httpStatusError struct {
	Code int
	Body string
}

// ErrOutputTruncated 表示模型输出达到 max_tokens 上限被截断，工具参数通常不完整。
var ErrOutputTruncated = errors.New("LLM 输出达到长度上限被截断")

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("LLM 接口返回 %d: %s", e.Code, e.Body)
}

func (c *Client) streamOnce(ctx context.Context, body []byte, onDelta func(string)) (*Message, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "text/event-stream")

	// 没有流式回调时，用心跳把等待进度暴露到终端，覆盖建连、首字节与流式读取全过程。
	var received atomic.Int64
	stopProgress := make(chan struct{})
	var progressWG sync.WaitGroup
	if c.OnProgress != nil && onDelta == nil {
		progressWG.Add(1)
		go func() {
			defer progressWG.Done()
			start := time.Now()
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stopProgress:
					return
				case <-ticker.C:
					c.OnProgress(fmt.Sprintf("模型响应中… 已等待 %d 秒，已接收 %d 字符", int(time.Since(start).Seconds()), received.Load()))
				}
			}
		}()
		defer func() {
			close(stopProgress)
			progressWG.Wait()
		}()
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用 LLM 失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4000))
		resp.Body.Close()
		return nil, &httpStatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var content strings.Builder
	finishReason := ""
	calls := map[int]*ToolCall{}
	order := []int{}
	var usage *Usage

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		received.Add(int64(len(data)))
		if data == "[DONE]" {
			break
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, ch := range chunk.Choices {
			if ch.FinishReason != "" {
				finishReason = ch.FinishReason
			}
			if ch.Delta.Content != "" {
				content.WriteString(ch.Delta.Content)
				if onDelta != nil {
					onDelta(ch.Delta.Content)
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				cur, ok := calls[tc.Index]
				if !ok {
					cur = &ToolCall{Type: "function"}
					calls[tc.Index] = cur
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					cur.ID = tc.ID
				}
				if tc.Type != "" {
					cur.Type = tc.Type
				}
				if tc.Function.Name != "" && cur.Function.Name == "" {
					cur.Function.Name = tc.Function.Name
				}
				cur.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("读取 LLM 流失败: %w", err)
	}
	if finishReason == "length" {
		return nil, ErrOutputTruncated
	}

	msg := &Message{Role: "assistant", Content: content.String(), Usage: usage}
	for _, idx := range order {
		tc := calls[idx]
		if tc.Function.Name == "" {
			continue
		}
		if tc.ID == "" {
			tc.ID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), idx)
		}
		msg.ToolCalls = append(msg.ToolCalls, *tc)
	}
	if strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 {
		return nil, errors.New("LLM 返回内容为空，请检查模型是否支持 Tool Calling")
	}
	return msg, nil
}

// Complete 发起一次非工具调用的纯文本请求。
func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	msg, err := c.Chat(ctx, []Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, nil, nil)
	if err != nil {
		return "", err
	}
	return msg.Content, nil
}

// CompleteJSON 请求模型输出 JSON 并解析到 out。
func (c *Client) CompleteJSON(ctx context.Context, system, user string, out any) error {
	_, err := c.CompleteJSONUsage(ctx, system, user, out)
	return err
}

// CompleteJSONUsage 与 CompleteJSON 相同，同时返回本次调用的 Token 用量。
func (c *Client) CompleteJSONUsage(ctx context.Context, system, user string, out any) (*Usage, error) {
	msg, err := c.Chat(ctx, []Message{
		{Role: "system", Content: system + "\n只输出合法 JSON，不要输出 Markdown 代码块或解释。"},
		{Role: "user", Content: user},
	}, nil, nil)
	if err != nil {
		return nil, err
	}
	clean := extractJSON(msg.Content)
	if err := json.Unmarshal([]byte(clean), out); err != nil {
		return msg.Usage, fmt.Errorf("解析模型 JSON 失败: %w", err)
	}
	return msg.Usage, nil
}

// extractJSON 从模型输出中截取最外层的 JSON 对象，兼容推理模型附加说明文字的情况。
func extractJSON(s string) string {
	s = stripFences(s)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			return strings.TrimSpace(s[i : j+1])
		}
	}
	return s
}

func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}
