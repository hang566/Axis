package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func makeTempProject(t *testing.T, lib *Library) *Project {
	t.Helper()
	src := filepath.Join(t.TempDir(), "demo.mp4")
	if err := os.WriteFile(src, []byte("fake media content"), 0o644); err != nil {
		t.Fatal(err)
	}
	asset, err := lib.Ingest(context.Background(), IngestOptions{
		SourcePath: src, FileName: "demo.mp4", Origin: "upload", Copy: true,
		Probe: &ProbeInfo{Duration: 12.5, HasAudio: true, HasVideo: true},
	})
	if err != nil {
		t.Fatalf("导入素材失败：%v", err)
	}
	p, err := lib.CreateProject(context.Background(), "演示项目", KindTranscribe, asset, asset.Probe)
	if err != nil {
		t.Fatalf("创建项目失败：%v", err)
	}
	return p
}

func TestProjectLifecycle(t *testing.T) {
	dir := t.TempDir()
	lib, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()
	p := makeTempProject(t, lib)
	if got := lib.ListProjects(ProjectListOptions{}); len(got) != 1 {
		t.Fatalf("项目列表数量错误：%d", len(got))
	}
	if p.Media == nil || p.Media.Size != int64(len("fake media content")) {
		t.Fatalf("素材信息错误：%+v", p.Media)
	}

	updated, err := lib.SaveSubtitleTrack(p.ID, SubtitleTrack{Format: "srt", CueCount: 2}, []byte("1\n00:00:00,000 --> 00:00:01,000\nhi\n\n"))
	if err != nil {
		t.Fatalf("保存字幕失败：%v", err)
	}
	if updated.Status != StatusReady || len(updated.Subtitles) != 1 {
		t.Fatalf("项目状态未更新：%+v", updated)
	}
	track := updated.Subtitles[0]
	raw, err := lib.ReadSubtitle(updated, track)
	if err != nil || len(raw) == 0 {
		t.Fatalf("读取字幕失败：%v", err)
	}

	// 重新打开应能读到同样的数据（持久化验证）。
	lib2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lib2.Close()
	p2, err := lib2.GetProject(p.ID)
	if err != nil || len(p2.Subtitles) != 1 {
		t.Fatalf("重启后字幕丢失：%v %+v", err, p2)
	}

	if err := lib2.DeleteProject(p.ID); err != nil {
		t.Fatalf("删除项目失败：%v", err)
	}
	if _, err := lib2.GetProject(p.ID); err == nil {
		t.Fatal("删除后仍能读到项目")
	}
}

