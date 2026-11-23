package project

import (
	"archive/zip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type HistoryItem struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Summary string    `json:"summary"`
}

// 项目生命周期：
//   - temporary：临时生成，默认状态，不进入正式项目列表
//   - active：用户正在继续修改
//   - saved：用户明确保存，永久保留
//   - archived：用户主动归档，保留源码与记忆但不出现在活动列表
const (
	LifecycleTemporary = "temporary"
	LifecycleActive    = "active"
	LifecycleSaved     = "saved"
	LifecycleArchived  = "archived"
	LifecycleDeleted   = "deleted"
)

type Project struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	TechStack     []string      `json:"tech_stack"`
	Status        string        `json:"status"`
	Pages         []string      `json:"pages"`
	DevURL        string        `json:"dev_url"`
	DevPort       int           `json:"dev_port"`
	SessionID     string        `json:"session_id"`
	Lifecycle     string        `json:"lifecycle"`
	PrevLifecycle string        `json:"prev_lifecycle,omitempty"`
	SavedAt       *time.Time    `json:"saved_at,omitempty"`
	ArchivedAt    *time.Time    `json:"archived_at,omitempty"`
	LastOpenedAt  *time.Time    `json:"last_opened_at,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	History       []HistoryItem `json:"history"`
	Deleted       bool          `json:"deleted"`
	Dir           string        `json:"dir"`
}

type FileNode struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	IsDir    bool       `json:"is_dir"`
	Size     int64      `json:"size"`
	Children []FileNode `json:"children,omitempty"`
}

type Version struct {
	File      string    `json:"file"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
	Size      int64     `json:"size"`
}

type Manager struct {
	dataRoot      string
	workspaceRoot string
	trashRoot     string
	exportRoot    string
	mu            sync.RWMutex
	cache         map[string]*Project
}

func NewManager(dataRoot, workspaceRoot string) *Manager {
	m := &Manager{
		dataRoot:      dataRoot,
		workspaceRoot: workspaceRoot,
		trashRoot:     filepath.Join(filepath.Dir(workspaceRoot), ".trash"),
		exportRoot:    filepath.Join(dataRoot, "exports"),
		cache:         map[string]*Project{},
	}
	_ = os.MkdirAll(filepath.Join(dataRoot, "projects"), 0o755)
	_ = os.MkdirAll(workspaceRoot, 0o755)
	_ = os.MkdirAll(m.trashRoot, 0o755)
	_ = os.MkdirAll(m.exportRoot, 0o755)
	return m
}

func (m *Manager) WorkspaceRoot() string { return m.workspaceRoot }

func (m *Manager) newID() string {
	buf := make([]byte, 3)
	_, _ = rand.Read(buf)
	return "project-" + time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(buf)
}

func (m *Manager) metaPath(id string) string {
	return filepath.Join(m.dataRoot, "projects", id+".json")
}

// ProjectDir 返回项目根目录，统一包含 project.json / memory.md / source / assets / output。
func (m *Manager) ProjectDir(id string) string {
	return filepath.Join(m.workspaceRoot, id)
}

// SiteDir 返回项目的网站源代码目录（source 子目录）。
func (m *Manager) SiteDir(id string) string {
	return filepath.Join(m.ProjectDir(id), "source")
}

// AssetsDir 返回用户素材目录，清理时永不删除。
func (m *Manager) AssetsDir(id string) string {
	return filepath.Join(m.ProjectDir(id), "assets")
}

// OutputDir 返回最终生成成果目录。
func (m *Manager) OutputDir(id string) string {
	return filepath.Join(m.ProjectDir(id), "output")
}

// MemoryFile 返回项目记忆文件（项目内 memory.md）。
func (m *Manager) MemoryFile(id string) string {
	return filepath.Join(m.ProjectDir(id), "memory.md")
}

// LegacySiteDir 返回第一轮改造前的网站目录位置，仅用于迁移。
func (m *Manager) LegacySiteDir(id string) string {
	return filepath.Join(filepath.Dir(m.workspaceRoot), "generated-websites", id)
}

