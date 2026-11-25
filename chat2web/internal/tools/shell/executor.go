package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Result 表示一次 Shell 命令的执行结果。
type Result struct {
	ID         string    `json:"id"`
	Command    string    `json:"command"`
	Cwd        string    `json:"cwd"`
	ExitCode   int       `json:"exit_code"`
	Stdout     string    `json:"stdout"`
	Stderr     string    `json:"stderr"`
	DurationMs int64     `json:"duration_ms"`
	TimedOut   bool      `json:"timed_out"`
	StartedAt  time.Time `json:"started_at"`
	Error      string    `json:"error,omitempty"`
	Blocked    string    `json:"blocked,omitempty"`
	Allowed    bool      `json:"allowed"`
}

// Process 表示一个后台进程（如开发服务器）。
type Process struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Command   string    `json:"command"`
	Cwd       string    `json:"cwd"`
	Status    string    `json:"status"`
	URL       string    `json:"url"`
	PID       int       `json:"pid"`
	ExitCode  int       `json:"exit_code"`
	StartedAt time.Time `json:"started_at"`

	cmd    *exec.Cmd
	mu     sync.Mutex
	lines  []string
	urlSet bool
}

type Executor struct {
	root     string
	timeout  time.Duration
	maxChars int
	mu       sync.RWMutex
	procs    map[string]*Process
	logs     []Result
	logOrder []string
	seq      int
}

func NewExecutor(root string, timeout time.Duration, maxChars int) *Executor {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if maxChars <= 0 {
		maxChars = 20000
	}
	return &Executor{
		root:     root,
		timeout:  timeout,
		maxChars: maxChars,
		procs:    map[string]*Process{},
	}
}

func (e *Executor) Root() string { return e.root }

func (e *Executor) newID(prefix string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().Unix(), e.seq)
}

// Run 在前台执行一条命令，带超时、输出截断与安全校验。
func (e *Executor) Run(ctx context.Context, command, dir string) Result {
	res := Result{
		ID:        e.newID("cmd"),
		Command:   command,
		Cwd:       dir,
		StartedAt: time.Now(),
		Allowed:   true,
	}
	if strings.TrimSpace(dir) == "" {
		dir = e.root
	}
	abs, err := filepath.Abs(dir)
	if err != nil || !within(e.root, abs) {
		res.Allowed = false
		res.ExitCode = -1
		res.Error = "工作目录超出允许的 workspace 范围"
		res.Blocked = res.Error
		e.log(res)
		return res
	}
	res.Cwd = abs

	decision := Validate(command, abs, e.root)
	if !decision.Allowed {
		res.Allowed = false
		res.ExitCode = -1
		res.Error = "命令被安全策略拦截: " + decision.Reason
		res.Blocked = decision.Reason
		e.log(res)
		return res
	}

	runCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	cmd := buildCommand(runCtx, command)
	cmd.Dir = abs
	cmd.Env = append(cmd.Environ(), "CI=1", "NO_COLOR=1", "FORCE_COLOR=0", "npm_config_yes=true")

	stdout := &cappedBuffer{max: e.maxChars}
	stderr := &cappedBuffer{max: e.maxChars / 2}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	start := time.Now()
	err = cmd.Run()
	res.DurationMs = time.Since(start).Milliseconds()
	res.Stdout = stdout.String()
	res.Stderr = stderr.String()

	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		res.ExitCode = -1
		res.Error = fmt.Sprintf("命令执行超时（>%s）", e.timeout)
	} else if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			res.Error = fmt.Sprintf("命令退出码 %d", res.ExitCode)
		} else {
			res.ExitCode = -1
			res.Error = err.Error()
		}
	}
	e.log(res)
	return res
}

// StartProcess 启动后台进程（开发服务器等），并自动探测运行地址。
func (e *Executor) StartProcess(ctx context.Context, name, command, dir string) (*Process, error) {
	if strings.TrimSpace(dir) == "" {
		dir = e.root
	}
	abs, err := filepath.Abs(dir)
	if err != nil || !within(e.root, abs) {
		return nil, errors.New("工作目录超出允许的 workspace 范围")
	}
	decision := Validate(command, abs, e.root)
	if !decision.Allowed {
		return nil, fmt.Errorf("命令被安全策略拦截: %s", decision.Reason)
	}
	if p := e.findRunning(abs, command); p != nil {
		return p, nil
	}

	cmd := buildCommand(context.Background(), command)
	cmd.Dir = abs
	cmd.Env = append(cmd.Environ(), "CI=1", "NO_COLOR=1", "FORCE_COLOR=0", "npm_config_yes=true")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动进程失败: %w", err)
	}

	proc := &Process{
		ID:        e.newID("proc"),
		Name:      name,
		Command:   command,
		Cwd:       abs,
		Status:    "running",
		StartedAt: time.Now(),
		cmd:       cmd,
	}
	if cmd.Process != nil {
		proc.PID = cmd.Process.Pid
	}

	e.mu.Lock()
	e.procs[proc.ID] = proc
	e.mu.Unlock()

	go proc.pump(stdout)
	go proc.pump(stderr)
	go func() {
		err := cmd.Wait()
		proc.mu.Lock()
		proc.Status = "stopped"
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				proc.ExitCode = exitErr.ExitCode()
			} else {
				proc.ExitCode = -1
			}
		}
		proc.mu.Unlock()
	}()

	return proc, nil
}

