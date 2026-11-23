// Command admin 执行一次性的历史数据盘点清理与项目目录迁移。
// 所有删除动作都是"移动到备份目录"，可通过 data/memory/backup/cleanup-* 恢复。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"chat2web/internal/config"
	"chat2web/internal/memory"
	"chat2web/internal/project"
)

// 保留 9 个有价值的项目（用户确认）。
var keepProjects = []string{
	"project-20261121-082357-1770b9", // 杭州三日
	"project-20261121-084838-86003a", // 哑铃30天
	"project-20261121-114700-0d4505", // 年味春节
	"project-20261121-205540-0ff459", // 冬日衣橱
	"project-20261122-111553-d3136f", // 夏日衣橱
	"project-20261122-094954-a2de17", // 早餐决策手册
	"project-20261122-113451-47b5fb", // 晨光早餐最终版
	"project-20261122-163211-b90bac", // 冬藏养生
	"project-20261122-171207-6769fd", // 涮羊肉
}

// 归档 7 个早期/重复/失败项目。
var archiveProjects = []string{
	"project-20261122-091523-866ee8",
	"project-20261122-104232-ab8175",
	"project-20261121-213657-ff5fcf",
	"project-20261121-113025-544eb9",
	"project-20261121-113647-5256cb",
	"project-20261121-191335-ac2d43",
	"project-20261121-211430-cdd313",
}

// 清理 13 个空/占位/失败脚手架/测试项目。
var removeProjects = []string{
	"project-20261122-104602-8d4023",
	"project-20261122-112404-c80ae3",
	"project-20261122-162730-75c7fe",
	"project-20261122-162827-ef0c0a",
	"project-20261121-212015-0adb3d",
	"project-20261121-212458-95c4ac",
	"project-20261121-213503-a4ae50",
	"project-20261122-103056-7f367c",
	"project-20261122-103433-2ccd14",
	"project-20261122-103813-0b97f3",
	"project-20261122-104822-07b9a2",
	"project-20261122-105132-5d1fd9",
	"project-20261122-105435-c2fb2c",
}

// 项目专属记忆迁入对应项目，用户级记忆只保留跨项目内容。
var entryMoves = map[string]string{
	"m2026112108540695c3910d": "project-20261121-084838-86003a",
	"m2026112108540668056d88": "project-20261121-084838-86003a",
	"m202611210854063f8974f7": "project-20261121-084838-86003a",
	"m20261121085406a1a17da1": "project-20261121-084838-86003a",
	"m202611210854069c8e0a2a": "project-20261121-084838-86003a",
	"m20261121085406ea6a06f6": "project-20261121-084838-86003a",
	"m20261121205731c9f34508": "project-20261121-205540-0ff459",
	"m20261121205731fe7dea72": "project-20261121-205540-0ff459",
	"m20261121205731bf215c77": "project-20261121-205540-0ff459",
	"m2026112209214344d28fa6": "project-20261122-094954-a2de17",
	"m202611221137391309a5db": "project-20261122-113451-47b5fb",
	"m20261122113739d23c0e04": "project-20261122-113451-47b5fb",
	"m20261122114749a415dc7a": "project-20261121-113647-5256cb",
	"m202611221621112f11a75c": "project-20261122-111553-d3136f",
	"m20261122174709eed8134e": "project-20261122-113451-47b5fb",
}

type admin struct {
	cfg      *config.Config
	projects *project.Manager
	backup   string
	apply    bool
	actions  []string
}

func main() {
	apply := flag.Bool("apply", false, "真正执行清理；缺省仅输出计划")
	flag.Parse()

	cfg := config.Load()
	ts := time.Now().Format("20060102-150405")
	a := &admin{
		cfg:      cfg,
		projects: project.NewManager(cfg.DataRoot, cfg.ProjectsRoot),
		backup:   filepath.Join(cfg.MemoryRoot, "backup", "cleanup-"+ts),
		apply:    *apply,
	}

	fmt.Printf("== Chat2Web 历史数据清理（%s）==\n", map[bool]string{true: "执行模式", false: "预演模式"}[*apply])
	fmt.Printf("备份目录：%s\n\n", a.backup)

	a.removeUseless()
	a.migrateLayout()
	a.applyLifecycle()
	a.archiveReportsAndTasks()
	a.archiveRootDebug()
	a.cleanMemory()
	a.verify()

	fmt.Println()
	fmt.Printf("完成：%d 项操作\n", len(a.actions))
	for _, act := range a.actions {
		fmt.Println(" -", act)
	}
}

