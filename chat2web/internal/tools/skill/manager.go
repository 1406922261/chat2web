package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"chat2web/internal/textutil"
)

// Skill 是从 SKILL.md 解析出的技能定义。
type Skill struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Triggers    []string  `json:"triggers"`
	Tags        []string  `json:"tags"`
	Path        string    `json:"path"`
	Body        string    `json:"body"`
	Size        int       `json:"size"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Match struct {
	Skill *Skill `json:"skill"`
	Score int    `json:"score"`
}

type Manager struct {
	root   string
	mu     sync.RWMutex
	skills map[string]*Skill
	order  []string
	cache  map[string]string
}

func NewManager(root string) *Manager {
	return &Manager{root: root, skills: map[string]*Skill{}, cache: map[string]string{}}
}

func (m *Manager) Root() string { return m.root }

// Scan 扫描 skills/*/SKILL.md 并动态加载，新增 Skill 无需改代码。
func (m *Manager) Scan() (int, error) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return 0, err
	}
	loaded := map[string]*Skill{}
	var order []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		p := filepath.Join(m.root, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		sk := parseSkill(p, string(data))
		if sk.Name == "" {
			sk.Name = entry.Name()
		}
		loaded[sk.Name] = sk
		order = append(order, sk.Name)
	}
	sort.SliceStable(order, func(i, j int) bool {
		return loaded[order[i]].Name < loaded[order[j]].Name
	})
	m.mu.Lock()
	m.skills = loaded
	m.order = order
	m.cache = map[string]string{}
	m.mu.Unlock()
	return len(order), nil
}

// ScanDir 加载单个目录（用于测试与热更新）。
func (m *Manager) ScanDir(dir string) (*Skill, error) {
	p := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	sk := parseSkill(p, string(data))
	if sk.Name == "" {
		sk.Name = filepath.Base(dir)
	}
	m.mu.Lock()
	m.skills[sk.Name] = sk
	if !contains(m.order, sk.Name) {
		m.order = append(m.order, sk.Name)
	}
	m.cache = map[string]string{}
	m.mu.Unlock()
	return sk, nil
}

func (m *Manager) List() []*Skill {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Skill, 0, len(m.order))
	for _, name := range m.order {
		if sk, ok := m.skills[name]; ok {
			out = append(out, sk)
		}
	}
	return out
}

func (m *Manager) Get(name string) (*Skill, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sk, ok := m.skills[name]
	return sk, ok
}

// Match 根据任务描述匹配最相关的 Skill，避免加载无关技能。
func (m *Manager) Match(query string, max int) []Match {
	if max <= 0 {
		max = 3
	}
	qk := map[string]bool{}
	for _, k := range textutil.Keywords(query) {
		qk[k] = true
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	var matches []Match
	for _, name := range m.order {
		sk, ok := m.skills[name]
		if !ok {
			continue
		}
		score := 0
		meta := strings.ToLower(strings.Join([]string{
			sk.Name, sk.Description, strings.Join(sk.Triggers, " "), strings.Join(sk.Tags, " "),
		}, " "))
		for k := range qk {
			if strings.Contains(meta, k) {
				score += 3
			}
			if strings.Contains(strings.ToLower(sk.Body), k) {
				score++
			}
		}
		// 技能是核心开发环节时给予基础权重，保证需求类技能不会被完全跳过
		if score > 0 {
			matches = append(matches, Match{Skill: sk, Score: score})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].Score > matches[j].Score })
	if len(matches) > max {
		matches = matches[:max]
	}
	return matches
}

// Summaries 只返回技能名称与简介，供系统提示词做「按需加载」索引，不注入正文。
func (m *Manager) Summaries() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var b strings.Builder
	for _, name := range m.order {
		sk, ok := m.skills[name]
		if !ok {
			continue
		}
		desc := textutil.Truncate(strings.TrimSpace(sk.Description), 150)
		if desc == "" {
			desc = textutil.Truncate(firstLine(sk.Body), 150)
		}
		fmt.Fprintf(&b, "- %s：%s\n", sk.Name, desc)
	}
	return strings.TrimRight(b.String(), "\n")
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if line != "" {
			return line
		}
	}
	return ""
}

// BuildContextCached 按技能集合缓存注入正文，避免同一任务重复拼接相同 Skill 内容。
func (m *Manager) BuildContextCached(matches []Match, maxChars int) string {
	if len(matches) == 0 {
		return ""
	}
	names := make([]string, 0, len(matches))
	for _, mt := range matches {
		if mt.Skill != nil {
			names = append(names, mt.Skill.Name)
		}
	}
	sort.Strings(names)
	key := strings.Join(names, ",") + fmt.Sprintf("|%d", maxChars)

	m.mu.RLock()
	if cached, ok := m.cache[key]; ok {
		m.mu.RUnlock()
		return cached
	}
	m.mu.RUnlock()

	out := m.BuildContext(matches, maxChars)
	m.mu.Lock()
	if m.cache == nil {
		m.cache = map[string]string{}
	}
	m.cache[key] = out
	m.mu.Unlock()
	return out
}

func (m *Manager) BuildContext(matches []Match, maxChars int) string {
	if len(matches) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## 本次任务已动态加载的技能（必须遵循）\n\n")
	budget := maxChars
	if budget <= 0 {
		budget = 14000
	}
	for _, mt := range matches {
		body := mt.Skill.Body
		if len([]rune(body)) > budget {
			body = string([]rune(body)[:budget])
		}
		fmt.Fprintf(&b, "### Skill: %s\n%s\n\n", mt.Skill.Name, body)
		budget -= len([]rune(body))
		if budget <= 800 {
			break
		}
	}
	return b.String()
}

func parseSkill(path, raw string) *Skill {
	sk := &Skill{Path: path, Body: raw, Size: len(raw)}
	info, err := os.Stat(path)
	if err == nil {
		sk.UpdatedAt = info.ModTime()
	}
	content := strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return sk
	}
	trimmed := strings.TrimSpace(content)
	front, body, ok := strings.Cut(trimmed, "\n---")
	if !ok {
		return sk
	}
	front = strings.TrimPrefix(front, "---")
	sk.Body = strings.TrimSpace(body)

	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		val := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		switch key {
		case "name":
			sk.Name = val
		case "description":
			sk.Description = val
		case "triggers", "tags", "keywords":
			items := splitList(val)
			if key == "tags" {
				sk.Tags = items
			} else {
				sk.Triggers = append(sk.Triggers, items...)
			}
		}
	}
	return sk
}

func splitList(v string) []string {
	v = strings.ReplaceAll(v, "，", ",")
	v = strings.ReplaceAll(v, "、", ",")
	parts := strings.Split(v, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, "[]")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func contains(list []string, v string) bool {
	return slices.Contains(list, v)
}
