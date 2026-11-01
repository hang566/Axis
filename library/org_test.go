package library

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFolderUniqueNames(t *testing.T) {
	lib, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()

	a, err := lib.CreateFolder("课程素材", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "课程素材" {
		t.Fatalf("首个文件夹应保持原名，实际 %q", a.Name)
	}
	b, err := lib.CreateFolder("课程素材", "")
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "课程素材 (2)" {
		t.Fatalf("同名文件夹应自动加序号，实际 %q", b.Name)
	}
	child, err := lib.CreateFolder("课程素材", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Name != "课程素材" {
		t.Fatalf("不同父目录下允许同名，实际 %q", child.Name)
	}
	// 重命名成同目录已有名字 → 自动去重
	if err := lib.RenameFolder(child.ID, "课程素材"); err != nil {
		t.Fatal(err)
	}
	var got *Folder
	for _, f := range lib.ListFolders() {
		if f.ID == child.ID {
			got = f
		}
	}
	if got == nil {
		t.Fatal("找不到子文件夹")
	}
	if got.Name != "课程素材" {
		t.Fatalf("子目录重命名不应受根目录同名影响，实际 %q", got.Name)
	}
}

func TestMoveFolderRejectsCycle(t *testing.T) {
	lib, _ := Open(t.TempDir())
	defer lib.Close()
	parent, _ := lib.CreateFolder("父", "")
	child, _ := lib.CreateFolder("子", parent.ID)
	if err := lib.MoveFolder(parent.ID, child.ID); err == nil {
		t.Fatal("应拒绝把父文件夹移动到自己的子文件夹下")
	}
	if err := lib.MoveFolder(parent.ID, parent.ID); err == nil {
		t.Fatal("应拒绝移动到自身")
	}
}

func TestTrashAndRestoreFolder(t *testing.T) {
	lib, _ := Open(t.TempDir())
	defer lib.Close()
	folder, _ := lib.CreateFolder("回收站测试", "")
	p := makeTempProject(t, lib)
	if err := lib.MoveProjects([]string{p.ID}, folder.ID); err != nil {
		t.Fatal(err)
	}

	if err := lib.TrashFolder(folder.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(lib.ListProjects(ProjectListOptions{View: ViewAll})); n != 0 {
		t.Fatalf("回收后普通视图应为空，实际 %d", n)
	}
	if n := len(lib.ListProjects(ProjectListOptions{View: ViewTrash})); n != 1 {
		t.Fatalf("回收站应有 1 个项目，实际 %d", n)
	}
	if len(lib.ListFolders()) != 0 {
		t.Fatal("回收后文件夹不应出现在目录树")
	}
	if len(lib.ListTrashedFolders()) != 1 {
		t.Fatal("回收站应保留文件夹")
	}

	if err := lib.RestoreFolder(folder.ID); err != nil {
		t.Fatal(err)
	}
	restored := lib.ListProjects(ProjectListOptions{View: ViewAll})
	if len(restored) != 1 || restored[0].Deleted {
		t.Fatalf("还原失败：%+v", restored)
	}
	if restored[0].FolderID != folder.ID {
		t.Fatal("还原后应回到原文件夹")
	}
}

func TestCopyProject(t *testing.T) {
	lib, _ := Open(t.TempDir())
	defer lib.Close()
	src := makeTempProject(t, lib)
	// 给项目写一份字幕文件，验证复制会带上目录内容。
	if _, err := lib.SaveSubtitleTrack(src.ID, SubtitleTrack{Format: "srt"}, []byte("1\n00:00:00,000 --> 00:00:01,000\nhi\n")); err != nil {
		t.Fatal(err)
	}

	copied, err := lib.CopyProject(src.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if copied.ID == src.ID {
		t.Fatal("副本应有独立 ID")
	}
	if copied.Title != "演示项目 - 副本" {
		t.Fatalf("副本标题不符：%q", copied.Title)
	}
	if len(copied.Subtitles) != 1 {
		t.Fatalf("副本应带 1 份字幕，实际 %d", len(copied.Subtitles))
	}
	if _, err := os.Stat(filepath.Join(lib.ProjectDir(copied.ID), "project.json")); err != nil {
		t.Fatalf("副本项目目录缺失：%v", err)
	}
	// 再复制一次 → (2)
	second, _ := lib.CopyProject(src.ID, "")
	if second.Title != "演示项目 - 副本 (2)" {
		t.Fatalf("第二次复制应加序号，实际 %q", second.Title)
	}
	if n := len(lib.ListProjects(ProjectListOptions{})); n != 3 {
		t.Fatalf("应有 3 个项目，实际 %d", n)
	}
}

func TestCopyFolderRecursive(t *testing.T) {
	lib, _ := Open(t.TempDir())
	defer lib.Close()
	parent, _ := lib.CreateFolder("课程", "")
	child, _ := lib.CreateFolder("第一课", parent.ID)
	p := makeTempProject(t, lib)
	_ = lib.MoveProjects([]string{p.ID}, child.ID)

	copied, err := lib.CopyFolder(parent.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if copied.Name != "课程 (2)" {
		t.Fatalf("复制文件夹应去重，实际 %q", copied.Name)
	}
	sub := lib.FolderSubtree(copied.ID)
	if len(sub) != 2 {
		t.Fatalf("副本应包含父+子两个文件夹，实际 %d", len(sub))
	}
	projects := lib.ListProjects(ProjectListOptions{})
	if len(projects) != 2 {
		t.Fatalf("应有原项目+副本，实际 %d", len(projects))
	}
}

func TestEmptyTrash(t *testing.T) {
	lib, _ := Open(t.TempDir())
	defer lib.Close()
	p := makeTempProject(t, lib)
	_ = lib.TrashProjects([]string{p.ID})
	n, err := lib.EmptyTrash()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应清空 1 个项目，实际 %d", n)
	}
	if len(lib.ListProjects(ProjectListOptions{View: ViewTrash})) != 0 {
		t.Fatal("清空后回收站应为空")
	}
	if _, err := os.Stat(filepath.Join(lib.Base(), "projects", p.ID)); !os.IsNotExist(err) {
		t.Fatal("彻底删除后项目目录应被移除")
	}
}

func TestListProjectViews(t *testing.T) {
	lib, _ := Open(t.TempDir())
	defer lib.Close()
	a := makeTempProject(t, lib)
	b := makeTempProject(t, lib)

	_, _ = lib.UpdateProject(a.ID, func(p *Project) { p.Status = StatusReady })
	_, _ = lib.UpdateProject(b.ID, func(p *Project) { p.Favorite = true })

	if got := lib.ListProjects(ProjectListOptions{View: ViewReady}); len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("已完成视图错误：%+v", got)
	}
	if got := lib.ListProjects(ProjectListOptions{View: ViewFavorites}); len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("收藏视图错误：%+v", got)
	}
	if got := lib.ListProjects(ProjectListOptions{View: ViewUnfiled}); len(got) != 2 {
		t.Fatalf("未分类视图应有 2 个，实际 %d", len(got))
	}
	if got := lib.ListProjects(ProjectListOptions{Sort: "name"}); len(got) != 2 || got[0].Title > got[1].Title {
		t.Fatalf("按名称排序错误：%+v", got)
	}
}
