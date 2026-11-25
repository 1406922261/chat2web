// Command admin 审计与迁移 Chat2Web 的项目存储。
//
// 默认只读：输出目录结构、重复数据与迁移计划。
// 传入 -apply 才执行迁移、归档过期临时项目与清理临时文件。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"chat2web/internal/config"
	"chat2web/internal/memory"
	"chat2web/internal/project"
)

func main() {
	apply := flag.Bool("apply", false, "执行迁移与清理（默认仅审计）")
	days := flag.Int("days", 30, "临时项目保留天数，超过则归档到回收站")
	flag.Parse()

	cfg := config.Load()
	projects := project.NewManager(cfg.DataRoot, cfg.ProjectsRoot)
	legacySite := filepath.Join(cfg.Root, "workspace", "generated-websites")
	legacyProjects := filepath.Join(cfg.Root, "workspace", "projects")
	legacyMemory := filepath.Join(cfg.MemoryRoot, "projects")

	fmt.Println("Chat2Web 项目存储审计")
	fmt.Println("数据根：", cfg.DataRoot)
	fmt.Println("项目根：", cfg.ProjectsRoot)
	fmt.Println("记忆根：", cfg.MemoryRoot)
	fmt.Println("会话根：", cfg.SessionsRoot)
	fmt.Println("备份根：", cfg.BackupsRoot)
	fmt.Println()

	audit(cfg, projects, legacySite, legacyProjects, legacyMemory)

	if !*apply {
		fmt.Println()
		fmt.Println("以上为只读审计。执行迁移与清理请加 -apply。")
		return
	}

	fmt.Println()
	fmt.Println("== 执行迁移 ==")
	rep := projects.MigrateLayout(legacySite, legacyProjects, legacyMemory)
	fmt.Printf("项目 %d，源码 %d，记忆 %d，成果 %d，合并元数据 %d，移动文件 %d\n",
		rep.Projects, rep.MovedSource, rep.MovedMemory, rep.CopiedOutput, rep.MergedMetadata, rep.MovedFiles)
	for _, d := range rep.Details {
		fmt.Println("  -", d)
	}
	for _, e := range rep.Errors {
		fmt.Println("  ! 警告：", e)
	}
	if rep.BackupDir != "" {
		fmt.Println("  备份目录：", rep.BackupDir)
	}

	fmt.Println()
	fmt.Println("== 归档过期临时项目 ==")
	if ids, err := projects.PurgeTemporary(*days, false); err != nil {
		fmt.Println("  归档失败：", err)
	} else if len(ids) == 0 {
		fmt.Println("  没有超过", *days, "天未使用的临时项目。")
	} else {
		for _, id := range ids {
			fmt.Println("  已归档：", id, "（可在项目管理中恢复）")
		}
	}

	// 旧回收站 data/projects/.trash → data/backups/trash
	if moved, err := projects.MigrateTrash(); err != nil {
		fmt.Println("  回收站迁移警告：", err)
	} else if moved > 0 {
		fmt.Println("  已迁移回收站项目：", moved)
	}

	mem := memory.NewManager(cfg.MemoryRoot, nil)
	mem.Configure(memory.Options{
		DataRoot:       cfg.DataRoot,
		SessionsRoot:   cfg.SessionsRoot,
		BackupsRoot:    cfg.BackupsRoot,
		RuntimeRoot:    cfg.RuntimeRoot,
		ReportsRoot:    cfg.ReportsRoot,
		TasksRoot:      cfg.TasksRoot,
		ProjectRoot:    cfg.ProjectsRoot,
		PlaywrightDays: cfg.RetentionPlaywrightDays,
		TaskDays:       cfg.RetentionTaskDays,
		SessionDays:    cfg.RetentionSessionDays,
		ReportsKeep:    cfg.ReportsKeep,
		Budget:         cfg.MemoryTokenBudget,
	})
	if err := mem.Init(); err != nil {
		fmt.Println("记忆初始化失败：", err)
		return
	}
	if lr := mem.LastLayoutReport(); !lr.Empty() {
		fmt.Printf("\n== data 目录迁移 ==\n会话摘要 %d，备份 %d，旧摘要 %d，报告 %d，运行数据 %d，任务 %d，冲突 %d\n",
			lr.MovedSessions, lr.MovedBackups, lr.MovedSummaries,
			lr.MovedReports, lr.MovedRuntime, lr.MovedTasks, lr.Conflicts)
		for _, d := range lr.Details {
			fmt.Println("  -", d)
		}
		for _, e := range lr.Errors {
			fmt.Println("  ! 警告：", e)
		}
		for _, d := range lr.RemovedDirs {
			fmt.Println("  已清理空目录：", d)
		}
	}
	if cleanup := mem.CleanupMemory(); cleanup.DeletedFiles > 0 || cleanup.ArchivedFiles > 0 {
		fmt.Printf("\n== 清理 ==\n临时文件 %d 个，归档 %d 个\n", cleanup.DeletedFiles, cleanup.ArchivedFiles)
	}

	fmt.Println()
	fmt.Println("== 迁移后结构 ==")
	audit(cfg, projects, legacySite, legacyProjects, legacyMemory)
}

