package memory

import (
	"fmt"
	"strings"
	"time"
)

// MemoryPatch 是更新记忆时的可选字段。
type MemoryPatch struct {
	Key   string
	Value string
	Tags  []string
}

// SaveMemory 保存一条长期记忆；相同 kind/key/value 会自动合并，避免重复。
func (m *Manager) SaveMemory(e Entry) (Entry, error) {
	e.Key = strings.TrimSpace(e.Key)
	e.Value = strings.TrimSpace(e.Value)
	if e.Kind == "" {
		e.Kind = "fact"
	}
	if e.Key == "" || e.Value == "" {
		return Entry{}, fmt.Errorf("记忆的 key 和 value 不能为空")
	}
	m.mu.Lock()
	m.upsertLocked(e)
	var saved Entry
	for _, cur := range m.allEntriesLocked() {
		if cur.Kind == e.Kind && cur.ProjectID == e.ProjectID && cur.Key == e.Key {
			saved = cur
			break
		}
	}
	m.mu.Unlock()
	if saved.ID == "" {
		return Entry{}, fmt.Errorf("保存记忆失败")
	}
	return saved, nil
}

// UpdateMemory 按 ID 更新用户长期记忆。
func (m *Manager) UpdateMemory(id string, patch MemoryPatch) error {
	id = strings.TrimPrefix(strings.TrimSpace(id), "entry:")
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.entries {
		if m.entries[i].ID != id {
			continue
		}
		if key := strings.TrimSpace(patch.Key); key != "" {
			m.entries[i].Key = key
		}
		if value := strings.TrimSpace(patch.Value); value != "" {
			m.entries[i].Value = value
		}
		if patch.Tags != nil {
			m.entries[i].Tags = dedupe(patch.Tags)
		}
		m.entries[i].Source = "manual"
		m.entries[i].UpdatedAt = time.Now()
		m.persistUserLocked()
		m.refreshIndexLocked()
		m.persistIndexLocked()
		return nil
	}
	return fmt.Errorf("记忆条目不存在")
}

// DeleteMemory 删除指定用户长期记忆。
func (m *Manager) DeleteMemory(id string) error {
	id = strings.TrimPrefix(strings.TrimSpace(id), "entry:")
	return m.DeleteEntry(id)
}

// GetProjectMemory 获取项目记忆；不存在时返回错误，便于界面提示。
func (m *Manager) GetProjectMemory(projectID string) (*ProjectMemory, error) {
	pm := m.ProjectMemory(projectID)
	if pm == nil {
		return nil, fmt.Errorf("项目记忆不存在：%s", projectID)
	}
	return pm, nil
}