func (a *admin) act(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	a.actions = append(a.actions, msg)
	fmt.Println("[操作]", msg)
}

func (a *admin) plan(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println("[计划]", msg)
}

func (a *admin) backupPath(category, name string) string {
	return filepath.Join(a.backup, category, name)
}

// removeUseless 把 13 个无价值项目的全部关联数据移入备份目录。
func (a *admin) removeUseless() {
	fmt.Println("\n-- 1. 清理无价值项目 --")
	for _, id := range removeProjects {
		dirs := []string{
			a.projects.ProjectDir(id),
			filepath.Join(a.cfg.Root, "workspace", "generated-websites", id),
		}
		movedSite := false
		for _, dir := range dirs {
			if !exists(dir) {
				continue
			}
			target := a.backupPath("sites", id)
			if !a.apply {
				a.plan("移动项目目录 %s -> %s", dir, target)
				movedSite = true
				continue
			}
			if err := move(dir, target); err != nil {
				a.act("移动 %s 失败：%v", dir, err)
			} else {
				a.act("项目目录已备份：%s", id)
				movedSite = true
			}
		}
		if !movedSite {
			a.plan("项目 %s 没有可迁移的目录（空项目）", id)
		}
		a.backupFile(filepath.Join(a.cfg.DataRoot, "projects", id+".json"), "projects")
		a.backupFile(filepath.Join(a.cfg.MemoryRoot, "projects", id+".md"), "memory")
		a.backupFile(filepath.Join(a.cfg.ReportsRoot, id+".json"), "reports")
		a.backupFile(filepath.Join(a.cfg.TasksRoot, id+".json"), "tasks")
		a.backupSessions(id)
	}
}

func (a *admin) backupFile(src, category string) {
	if !exists(src) {
		return
	}
	target := a.backupPath(category, filepath.Base(src))
	if !a.apply {
		a.plan("备份文件 %s -> %s", src, target)
		return
	}
	if err := move(src, target); err != nil {
		a.act("备份 %s 失败：%v", src, err)
	}
}

func (a *admin) backupSessions(projectID string) {
	files, _ := filepath.Glob(filepath.Join(a.cfg.MemoryRoot, "sessions", "*", "*"+projectID+"*.md"))
	for _, f := range files {
		target := a.backupPath("sessions", filepath.Base(f))
		if !a.apply {
			a.plan("备份会话摘要 %s", f)
			continue
		}
		if err := move(f, target); err != nil {
			a.act("备份会话 %s 失败：%v", f, err)
		}
	}
}

// migrateLayout 把剩余项目迁移到统一目录结构。
func (a *admin) migrateLayout() {
	fmt.Println("\n-- 2. 统一项目目录结构 --")
	if !a.apply {
		a.plan("迁移 workspace/generated-websites/{id} -> workspace/projects/{id}/{source,assets,output}")
		a.plan("把 memory/projects/{id}.md 移入项目目录 memory.md")
		return
	}
	rep := a.projects.MigrateLayout(
		filepath.Join(a.cfg.Root, "workspace", "generated-websites"),
		filepath.Join(a.cfg.MemoryRoot, "projects"),
	)
	a.act("目录迁移：项目 %d，源码 %d，记忆 %d，成果 %d", rep.Projects, rep.MovedSource, rep.MovedMemory, rep.CopiedOutput)
	for _, e := range rep.Errors {
		a.act("迁移告警：%s", e)
	}
}

