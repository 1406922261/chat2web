package session

import (
	"path/filepath"
	"time"
)

// 会话摘要按月归档：data/sessions/YYYY-MM/{session_id}.json。
// 早期版本使用 .md，仍会被扫描以便迁移为 JSON。
const (
	digestExt       = ".json"
	legacyDigestExt = ".md"
	monthLayout     = "2006-01"
	monthPattern    = "[0-9][0-9][0-9][0-9]-[0-9][0-9]"
)

// monthDir 返回指定时间的归档月份目录。
func monthDir(root string, at time.Time) string {
	return filepath.Join(root, at.Format(monthLayout))
}

// digestName 返回会话摘要文件名。
func digestName(sessionID string) string {
	return SafeID(sessionID) + digestExt
}