func (m *Manager) ensureLayout(id string) error {
	for _, dir := range []string{m.ProjectDir(id), m.SiteDir(id), m.AssetsDir(id), m.OutputDir(id)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) Create(name, description string) (*Project, error) {
	if strings.TrimSpace(name) == "" {
		name = "未命名网站项目"
	}
	id := m.newID()
	now := time.Now()
	p := &Project{
		ID:          id,
		Name:        strings.TrimSpace(name),
		Description: strings.TrimSpace(description),
		Status:      "draft",
		SessionID:   "session-" + id,
		Lifecycle:   LifecycleTemporary,
		CreatedAt:   now,
		UpdatedAt:   now,
		Dir:         m.SiteDir(id),
	}
	if err := m.ensureLayout(id); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.cache[id] = p
	m.mu.Unlock()
	if err := m.save(p); err != nil {
		return nil, err
	}
	return p, nil
}

// normalize 兼容旧数据：补齐生命周期与目录字段。
func (m *Manager) normalize(p *Project) {
	p.Dir = m.SiteDir(p.ID)
	if p.Lifecycle == "" {
		if p.Deleted {
			p.Lifecycle = LifecycleDeleted
		} else {
			// 改造前已存在的项目按用户资产处理，避免被当作临时项目清理。
			p.Lifecycle = LifecycleSaved
		}
	}
	if p.Deleted {
		p.Lifecycle = LifecycleDeleted
	}
}

func (m *Manager) save(p *Project) error {
	p.UpdatedAt = time.Now()
	p.Dir = m.SiteDir(p.ID)
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(m.metaPath(p.ID), data, 0o644); err != nil {
		return err
	}
	// 项目目录内同步一份 project.json，保证目录自包含；已删除项目目录在回收站，不重建。
	if info, statErr := os.Stat(m.ProjectDir(p.ID)); statErr == nil && info.IsDir() {
		_ = os.WriteFile(filepath.Join(m.ProjectDir(p.ID), "project.json"), data, 0o644)
	}
	return nil
}

func (m *Manager) Update(p *Project) error {
	m.mu.Lock()
	m.cache[p.ID] = p
	m.mu.Unlock()
	return m.save(p)
}

func (m *Manager) UpdateFunc(id string, fn func(*Project)) (*Project, error) {
	p, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	fn(p)
	if err := m.Update(p); err != nil {
		return nil, err
	}
	return p, nil
}

func (m *Manager) Get(id string) (*Project, error) {
	m.mu.RLock()
	if p, ok := m.cache[id]; ok {
		m.mu.RUnlock()
		return p, nil
	}
	m.mu.RUnlock()

	data, err := os.ReadFile(m.metaPath(id))
	if err != nil {
		return nil, errors.New("项目不存在")
	}
	var p Project
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	m.normalize(&p)
	m.mu.Lock()
	m.cache[id] = &p
	m.mu.Unlock()
	return &p, nil
}

func (m *Manager) List(includeDeleted bool) []*Project {
	files, _ := filepath.Glob(filepath.Join(m.dataRoot, "projects", "*.json"))
	out := make([]*Project, 0, len(files))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var p Project
		if err := json.Unmarshal(data, &p); err != nil {
			continue
		}
		if p.Deleted && !includeDeleted {
			continue
		}
		m.normalize(&p)
		out = append(out, &p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// Delete 把整个项目目录（含源码、记忆、素材与成果）移入回收站，并标记删除。
func (m *Manager) Delete(id string) error {
	p, err := m.Get(id)
	if err != nil {
		return err
	}
	target := filepath.Join(m.trashRoot, id+"-"+time.Now().Format("20060102150405"))
	if info, statErr := os.Stat(m.ProjectDir(id)); statErr == nil && info.IsDir() {
		if err := moveDir(m.ProjectDir(id), target); err != nil {
			return fmt.Errorf("移动项目目录到回收站失败: %w", err)
		}
	}
	if p.Lifecycle != "" && p.Lifecycle != LifecycleDeleted {
		p.PrevLifecycle = p.Lifecycle
	}
	p.Deleted = true
	p.Lifecycle = LifecycleDeleted
	p.Status = "deleted"
	p.DevURL = ""
	p.History = append(p.History, HistoryItem{Time: time.Now(), Kind: "删除", Summary: "项目已移入回收站，可恢复"})
	return m.Update(p)
}

// Restore 从回收站恢复整个项目目录。
func (m *Manager) Restore(id string) (*Project, error) {
	p, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	matches, _ := filepath.Glob(filepath.Join(m.trashRoot, id+"-*"))
	if len(matches) > 0 {
		sort.Strings(matches)
		src := matches[len(matches)-1]
		if err := moveDir(src, m.ProjectDir(id)); err != nil {
			return nil, fmt.Errorf("从回收站恢复失败: %w", err)
		}
	}
	if err := m.ensureLayout(id); err != nil {
		return nil, err
	}
	m.normalizeLegacySource(id)
	p.Deleted = false
	p.Lifecycle = p.PrevLifecycle
	if p.Lifecycle == "" || p.Lifecycle == LifecycleDeleted {
		p.Lifecycle = LifecycleSaved
	}
	p.PrevLifecycle = ""
	if p.Status == "deleted" {
		p.Status = "generated"
	}
	p.History = append(p.History, HistoryItem{Time: time.Now(), Kind: "恢复", Summary: "项目已从回收站恢复"})
	return p, m.Update(p)
}

// normalizeLegacySource 兼容旧回收站结构：源码直接位于项目目录时移入 source/。
func (m *Manager) normalizeLegacySource(id string) {
	root := m.ProjectDir(id)
	if _, err := os.Stat(filepath.Join(root, "package.json")); err != nil {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		switch e.Name() {
		case "source", "assets", "output", "memory.md", "project.json":
			continue
		}
		_ = os.Rename(filepath.Join(root, e.Name()), filepath.Join(m.SiteDir(id), e.Name()))
	}
}

func (m *Manager) Rename(id, name string) (*Project, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("项目名称不能为空")
	}
	return m.UpdateFunc(id, func(p *Project) { p.Name = strings.TrimSpace(name) })
}

// Tree 返回项目文件树（跳过依赖与构建目录）。
func (m *Manager) Tree(id string, maxDepth int) (FileNode, error) {
	root := m.SiteDir(id)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return FileNode{Name: filepath.Base(root), Path: "", IsDir: true}, nil
	}
	if maxDepth <= 0 {
		maxDepth = 6
	}
	return walkNode(root, "", maxDepth)
}

func walkNode(abs, rel string, depth int) (FileNode, error) {
	info, err := os.Stat(abs)
	if err != nil {
		return FileNode{}, err
	}
	node := FileNode{Name: filepath.Base(abs), Path: rel, IsDir: info.IsDir(), Size: info.Size()}
	if !info.IsDir() || depth <= 0 {
		return node, nil
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return node, nil
	}
	for _, e := range entries {
		name := e.Name()
		if skipDir(name) {
			continue
		}
		childRel := name
		if rel != "" {
			childRel = rel + "/" + name
		}
		child, err := walkNode(filepath.Join(abs, name), childRel, depth-1)
		if err != nil {
			continue
		}
		node.Children = append(node.Children, child)
	}
	sort.Slice(node.Children, func(i, j int) bool {
		a, b := node.Children[i], node.Children[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return a.Name < b.Name
	})
	return node, nil
}

func skipDir(name string) bool {
	switch name {
	case "node_modules", ".git", "dist", "build", ".vite", ".next", ".cache":
		return true
	}
	return false
}

func (m *Manager) ReadFile(id, rel string) (string, error) {
	p, err := m.safePath(id, rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if len(data) > 400_000 {
		return string(data[:400_000]) + "\n...(文件过大，已截断)", nil
	}
	return string(data), nil
}

func (m *Manager) WriteFile(id, rel, content string) error {
	p, err := m.safePath(id, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

func (m *Manager) safePath(id, rel string) (string, error) {
	root := m.SiteDir(id)
	rel = strings.TrimPrefix(strings.ReplaceAll(rel, "\\", "/"), "/")
	full := filepath.Join(root, rel)
	absRoot, _ := filepath.Abs(root)
	absFull, _ := filepath.Abs(full)
	r, err := filepath.Rel(absRoot, absFull)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", errors.New("路径超出项目目录范围")
	}
	return absFull, nil
}

func (m *Manager) AddHistory(id, kind, summary string) {
	_, _ = m.UpdateFunc(id, func(p *Project) {
		p.History = append(p.History, HistoryItem{Time: time.Now(), Kind: kind, Summary: summary})
		if len(p.History) > 60 {
			p.History = p.History[len(p.History)-60:]
		}
	})
}

func (m *Manager) Snapshot(id, label string) (Version, error) {
	src := m.SiteDir(id)
	dir := filepath.Join(m.dataRoot, "projects", id, "versions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Version{}, err
	}
	name := time.Now().Format("20060102-150405") + ".zip"
	file := filepath.Join(dir, name)
	if err := zipDir(src, file); err != nil {
		return Version{}, err
	}
	info, _ := os.Stat(file)
	var size int64
	if info != nil {
		size = info.Size()
	}
	v := Version{File: filepath.ToSlash(filepath.Join("versions", name)), Label: label, CreatedAt: time.Now(), Size: size}
	b, _ := json.Marshal(v)
	_ = os.WriteFile(filepath.Join(dir, "index-"+name+".json"), b, 0o644)
	return v, nil
}

func (m *Manager) Versions(id string) []Version {
	dir := filepath.Join(m.dataRoot, "projects", id, "versions")
	files, _ := filepath.Glob(filepath.Join(dir, "index-*.json"))
	var out []Version
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var v Version
		if json.Unmarshal(data, &v) == nil {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (m *Manager) Export(id string) (string, error) {
	p, err := m.Get(id)
	if err != nil {
		return "", err
	}
	_ = os.MkdirAll(m.exportRoot, 0o755)
	target := filepath.Join(m.exportRoot, sanitizeFileName(p.Name)+"-"+time.Now().Format("20060102-150405")+".zip")
	if err := zipDir(m.SiteDir(id), target); err != nil {
		return "", err
	}
	return target, nil
}

func zipDir(src, target string) error {
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()

	err = filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if path != src && skipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return nil
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return nil
		}
		header.Name = filepath.ToSlash(rel)
		header.Method = zip.Deflate
		w, err := zw.CreateHeader(header)
		if err != nil {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer in.Close()
		_, _ = io.Copy(w, in)
		return nil
	})
	return err
}

func sanitizeFileName(s string) string {
	if strings.TrimSpace(s) == "" {
		return "project"
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-", "?", "-", "\"", "-", "<", "-", ">", "-", "|", "-")
	return strings.TrimSpace(replacer.Replace(s))
}