// applyLifecycle 保存保留项目、归档重复项目、纠正项目名称。
func (a *admin) applyLifecycle() {
	fmt.Println("\n-- 3. 生命周期标记 --")
	if !a.apply {
		a.plan("保留 %d 个项目标记为 saved，归档 %d 个项目", len(keepProjects), len(archiveProjects))
		return
	}
	for _, id := range keepProjects {
		if _, err := a.projects.Get(id); err != nil {
			a.act("保留项目缺失：%s", id)
			continue
		}
		if _, err := a.projects.Save(id); err == nil {
			a.act("已保存：%s", id)
		}
	}
	for _, id := range archiveProjects {
		if _, err := a.projects.Get(id); err != nil {
			continue
		}
		if _, err := a.projects.Archive(id); err == nil {
			a.act("已归档：%s", id)
		}
	}
	// 项目名称与最终网页主题保持一致
	if _, err := a.projects.UpdateFunc("project-20261122-171207-6769fd", func(p *project.Project) {
		p.Name = "北京涮羊肉攻略指南"
	}); err == nil {
		a.act("更新项目名称：北京涮羊肉攻略指南")
	}
}

// archiveReportsAndTasks 只保留被保留项目引用的报告与任务，重复项移入备份。
func (a *admin) archiveReportsAndTasks() {
	fmt.Println("\n-- 4. 报告与任务 --")
	valid := map[string]bool{}
	for _, id := range append(append([]string{}, keepProjects...), archiveProjects...) {
		valid[id] = true
	}
	for _, dir := range []struct {
		path string
		cat  string
	}{{a.cfg.ReportsRoot, "reports"}, {a.cfg.TasksRoot, "tasks"}} {
		files, _ := filepath.Glob(filepath.Join(dir.path, "*.json"))
		sort.Strings(files)
		for _, f := range files {
			id := strings.TrimSuffix(filepath.Base(f), ".json")
			if valid[id] {
				continue
			}
			if !a.apply {
				a.plan("归档孤立%s：%s", map[string]string{"reports": "报告", "tasks": "任务"}[dir.cat], filepath.Base(f))
				continue
			}
			if err := move(f, a.backupPath(dir.cat, filepath.Base(f))); err == nil {
				a.act("已归档孤立%s：%s", map[string]string{"reports": "报告", "tasks": "任务"}[dir.cat], filepath.Base(f))
			}
		}
	}
	// 每个项目只保留最新一份报告（重名不存在时无需处理），总量上限交给自动清理。
	if a.apply {
		kept, removed := a.dedupeReports(a.cfg.ReportsRoot, 10)
		if removed > 0 {
			a.act("报告去重：保留 %d 份，备份 %d 份", kept, removed)
		}
	}
}

// archiveRootDebug 把调试截图与测试日志移入备份，保留 README 与测试报告文档。
func (a *admin) archiveRootDebug() {
	fmt.Println("\n-- 5. 根目录调试产物 --")
	patterns := []string{"home.png", "schedule.png", "open_test*.log", "open_test*.txt", "e2e_*.log", "e2e_*.txt"}
	for _, pattern := range patterns {
		files, _ := filepath.Glob(filepath.Join(a.cfg.Root, pattern))
		for _, f := range files {
			if !a.apply {
				a.plan("归档调试文件 %s", filepath.Base(f))
				continue
			}
			if err := move(f, a.backupPath("root-debug", filepath.Base(f))); err == nil {
				a.act("已归档调试文件：%s", filepath.Base(f))
			}
		}
	}
	for _, dir := range []string{".screenshots", ".tmp"} {
		path := filepath.Join(a.cfg.Root, dir)
		if !exists(path) {
			continue
		}
		if !a.apply {
			a.plan("归档临时目录 %s", dir)
			continue
		}
		if err := move(path, a.backupPath("root-debug", dir)); err == nil {
			a.act("已归档临时目录：%s", dir)
		}
	}
}

