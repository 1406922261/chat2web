package session

import (
	"os"
	"path/filepath"
)

// scanDigests 只扫描按月归档（YYYY-MM）的目录，返回 JSON 与旧的 Markdown 摘要文件，
// 避免把 data/sessions/tasks 等非摘要 JSON 当作会话摘要。
func scanDigests(root string) []string {
	files, _ := filepath.Glob(filepath.Join(root, monthPattern, "*"+digestExt))
	legacy, _ := filepath.Glob(filepath.Join(root, monthPattern, "*"+legacyDigestExt))
	return append(files, legacy...)
}

// atomicWrite 先写临时文件再重命名，避免中断写入产生半截内容。
func atomicWrite(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
