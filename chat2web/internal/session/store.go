// Package session 管理会话摘要文件的落盘位置与读写。
//
// 会话只在当前进程内保留原始对话；落盘的只有按月归档的摘要文件：
//
//	data/sessions/YYYY-MM/{session_id}.json
package session

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Store 是 data/sessions 的轻量文件存储，不负责记忆内容本身。
type Store struct {
	root string
}

func NewStore(root string) *Store { return &Store{root: root} }

func (s *Store) Root() string { return s.root }

// DigestPath 返回会话摘要文件路径，按月归档。
func (s *Store) DigestPath(sessionID string, at time.Time) string {
	return filepath.Join(monthDir(s.root, at), digestName(sessionID))
}

// List 返回所有已归档的会话摘要文件；JSON 优先，同时兼容旧的 Markdown 摘要以便迁移。
func (s *Store) List() []string {
	return scanDigests(s.root)
}

// Read 读取摘要文件内容。
func (s *Store) Read(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Write 原子写入摘要文件。
func (s *Store) Write(path, content string) error {
	return atomicWrite(path, content)
}

// SafeID 把会话 ID 转换为安全文件名。
func SafeID(id string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
	out := re.ReplaceAllString(strings.TrimSpace(id), "_")
	out = strings.Trim(out, "_.")
	if out == "" {
		out = "session-" + time.Now().Format("20060102150405")
	}
	return out
}