func audit(cfg *config.Config, projects *project.Manager, legacySite, legacyProjects, legacyMemory string) {
	active := projects.List(false)
	deleted := projects.List(true)
	recycled := 0
	for _, p := range deleted {
		if p.Deleted {
			recycled++
		}
	}

	duplicated := 0
	for _, p := range active {
		if exists(filepath.Join(projects.ProjectsRoot(), p.ID+".json")) {
			duplicated++
		}
	}

	fmt.Printf("当前项目：%d 个（回收站 %d 个，位于 %s）\n", len(active), recycled, projects.TrashRoot())
	for _, p := range active {
		fmt.Printf("  - %-42s [%s] %s\n", p.ID, project.LifecycleLabel(p.Lifecycle), p.Name)
	}

	fmt.Println("旧布局残留：")
	fmt.Printf("  data/projects/*.json        %d\n", countGlob(filepath.Join(projects.ProjectsRoot(), "*.json")))
	fmt.Printf("  data/projects/.trash        %d\n", countTree(filepath.Join(projects.ProjectsRoot(), ".trash")))
	fmt.Printf("  workspace/projects          %d\n", countDirs(legacyProjects))
	fmt.Printf("  workspace/generated-websites %d\n", countDirs(legacySite))
	fmt.Printf("  data/memory/projects        %d\n", countMD(legacyMemory))
	fmt.Printf("  data/memory/sessions        %d\n", countTree(filepath.Join(cfg.MemoryRoot, "sessions")))
	fmt.Printf("  data/memory/backup          %d\n", countTree(filepath.Join(cfg.MemoryRoot, "backup")))
	fmt.Printf("  data/memory/summaries       %d\n", countTree(filepath.Join(cfg.MemoryRoot, "summaries")))
	fmt.Printf("  data/reports, tasks, runtime %d\n",
		countTree(filepath.Join(cfg.DataRoot, "reports"))+
			countTree(filepath.Join(cfg.DataRoot, "tasks"))+
			countTree(filepath.Join(cfg.DataRoot, "runtime")))
	if duplicated > 0 {
		fmt.Printf("  ! 重复元数据：%d 个项目同时存在项目内 project.json 与旧索引 JSON\n", duplicated)
	}

	orphanMemory := 0
	if entries, err := os.ReadDir(legacyMemory); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				id := strings.TrimSuffix(e.Name(), ".md")
				if _, err := os.Stat(filepath.Join(projects.ProjectsRoot(), id)); err != nil {
					orphanMemory++
				}
			}
		}
	}
	if orphanMemory > 0 {
		fmt.Printf("  ! 无归属的旧项目记忆：%d 个（迁移时保留，不自动删除）\n", orphanMemory)
	}
}

func countTree(root string) int {
	n := 0
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func countGlob(pattern string) int {
	files, _ := filepath.Glob(pattern)
	return len(files)
}

func countDirs(root string) int {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			n++
		}
	}
	return n
}

func countMD(root string) int {
	files, _ := filepath.Glob(filepath.Join(root, "*.md"))
	return len(files)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