func (p *Process) pump(r interface{ Read([]byte) (int, error) }) {
	buf := make([]byte, 4096)
	var pending string
	for {
		n, err := r.Read(buf)
		if n > 0 {
			pending += string(buf[:n])
			for {
				idx := strings.IndexAny(pending, "\r\n")
				if idx < 0 {
					break
				}
				line := pending[:idx]
				pending = pending[idx+1:]
				p.appendLine(line)
			}
			if len(pending) > 2000 {
				p.appendLine(pending)
				pending = ""
			}
		}
		if err != nil {
			break
		}
	}
	if strings.TrimSpace(pending) != "" {
		p.appendLine(pending)
	}
}

var urlRe = regexp.MustCompile(`(https?://(?:localhost|127\.0\.0\.1|0\.0\.0\.0):\d+(?:/[^\s"']*)?)`)

func (p *Process) appendLine(line string) {
	line = strings.TrimSpace(strings.TrimRight(line, "\r"))
	if line == "" {
		return
	}
	p.mu.Lock()
	p.lines = append(p.lines, line)
	if len(p.lines) > 400 {
		p.lines = p.lines[len(p.lines)-400:]
	}
	if !p.urlSet {
		if m := urlRe.FindString(line); m != "" {
			p.URL = strings.Replace(m, "0.0.0.0", "127.0.0.1", 1)
			p.urlSet = true
		}
	}
	p.mu.Unlock()
}

func (p *Process) URLValue() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.URL
}

func (p *Process) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Status == "running"
}

func (p *Process) Output(limit int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	lines := p.lines
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return strings.Join(lines, "\n")
}

func (p *Process) Snapshot() Process {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Process{
		ID:        p.ID,
		Name:      p.Name,
		Command:   p.Command,
		Cwd:       p.Cwd,
		Status:    p.Status,
		URL:       p.URL,
		PID:       p.PID,
		ExitCode:  p.ExitCode,
		StartedAt: p.StartedAt,
	}
}

func (e *Executor) WaitForURL(ctx context.Context, id string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p := e.GetProcess(id)
		if p == nil {
			return ""
		}
		if u := p.URLValue(); u != "" {
			return u
		}
		if !p.Running() {
			return ""
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(300 * time.Millisecond):
		}
	}
	return ""
}

func (e *Executor) GetProcess(id string) *Process {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.procs[id]
}

func (e *Executor) findRunning(dir, command string) *Process {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, p := range e.procs {
		if p.Running() && p.Cwd == dir && p.Command == command {
			return p
		}
	}
	return nil
}

func (e *Executor) StopProcess(id string) error {
	p := e.GetProcess(id)
	if p == nil {
		return errors.New("进程不存在")
	}
	if p.cmd == nil || p.cmd.Process == nil {
		return errors.New("进程未启动")
	}
	if runtime.GOOS == "windows" {
		kill := exec.Command("taskkill", "/PID", fmt.Sprintf("%d", p.cmd.Process.Pid), "/T", "/F")
		_ = kill.Run()
	} else {
		_ = p.cmd.Process.Kill()
	}
	p.mu.Lock()
	p.Status = "stopped"
	p.mu.Unlock()
	return nil
}

func (e *Executor) StopByName(name string) {
	e.mu.RLock()
	var targets []*Process
	for _, p := range e.procs {
		if p.Name == name {
			targets = append(targets, p)
		}
	}
	e.mu.RUnlock()
	for _, p := range targets {
		_ = e.StopProcess(p.ID)
	}
}

func (e *Executor) Processes() []Process {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Process, 0, len(e.procs))
	for _, p := range e.procs {
		out = append(out, p.Snapshot())
	}
	return out
}

func (e *Executor) log(res Result) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.logs = append(e.logs, res)
	e.logOrder = append(e.logOrder, res.ID)
	if len(e.logs) > 300 {
		e.logs = e.logs[len(e.logs)-300:]
		e.logOrder = e.logOrder[len(e.logOrder)-300:]
	}
}

func (e *Executor) Logs(limit int) []Result {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if limit <= 0 || limit > len(e.logs) {
		limit = len(e.logs)
	}
	out := make([]Result, limit)
	copy(out, e.logs[len(e.logs)-limit:])
	return out
}

func buildCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		command = normalizeWindowsCommand(command)
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "sh", "-c", command)
}

// normalizeWindowsCommand 把 Linux 专用写法改写为 Windows 等价物，避免命令假失败。
// 例如 curl -o /dev/null 在 Windows 会因写入失败返回退出码 23。
func normalizeWindowsCommand(command string) string {
	return strings.ReplaceAll(command, "/dev/null", "NUL")
}

type cappedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.max <= 0 || b.buf.Len() < b.max {
		remain := b.max - b.buf.Len()
		if b.max > 0 && len(p) > remain {
			b.buf.Write(p[:remain])
			b.truncated = true
		} else if b.max > 0 {
			b.buf.Write(p)
		} else {
			b.buf.Write(p)
		}
	} else {
		b.truncated = true
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf.String()
	if b.truncated {
		s += "\n...(输出已截断)"
	}
	return s
}