// cleanMemory 迁移项目专属记忆、合并重复会话摘要、重建索引。
func (a *admin) cleanMemory() {
	fmt.Println("\n-- 6. 长期记忆整理 --")
	mem := memory.NewManager(a.cfg.MemoryRoot, nil)
	mem.Configure(memory.Options{
		RuntimeRoot: a.cfg.RuntimeRoot,
		ReportsRoot: a.cfg.ReportsRoot,
		TasksRoot:   a.cfg.TasksRoot,
		ProjectRoot: a.cfg.ProjectsRoot,
	})
	if !a.apply {
		a.plan("把 %d 条项目专属用户记忆迁入对应项目", len(entryMoves))
		a.plan("合并重复会话摘要，按项目保留 1 份")
		a.plan("重建 index.json（过滤停用词，限制关键词数量）")
		return
	}
	if err := mem.Init(); err != nil {
		a.act("初始化记忆失败：%v", err)
		return
	}
	ids := make([]string, 0, len(entryMoves))
	for id := range entryMoves {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	moved := 0
	for _, id := range ids {
		if err := mem.MoveEntryToProject(id, entryMoves[id]); err == nil {
			moved++
		}
	}
	a.act("迁移项目专属记忆：%d 条", moved)
	if n, err := mem.ConsolidateSessions(); err != nil {
		a.act("合并会话摘要失败：%v", err)
	} else {
		a.act("合并重复会话摘要：%d 份（旧文件已备份）", n)
	}
	mem.RebuildIndex()
	a.act("记忆索引已重建")
	if rep := mem.CleanupMemory(); rep.DeletedFiles > 0 || rep.ArchivedFiles > 0 {
		a.act("自动清理：删除临时文件 %d 个，归档报告 %d 份", rep.DeletedFiles, rep.ArchivedFiles)
	}
}

// verify 输出清理后的项目分布与索引大小，便于验收。
func (a *admin) verify() {
	fmt.Println("\n-- 7. 验收检查 --")
	fmt.Printf("正式项目：%d\n", len(a.projects.FormalProjects()))
	fmt.Printf("临时项目：%d\n", len(a.projects.TemporaryProjects()))
	fmt.Printf("归档项目：%d\n", len(a.projects.ArchivedProjects()))
	fmt.Printf("项目总数（不含回收站）：%d\n", len(a.projects.List(false)))
	if info, err := os.Stat(filepath.Join(a.cfg.MemoryRoot, "index.json")); err == nil {
		fmt.Printf("记忆索引大小：%.1f KB\n", float64(info.Size())/1024)
	}
	files, _ := filepath.Glob(filepath.Join(a.cfg.MemoryRoot, "sessions", "*", "*.md"))
	fmt.Printf("会话摘要文件：%d 份\n", len(files))
	reportFiles, _ := filepath.Glob(filepath.Join(a.cfg.ReportsRoot, "*.json"))
	taskFiles, _ := filepath.Glob(filepath.Join(a.cfg.TasksRoot, "*.json"))
	fmt.Printf("报告：%d 份，任务记录：%d 份\n", len(reportFiles), len(taskFiles))

	// 清理后仍能按关键词检索历史记忆与项目记忆。
	mem := memory.NewManager(a.cfg.MemoryRoot, nil)
	mem.Configure(memory.Options{ProjectRoot: a.cfg.ProjectsRoot})
	if err := mem.Init(); err == nil {
		for _, kw := range []string{"涮羊肉", "早餐", "哑铃"} {
			fmt.Printf("记忆检索「%s」：%d 条\n", kw, len(mem.SearchMemory(kw, 5)))
		}
	}
}

// dedupeReports 限制报告总量，超出部分移入备份目录而不是直接删除。
func (a *admin) dedupeReports(dir string, max int) (int, int) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Slice(files, func(i, j int) bool {
		ii, _ := os.Stat(files[i])
		jj, _ := os.Stat(files[j])
		if ii == nil || jj == nil {
			return false
		}
		return ii.ModTime().After(jj.ModTime())
	})
	removed := 0
	for i, f := range files {
		if i < max {
			continue
		}
		if move(f, a.backupPath("reports", filepath.Base(f))) == nil {
			removed++
		}
	}
	return len(files) - removed, removed
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// move 优先重命名，失败则复制后删除。
func move(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyAll(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func copyAll(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, info.Mode())
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyAll(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
