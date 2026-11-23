// Package task 持久化单次任务的目标与步骤状态，避免重复执行已成功的步骤。
package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Step struct {
	Name string `json:"name"`
	Done bool   `json:"done"`
	Note string `json:"note,omitempty"`
}

type State struct {
	ProjectID string    `json:"project_id"`
	Goal      string    `json:"goal"`
	Status    string    `json:"status"`
	Steps     []Step    `json:"steps"`
	UpdatedAt time.Time `json:"updated_at"`
}

func path(dir, id string) string {
	return filepath.Join(dir, id+".json")
}

// Load 读取任务状态；不存在时返回 nil, nil。
func Load(dir, id string) (*State, error) {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(id) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path(dir, id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Save 原子写入任务状态。
func Save(dir string, s *State) error {
	if s == nil || strings.TrimSpace(dir) == "" || strings.TrimSpace(s.ProjectID) == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	s.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	p := path(dir, s.ProjectID)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// SetStep 更新步骤状态（不存在则追加）。
func (s *State) SetStep(name string, done bool, note string) {
	if s == nil {
		return
	}
	for i := range s.Steps {
		if s.Steps[i].Name == name {
			s.Steps[i].Done = done
			if note != "" {
				s.Steps[i].Note = note
			}
			return
		}
	}
	s.Steps = append(s.Steps, Step{Name: name, Done: done, Note: note})
}

// Text 输出可注入提示词的执行状态。
func (s *State) Text() string {
	if s == nil || len(s.Steps) == 0 {
		return ""
	}
	var b strings.Builder
	for _, st := range s.Steps {
		mark := " "
		if st.Done {
			mark = "x"
		}
		fmt.Fprintf(&b, "- [%s] %s", mark, st.Name)
		if st.Note != "" {
			fmt.Fprintf(&b, "：%s", st.Note)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