func TestRecoverFromIndexLoss(t *testing.T) {
	dir := t.TempDir()
	lib, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	makeTempProject(t, lib)
	// 模拟索引丢失（SQLite 索引库）。先关闭连接，Windows 下才能删除文件。
	_ = lib.Close()
	_ = os.Remove(filepath.Join(dir, "library.db"))
	_ = os.Remove(filepath.Join(dir, "library.db-wal"))
	_ = os.Remove(filepath.Join(dir, "library.db-shm"))
	lib2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lib2.Close()
	if got := lib2.ListProjects(ProjectListOptions{}); len(got) != 0 {
		t.Fatalf("索引丢失后不应有项目，实际 %d", len(got))
	}
	n, err := lib2.Recover()
	if err != nil {
		t.Fatalf("恢复失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("应恢复 1 个项目，实际 %d", n)
	}
}

func TestStaleTemporaryPurge(t *testing.T) {
	dir := t.TempDir()
	lib, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()
	p := makeTempProject(t, lib)
	_, _ = lib.UpdateProject(p.ID, func(pp *Project) {
		pp.Temporary = true
		pp.UpdatedAt = time.Now().Add(-96 * time.Hour)
	})
	// 模拟「很久以前」的状态：把索引里的更新时间也回拨。
	lib.mu.Lock()
	lib.index.Projects[p.ID].UpdatedAt = time.Now().Add(-96 * time.Hour)
	lib.mu.Unlock()
	_ = lib.Save()
	if n := lib.PurgeStaleTemporary(72 * time.Hour); n != 1 {
		t.Fatalf("应清理 1 个临时项目，实际 %d", n)
	}
}

// createAssetProject 新建一个独立素材与项目，temporary 控制是否为临时工作区。
func createAssetProject(t *testing.T, lib *Library, content string, temporary bool) *Project {
	t.Helper()
	src := filepath.Join(t.TempDir(), "sample.mp4")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	asset, err := lib.Ingest(context.Background(), IngestOptions{
		SourcePath: src, FileName: "sample.mp4", Origin: "upload", Copy: true,
		Probe: &ProbeInfo{Duration: 10, HasAudio: true, HasVideo: true},
	})
	if err != nil {
		t.Fatalf("导入素材失败：%v", err)
	}
	var opts []ProjectOption
	if temporary {
		opts = append(opts, WithTemporary())
	}
	p, err := lib.CreateProject(context.Background(), "项目", KindTranscribe, asset, asset.Probe, opts...)
	if err != nil {
		t.Fatalf("创建项目失败：%v", err)
	}
	return p
}

func TestStatsIgnoresTemporaryProjects(t *testing.T) {
	lib, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()

	real := createAssetProject(t, lib, "正式项目内容", false)
	for i := 0; i < 2; i++ {
		createAssetProject(t, lib, "临时内容"+string(rune('a'+i)), true)
	}

	st := lib.Stats()
	if st.Projects != 1 {
		t.Fatalf("项目总数应只统计正式项目（1），实际 %d", st.Projects)
	}
	list := lib.ListProjects(ProjectListOptions{})
	if len(list) != 1 || list[0].ID != real.ID {
		t.Fatalf("项目库列表应为 1 个正式项目，实际 %d", len(list))
	}
}

func TestOpenAtSplitsMetadataAndContent(t *testing.T) {
	meta := t.TempDir()
	root := filepath.Join(t.TempDir(), "project")
	lib, err := OpenAt(meta, root)
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()

	p := createAssetProject(t, lib, "拆分布局内容", false)
	if _, err := os.Stat(filepath.Join(meta, "library.db")); err != nil {
		t.Fatalf("元数据库应位于 data 目录：%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "projects", p.ID)); err != nil {
		t.Fatalf("项目目录应位于内容目录：%v", err)
	}
	if _, err := os.Stat(lib.AssetPath(p.Media)); err != nil {
		t.Fatalf("素材文件应位于内容目录：%v", err)
	}
	if _, err := os.Stat(filepath.Join(meta, "assets")); !os.IsNotExist(err) {
		t.Fatalf("元数据目录不应出现素材文件：%v", err)
	}

	lib2, err := OpenAt(meta, root)
	if err != nil {
		t.Fatal(err)
	}
	defer lib2.Close()
	if got := len(lib2.ListProjects(ProjectListOptions{})); got != 1 {
		t.Fatalf("重新打开后项目数量错误：%d", got)
	}
}

func TestMigrateLegacyLayout(t *testing.T) {
	meta := t.TempDir()
	root := filepath.Join(t.TempDir(), "project")

	old, err := Open(meta)
	if err != nil {
		t.Fatal(err)
	}
	p := createAssetProject(t, old, "旧布局内容", false)
	_ = old.Close()

	lib, err := OpenAt(meta, root)
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()

	if _, err := os.Stat(filepath.Join(root, "projects", p.ID)); err != nil {
		t.Fatalf("旧项目目录未迁移到内容目录：%v", err)
	}
	if _, err := os.Stat(filepath.Join(meta, "projects")); !os.IsNotExist(err) {
		t.Fatalf("旧布局的项目目录应已移走：%v", err)
	}
	if _, err := os.Stat(lib.AssetPath(p.Media)); err != nil {
		t.Fatalf("旧素材未迁移到内容目录：%v", err)
	}
	if got := len(lib.ListProjects(ProjectListOptions{})); got != 1 {
		t.Fatalf("迁移后项目数量错误：%d", got)
	}
}

func TestFindProjectByAsset(t *testing.T) {
	lib, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()

	temp := createAssetProject(t, lib, "临时素材内容", true)
	real := createAssetProject(t, lib, "正式素材内容", false)

	if got := lib.FindProjectByAsset(temp.Media.ID, true); got == nil || got.ID != temp.ID {
		t.Fatalf("应命中临时项目：%+v", got)
	}
	if got := lib.FindProjectByAsset(real.Media.ID, false); got == nil || got.ID != real.ID {
		t.Fatalf("应命中正式项目：%+v", got)
	}
	// 临时/正式分区互不影响，也用于「重复导入」时判断是否复用。
	if got := lib.FindProjectByAsset(temp.Media.ID, false); got != nil {
		t.Fatalf("不应在正式项目中命中临时素材：%+v", got)
	}
	if got := lib.FindProjectByAsset("", true); got != nil {
		t.Fatalf("空素材 ID 不应命中：%+v", got)
	}
}
