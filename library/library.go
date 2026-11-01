// Package library 是工作台的数据核心：素材库（内容寻址、去重）与项目库
// （每个项目一个目录，字幕/成片/任务全部落盘）。
//
// 设计原则：
//   - 索引只是一个「可重建的缓存」：真实数据在 projects/<id>/ 与 assets/ 里，
//     即使 library.json 损坏也能从目录结构恢复。
//   - 所有写操作原子化，并保留 .bak。
//   - 同一份文件只存一次（按 SHA-256 内容寻址），项目之间共享引用。
package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/local/stt-sub/data"
)

// 项目状态。
const (
	StatusDraft      = "draft"      // 有素材，还没开始处理
	StatusProcessing = "processing" // 有任务在跑
	StatusReady      = "ready"      // 至少有一份可用产出
	StatusFailed     = "failed"     // 最近一次任务失败
)

// 项目类型，仅用于界面上的默认动作。
const (
	KindTranscribe = "transcribe"
	KindTranscode  = "transcode"
	KindInfo       = "info"
)

// ErrNotFound 表示项目/素材不存在。
var ErrNotFound = errors.New("library: 未找到")

// ProbeInfo 是对 ffprobe 结果的精简投影，只保留界面直接展示的字段。
type ProbeInfo struct {
	FormatName   string  `json:"formatName,omitempty"`
	LongName     string  `json:"longName,omitempty"`
	Duration     float64 `json:"duration,omitempty"` // 秒
	Size         int64   `json:"size,omitempty"`
	BitRate      int64   `json:"bitRate,omitempty"`
	VideoCodec   string  `json:"videoCodec,omitempty"`
	Width        int     `json:"width,omitempty"`
	Height       int     `json:"height,omitempty"`
	FrameRate    float64 `json:"frameRate,omitempty"`
	VideoBitRate int64   `json:"videoBitRate,omitempty"`
	AudioCodec   string  `json:"audioCodec,omitempty"`
	SampleRate   int     `json:"sampleRate,omitempty"`
	Channels     int     `json:"channels,omitempty"`
	HasAudio     bool    `json:"hasAudio"`
	HasVideo     bool    `json:"hasVideo"`
}

// Asset 是素材库中的一份文件。
type Asset struct {
	ID        string     `json:"id"`
	Hash      string     `json:"hash"`
	Path      string     `json:"path"` // 相对 data 目录
	FileName  string     `json:"fileName"`
	Size      int64      `json:"size"`
	CreatedAt time.Time  `json:"createdAt"`
	Origin    string     `json:"origin,omitempty"` // upload / output / legacy
	Probe     *ProbeInfo `json:"probe,omitempty"`
}

// SubtitleTrack 是项目中的一份字幕。
type SubtitleTrack struct {
	ID        string    `json:"id"`
	AssetID   string    `json:"assetId,omitempty"`
	FileName  string    `json:"fileName,omitempty"`
	Path      string    `json:"path,omitempty"` // 相对项目目录
	Format    string    `json:"format"`         // srt / vtt / json
	Language  string    `json:"language,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	Model     string    `json:"model,omitempty"`
	CueCount  int       `json:"cueCount"`
	Duration  float64   `json:"duration,omitempty"`
	Edited    bool      `json:"edited,omitempty"`
	// Text 是可搜索的纯文本（去时间码），用于按字幕内容搜索项目。
	Text      string    `json:"text,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// JobRef 挂在项目上，指向任务记录与产出。
type JobRef struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"` // transcribe / transcode / export
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

// Folder 是项目层级分类：每个项目最多归属一个文件夹（可嵌套）。
type Folder struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	ParentID  string    `json:"parentId,omitempty"`
	Deleted   bool      `json:"deleted,omitempty"`
	DeletedAt time.Time `json:"deletedAt,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Collection 是跨文件夹的项目集：一个项目可属于多个集。
type Collection struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// Attachment 是项目附带的额外文件（音频、字幕文件、文档等）。
// 直接内联 Asset 字段，并附带分类，便于预览/下载/管理。
type Attachment struct {
	Asset     `json:",inline"`
	Category  string `json:"category,omitempty"` // audio / subtitle / document / other
}

// Project 是一个工作单元：一份主素材 + 若干字幕 + 若干任务。
type Project struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Temporary bool            `json:"temporary,omitempty"` // 临时项目（不常驻资料库）
	Favorite  bool            `json:"favorite,omitempty"`
	Notes     string          `json:"notes,omitempty"`
	PrimaryID string          `json:"primaryId,omitempty"`
	Media     *Asset          `json:"media,omitempty"`
	Subtitles []SubtitleTrack `json:"subtitles,omitempty"`
	Outputs   []*Asset        `json:"outputs,omitempty"`
	Jobs      []JobRef        `json:"jobs,omitempty"`
	Duration  float64         `json:"duration,omitempty"`
	Language  string          `json:"language,omitempty"`
	LastError string          `json:"lastError,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
	OpenedAt  time.Time       `json:"openedAt,omitempty"`
	// 组织：文件夹（单归属）+ 项目集（多归属标签）
	FolderID      string   `json:"folderId,omitempty"`
	CollectionIDs []string `json:"collectionIds,omitempty"`
	// 回收站：删除先标记，可还原；彻底删除才移除记录。
	Deleted   bool      `json:"deleted,omitempty"`
	DeletedAt time.Time `json:"deletedAt,omitempty"`
	// 附件：除主素材外的额外文件（音频/字幕文件/文档等）
	Attachments []*Attachment `json:"attachments,omitempty"`
}

// Index 是 library.json 的落盘结构。
type Index struct {
	Version     int                 `json:"version"`
	Assets      map[string]*Asset   `json:"assets"`
	Projects    map[string]*Project `json:"projects"`
	Folders     map[string]*Folder  `json:"folders,omitempty"`
	Collections map[string]*Collection `json:"collections,omitempty"`
	Updated     time.Time           `json:"updatedAt"`
}

// Library 提供素材与项目操作，线程安全。
type Library struct {
	mu     sync.RWMutex
	base   string // 元数据目录：library.db 与旧版 library.json
	root   string // 内容目录：assets/、projects/、jobs/、temp/、trash/
	index  Index
	db     *sql.DB // SQLite 索引库（library.db）
	saveMu sync.Mutex
}

// Open 打开资料库，元数据与内容文件放在同一个目录（测试与旧调用方式）。
func Open(base string) (*Library, error) { return OpenAt(base, base) }

// OpenAt 打开资料库：metaDir 保存 SQLite 索引与设置，contentDir 保存素材、
// 项目与任务文件。contentDir 为空时退回 metaDir（保持旧行为）。
//
// 首次从旧布局（内容文件都在 data/ 下）升级时，会把 assets/projects/jobs/
// temp/trash 迁移到 contentDir，相对路径保持不变，索引无需改写。
func OpenAt(metaDir, contentDir string) (*Library, error) {
	if strings.TrimSpace(contentDir) == "" {
		contentDir = metaDir
	}
	if err := data.EnsureLayout(metaDir); err != nil {
		return nil, err
	}
	if err := migrateContent(metaDir, contentDir); err != nil {
		return nil, err
	}
	if err := data.EnsureProjectLayout(contentDir); err != nil {
		return nil, err
	}
	lib := &Library{base: metaDir, root: contentDir}
	lib.index = Index{
		Version:     1,
		Assets:      map[string]*Asset{},
		Projects:    map[string]*Project{},
		Folders:     map[string]*Folder{},
		Collections: map[string]*Collection{},
	}
	db, err := openSQLite(lib.dbPath())
	if err != nil {
		return nil, err
	}
	lib.db = db
	if err := lib.loadIndex(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if lib.index.Assets == nil {
		lib.index.Assets = map[string]*Asset{}
	}
	if lib.index.Projects == nil {
		lib.index.Projects = map[string]*Project{}
	}
	if lib.index.Folders == nil {
		lib.index.Folders = map[string]*Folder{}
	}
	if lib.index.Collections == nil {
		lib.index.Collections = map[string]*Collection{}
	}
	return lib, nil
}

// migrateContent 把旧版布局（内容文件在 data/ 下）迁移到独立内容目录。
// 只在目标不存在时移动，避免覆盖已有数据；同盘直接改名，跨盘逐项复制。
func migrateContent(from, to string) error {
	if strings.EqualFold(filepath.Clean(from), filepath.Clean(to)) {
		return nil
	}
	for _, sub := range []string{data.AssetsDir, data.ProjectsDir, data.JobsDir, data.TempDir, data.TrashDir} {
		src := filepath.Join(from, sub)
		dst := filepath.Join(to, sub)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if _, err := os.Stat(dst); err == nil {
			continue // 目标已有内容：不覆盖，留给用户手工合并
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("library: 迁移目录 %s 失败：%w", sub, err)
		}
		if err := os.Rename(src, dst); err == nil {
			continue
		}
		if err := copyTree(src, dst); err != nil {
			return fmt.Errorf("library: 迁移目录 %s 失败：%w", sub, err)
		}
		_ = data.RemoveDir(src)
	}
	return nil
}

// copyTree 递归复制目录（跨盘迁移的兜底方案）。
func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		from := filepath.Join(src, entry.Name())
		to := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyTree(from, to); err != nil {
				return err
			}
			continue
		}
		if err := data.MoveInto(from, to); err != nil {
			return err
		}
	}
	return nil
}

func (l *Library) Base() string { return l.base }

// Root 返回内容文件目录（素材、项目、任务所在的位置）。
func (l *Library) Root() string { return l.root }

func (l *Library) tempDir() string { return filepath.Join(l.root, data.TempDir) }
func (l *Library) projectDir(id string) string {
	return filepath.Join(l.root, data.ProjectsDir, id)
}

// Abs 把相对内容目录的路径还原为绝对路径。
func (l *Library) Abs(rel string) string {
	if rel == "" {
		return ""
	}
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(l.root, filepath.FromSlash(rel))
}

// Rel 把绝对路径转为相对内容目录的路径（用于落盘）。
func (l *Library) Rel(abs string) string {
	rel, err := filepath.Rel(l.root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

// Save 把当前索引写入 SQLite（library.db，单事务全量覆盖）。
// 索引损坏时可由 Recover() 从 projects/ 目录与 project.json 重建。
func (l *Library) Save() error {
	return l.persistIndexLocked()
}

// AssetPath 返回素材的绝对路径。
func (l *Library) AssetPath(a *Asset) string { return l.Abs(a.Path) }

// assetRelPath 计算内容寻址路径：assets/ab/<hash><ext>。
func assetRelPath(hash, fileName string) string {
	prefix := "00"
	if len(hash) >= 2 {
		prefix = hash[:2]
	}
	ext := strings.ToLower(filepath.Ext(fileName))
	return filepath.ToSlash(filepath.Join(data.AssetsDir, prefix, hash+ext))
}

// IngestOptions 描述一次素材导入。
type IngestOptions struct {
	SourcePath string
	FileName   string // 展示用文件名（默认取源文件 base name）
	Origin     string // upload / output / legacy
	// Copy 为 true 时复制进素材库（上传场景）；false 时登记已有文件（导出/转码产物）。
	Copy       bool
	Probe      *ProbeInfo
	OnProgress func(done, total int64)
}

// Ingest 把文件登记为素材（按内容去重）。相同内容已存在时直接复用。
func (l *Library) Ingest(ctx context.Context, opt IngestOptions) (*Asset, error) {
	if strings.TrimSpace(opt.SourcePath) == "" {
		return nil, errors.New("library: 缺少素材路径")
	}
	fileName := data.SafeName(opt.FileName)
	if fileName == "" || fileName == "file" {
		fileName = data.SafeName(filepath.Base(opt.SourcePath))
	}
	hash, size, err := data.HashFile(opt.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("library: 读取素材失败：%w", err)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// 去重：同 hash 直接复用已有素材。
	l.mu.RLock()
	for _, a := range l.index.Assets {
		if a.Hash == hash && a.Size == size {
			existing := *a
			l.mu.RUnlock()
			if opt.Probe != nil && existing.Probe == nil && !opt.Copy {
				_ = l.UpdateAssetProbe(existing.ID, opt.Probe)
			}
			return &existing, nil
		}
	}
	l.mu.RUnlock()

	rel := assetRelPath(hash, fileName)
	target := l.Abs(rel)
	if opt.Copy {
		if err := data.CopyFile(opt.SourcePath, target, opt.OnProgress); err != nil {
			return nil, fmt.Errorf("library: 保存素材失败：%w", err)
		}
	} else if filepath.Clean(target) != filepath.Clean(opt.SourcePath) {
		if err := data.CopyFile(opt.SourcePath, target, opt.OnProgress); err != nil {
			return nil, fmt.Errorf("library: 保存素材失败：%w", err)
		}
	}

	now := time.Now()
	asset := &Asset{
		ID:        data.NewID("a_"),
		Hash:      hash,
		Path:      rel,
		FileName:  fileName,
		Size:      size,
		CreatedAt: now,
		Origin:    opt.Origin,
		Probe:     opt.Probe,
	}
	l.mu.Lock()
	l.index.Assets[asset.ID] = asset
	l.mu.Unlock()
	if err := l.Save(); err != nil {
		return nil, err
	}
	out := *asset
	return &out, nil
}

// UpdateAssetProbe 回填媒体探测信息。
func (l *Library) UpdateAssetProbe(assetID string, probe *ProbeInfo) error {
	l.mu.Lock()
	a, ok := l.index.Assets[assetID]
	if ok {
		a.Probe = probe
	}
	l.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	return l.Save()
}

// Asset 按 ID 取素材快照。
func (l *Library) Asset(id string) (*Asset, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	a, ok := l.index.Assets[id]
	if !ok {
		return nil, false
	}
	cp := *a
	return &cp, true
}

// OutputAsset 按 ID 在所有项目的 Outputs 中查找转码产物。
// 转码产物只登记在项目 Outputs 里（不进全局索引），播放/下载时需回退到这里查找。
func (l *Library) OutputAsset(id string) (*Asset, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, p := range l.index.Projects {
		for _, a := range p.Outputs {
			if a.ID == id {
				cp := *a
				return &cp, true
			}
		}
	}
	return nil, false
}

// ---- 项目 ----

// ProjectDir 返回项目目录（不存在则创建子目录）。
func (l *Library) ProjectDir(id string) string {
	dir := l.projectDir(id)
	for _, sub := range []string{"media", "out", "subs"} {
		_ = os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}
	return dir
}

func (l *Library) projectMetaPath(id string) string {
	return filepath.Join(l.projectDir(id), "project.json")
}

// CreateProject 以给定素材新建项目。
func (l *Library) CreateProject(ctx context.Context, title string, kind string, asset *Asset, probe *ProbeInfo, opts ...ProjectOption) (*Project, error) {
	if asset == nil {
		return nil, errors.New("library: 缺少素材")
	}
	if strings.TrimSpace(title) == "" {
		title = strings.TrimSuffix(asset.FileName, filepath.Ext(asset.FileName))
	}
	now := time.Now()
	p := &Project{
		ID:        data.NewID("p_"),
		Title:     strings.TrimSpace(title),
		Kind:      kind,
		Status:    StatusDraft,
		PrimaryID: asset.ID,
		Media:     asset,
		CreatedAt: now,
		UpdatedAt: now,
		OpenedAt:  now,
	}
	if probe != nil {
		p.Duration = probe.Duration
	}
	for _, opt := range opts {
		opt(p)
	}
	if err := l.persistProject(p); err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.index.Projects[p.ID] = p
	l.mu.Unlock()
	if err := l.Save(); err != nil {
		return nil, err
	}
	return l.GetProject(p.ID)
}

// ProjectOption 用于定制新建项目。
type ProjectOption func(*Project)

// WithTemporary 标记临时项目（不进入资料库列表）。
func WithTemporary() ProjectOption { return func(p *Project) { p.Temporary = true } }

// WithLanguage 预设语言。
func WithLanguage(lang string) ProjectOption { return func(p *Project) { p.Language = lang } }

// persistProject 写项目目录内的 project.json（独立于总索引，便于迁移/修复）。
func (l *Library) persistProject(p *Project) error {
	p.UpdatedAt = time.Now()
	return data.WriteJSON(l.projectMetaPath(p.ID), p)
}

// GetProject 返回项目深拷贝。
func (l *Library) GetProject(id string) (*Project, error) {
	l.mu.RLock()
	p, ok := l.index.Projects[id]
	l.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	return cloneProject(p), nil
}

// FindProjectByAsset 查找引用指定素材的项目（返回最近打开的一个）。
// 用于重复导入同一文件时复用已有项目，避免资料库/临时区出现重复条目。
func (l *Library) FindProjectByAsset(assetID string, temporary bool) *Project {
	if assetID == "" {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	var best *Project
	for _, p := range l.index.Projects {
		if p.Deleted || p.Temporary != temporary {
			continue
		}
		if p.Media == nil || p.Media.ID != assetID {
			continue
		}
		if best == nil || p.OpenedAt.After(best.OpenedAt) {
			best = p
		}
	}
	if best == nil {
		return nil
	}
	return cloneProject(best)
}

// TouchProject 更新「最近打开时间」，用于资料库排序。
func (l *Library) TouchProject(id string) {
	l.mu.Lock()
	if p, ok := l.index.Projects[id]; ok {
		p.OpenedAt = time.Now()
	}
	l.mu.Unlock()
	_ = l.Save()
}

// UpdateProject 在锁内修改项目并落盘。
func (l *Library) UpdateProject(id string, mutate func(p *Project)) (*Project, error) {
	l.mu.Lock()
	p, ok := l.index.Projects[id]
	if !ok {
		l.mu.Unlock()
		return nil, ErrNotFound
	}
	mutate(p)
	p.UpdatedAt = time.Now()
	snapshot := cloneProject(p)
	l.mu.Unlock()

	if err := l.persistProject(snapshot); err != nil {
		return nil, err
	}
	if err := l.Save(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// RenameProject 修改标题。
func (l *Library) RenameProject(id, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("library: 标题不能为空")
	}
	_, err := l.UpdateProject(id, func(p *Project) { p.Title = title })
	return err
}

// DeleteProject 删除项目目录，并在没有其他引用时清理素材文件。
func (l *Library) DeleteProject(id string) error {
	l.mu.Lock()
	_, ok := l.index.Projects[id]
	delete(l.index.Projects, id)
	l.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	dir := l.projectDir(id)
	// 先移到 trash 再删除，避免删除过程被杀进程后留下半截目录。
	trash := filepath.Join(l.root, data.TrashDir, id+"-"+time.Now().Format("150405"))
	if err := os.MkdirAll(filepath.Dir(trash), 0o755); err == nil {
		if err := os.Rename(dir, trash); err == nil {
			dir = trash
		}
	}
	_ = data.RemoveDir(dir)
	// 项目.json 里的素材若是本地副本（media/），随目录一起删除；
	// 共享素材在 Assets 里按引用计数回收。
	l.gcAssetsLocked()
	if err := l.Save(); err != nil {
		return err
	}
	return nil
}

// gcAssetsLocked 删除没有任何项目引用的素材。
func (l *Library) gcAssetsLocked() {
	used := map[string]bool{}
	for _, p := range l.index.Projects {
		if p.PrimaryID != "" {
			used[p.PrimaryID] = true
		}
		if p.Media != nil {
			used[p.Media.ID] = true
		}
		for _, t := range p.Subtitles {
			if t.AssetID != "" {
				used[t.AssetID] = true
			}
		}
		for _, o := range p.Outputs {
			if o != nil {
				used[o.ID] = true
			}
		}
		for _, at := range p.Attachments {
			if at != nil && at.ID != "" {
				used[at.ID] = true
			}
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, a := range l.index.Assets {
		if used[id] {
			continue
		}
		_ = os.Remove(l.AssetPath(a))
		delete(l.index.Assets, id)
	}
}

// 列表视图（与资源管理器的「位置」对应）。
const (
	ViewAll        = "all"        // 全部项目（不含回收站）
	ViewRecent     = "recent"     // 最近使用
	ViewFavorites  = "favorites"  // 收藏
	ViewUnfiled    = "unfiled"    // 未分类
	ViewTrash      = "trash"      // 回收站
	ViewReady      = "ready"      // 已完成
	ViewProcessing = "processing" // 处理中
	ViewFailed     = "failed"     // 失败
	ViewNoSubtitle = "nosubtitle" // 没有字幕
	ViewNoOutput   = "nooutput"   // 没有成片
)

// ProjectListOptions 控制列表过滤。
type ProjectListOptions struct {
	Query        string
	Kind         string
	Temporary    bool
	Favorite     bool
	FolderID     string // 按文件夹过滤（单归属）；空表示不限
	CollectionID string // 按项目集过滤（多归属）
	View         string // 见 View* 常量；空等价于 ViewAll
	Sort         string // name / recent / created / updated / duration / size / subtitles
	Desc         bool   // 是否倒序（默认按所选字段自然顺序）
	Limit        int
	Offset       int
}

// matchesView 判断项目是否属于某个视图。
func (l *Library) matchesView(p *Project, view string) bool {
	switch view {
	case "", ViewAll:
		return !p.Deleted
	case ViewTrash:
		return p.Deleted
	case ViewRecent:
		if p.Deleted {
			return false
		}
		at := p.OpenedAt
		if at.IsZero() {
			at = p.UpdatedAt
		}
		return time.Since(at) <= 30*24*time.Hour
	case ViewFavorites:
		return !p.Deleted && p.Favorite
	case ViewUnfiled:
		return !p.Deleted && p.FolderID == ""
	case ViewReady:
		return !p.Deleted && p.Status == StatusReady
	case ViewProcessing:
		return !p.Deleted && p.Status == StatusProcessing
	case ViewFailed:
		return !p.Deleted && p.Status == StatusFailed
	case ViewNoSubtitle:
		return !p.Deleted && len(p.Subtitles) == 0
	case ViewNoOutput:
		return !p.Deleted && len(p.Outputs) == 0
	default:
		return !p.Deleted
	}
}

// projectSize 返回项目占用的近似体积（主素材 + 成片 + 附件）。
func projectSize(p *Project) int64 {
	var n int64
	if p.Media != nil {
		n += p.Media.Size
	}
	for _, o := range p.Outputs {
		if o != nil {
			n += o.Size
		}
	}
	for _, at := range p.Attachments {
		if at != nil {
			n += at.Size
		}
	}
	return n
}

// ListProjects 返回按条件过滤并排序的项目（深拷贝）。
func (l *Library) ListProjects(opt ProjectListOptions) []*Project {
	l.mu.RLock()
	out := make([]*Project, 0, len(l.index.Projects))
	q := strings.ToLower(strings.TrimSpace(opt.Query))
	for _, p := range l.index.Projects {
		if p.Temporary != opt.Temporary {
			continue
		}
		if !l.matchesView(p, opt.View) {
			continue
		}
		if opt.Kind != "" && p.Kind != opt.Kind {
			continue
		}
		if opt.Favorite && !p.Favorite {
			continue
		}
		if opt.FolderID != "" && p.FolderID != opt.FolderID {
			continue
		}
		if opt.CollectionID != "" {
			in := false
			for _, c := range p.CollectionIDs {
				if c == opt.CollectionID {
					in = true
					break
				}
			}
			if !in {
				continue
			}
		}
		if q != "" {
			hay := strings.ToLower(p.Title + " " + p.Notes)
			if p.Media != nil {
				hay += " " + strings.ToLower(p.Media.FileName)
			}
			for _, at := range p.Attachments {
				if at != nil {
					hay += " " + strings.ToLower(at.FileName)
				}
			}
			for _, t := range p.Subtitles {
				hay += " " + strings.ToLower(t.FileName)
				if t.Text != "" {
					hay += " " + strings.ToLower(t.Text)
				}
			}
			if f, ok := l.index.Folders[p.FolderID]; ok {
				hay += " " + strings.ToLower(f.Name)
			}
			for _, cid := range p.CollectionIDs {
				if c, ok := l.index.Collections[cid]; ok {
					hay += " " + strings.ToLower(c.Name)
				}
			}
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, cloneProject(p))
	}
	l.mu.RUnlock()

	// 排序：默认「名称」与资源管理器一致，中文按拼音（localeCompare 在浏览器侧；
	// 这里按 Unicode 近似，前端会再按名称本地化排序）。
	less := func(a, b *Project) bool { return false }
	switch opt.Sort {
	case "created":
		less = func(a, b *Project) bool { return a.CreatedAt.Before(b.CreatedAt) }
	case "updated":
		less = func(a, b *Project) bool { return a.UpdatedAt.Before(b.UpdatedAt) }
	case "recent":
		less = func(a, b *Project) bool {
			ta, tb := a.OpenedAt, b.OpenedAt
			if ta.IsZero() {
				ta = a.UpdatedAt
			}
			if tb.IsZero() {
				tb = b.UpdatedAt
			}
			return ta.Before(tb)
		}
	case "duration":
		less = func(a, b *Project) bool { return a.Duration < b.Duration }
	case "size":
		less = func(a, b *Project) bool { return projectSize(a) < projectSize(b) }
	case "subtitles":
		less = func(a, b *Project) bool { return len(a.Subtitles) < len(b.Subtitles) }
	default: // name
		less = func(a, b *Project) bool {
			return strings.ToLower(a.Title) < strings.ToLower(b.Title)
		}
	}
	if opt.Desc {
		inner := less
		less = func(a, b *Project) bool { return inner(b, a) }
	}
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	if opt.Offset > 0 {
		if opt.Offset >= len(out) {
			return nil
		}
		out = out[opt.Offset:]
	}
	if opt.Limit > 0 && len(out) > opt.Limit {
		out = out[:opt.Limit]
	}
	return out
}

// Stats 是资料库概览数据。
type Stats struct {
	Projects   int   `json:"projects"`
	Assets     int   `json:"assets"`
	DiskBytes  int64 `json:"diskBytes"`
	MediaBytes int64 `json:"mediaBytes"`
	SubsBytes  int64 `json:"subsBytes"`
	OutBytes   int64 `json:"outBytes"`
	TempBytes  int64 `json:"tempBytes"`
	ReadyCount int   `json:"readyCount"`
	TrashCount int   `json:"trashCount"`
	FolderCount int  `json:"folderCount"`
}

// Stats 统计项目数量与磁盘占用（会遍历目录，调用频率不要太高）。
// 临时项目（导入后尚未转存）不计入项目总数，与「项目库」列表保持一致。
func (l *Library) Stats() Stats {
	l.mu.RLock()
	st := Stats{Assets: len(l.index.Assets)}
	for _, p := range l.index.Projects {
		if p.Temporary {
			continue
		}
		if p.Deleted {
			st.TrashCount++
			continue
		}
		st.Projects++
		if p.Status == StatusReady {
			st.ReadyCount++
		}
	}
	for _, f := range l.index.Folders {
		if !f.Deleted {
			st.FolderCount++
		}
	}
	l.mu.RUnlock()
	st.MediaBytes = data.DirSize(filepath.Join(l.root, data.AssetsDir))
	st.DiskBytes = data.DirSize(l.root)
	st.TempBytes = data.DirSize(filepath.Join(l.root, data.TempDir))
	projectsDir := filepath.Join(l.root, data.ProjectsDir)
	_ = filepath.Walk(projectsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		switch filepath.Base(filepath.Dir(path)) {
		case "subs":
			st.SubsBytes += info.Size()
		case "out":
			st.OutBytes += info.Size()
		}
		return nil
	})
	return st
}

// TempDir 返回临时目录（确保存在）。
func (l *Library) TempDir() string {
	dir := l.tempDir()
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// CleanTemp 清理超过 maxAge 的临时文件。
func (l *Library) CleanTemp(maxAge time.Duration) int {
	dir := l.tempDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < maxAge {
			continue
		}
		if data.RemoveDir(filepath.Join(dir, e.Name())) == nil {
			removed++
		}
	}
	return removed
}

// PurgeStaleTemporary 删除长期未用的临时项目（及其目录）。
func (l *Library) PurgeStaleTemporary(maxAge time.Duration) int {
	l.mu.RLock()
	var ids []string
	for id, p := range l.index.Projects {
		if p.Temporary && time.Since(p.UpdatedAt) > maxAge {
			ids = append(ids, id)
		}
	}
	l.mu.RUnlock()
	n := 0
	for _, id := range ids {
		if l.DeleteProject(id) == nil {
			n++
		}
	}
	return n
}

// SaveSubtitleTrack 写出字幕文件并把 track 挂到项目上（新增或更新）。
func (l *Library) SaveSubtitleTrack(projectID string, track SubtitleTrack, content []byte) (*Project, error) {
	dir := l.ProjectDir(projectID)
	if track.ID == "" {
		track.ID = data.NewID("s_")
	}
	ext := strings.ToLower(strings.TrimSpace(track.Format))
	if ext == "" {
		ext = "srt"
	}
	name := data.SafeName(strings.TrimSuffix(track.FileName, filepath.Ext(track.FileName)))
	if name == "" || name == "file" {
		name = "subtitle"
	}
	track.FileName = name + "." + ext
	track.Path = filepath.ToSlash(filepath.Join("subs", track.ID+"."+ext))
	abs := filepath.Join(dir, filepath.FromSlash(track.Path))
	if err := data.WriteFile(abs, content, 0o644); err != nil {
		return nil, err
	}
	now := time.Now()
	if track.CreatedAt.IsZero() {
		track.CreatedAt = now
	}
	track.UpdatedAt = now
	// 维护可搜索纯文本（去时间码），用于按字幕内容检索项目。
	track.Text = subtitlePlainText(content)

	return l.UpdateProject(projectID, func(p *Project) {
		replaced := false
		for i := range p.Subtitles {
			if p.Subtitles[i].ID == track.ID {
				p.Subtitles[i] = track
				replaced = true
			}
		}
		if !replaced {
			p.Subtitles = append([]SubtitleTrack{track}, p.Subtitles...)
		}
		p.Status = StatusReady
		p.LastError = ""
		if track.Language != "" && p.Language == "" {
			p.Language = track.Language
		}
	})
}

// SubtitleAbsPath 返回字幕文件绝对路径。
func (l *Library) SubtitleAbsPath(p *Project, t SubtitleTrack) string {
	if t.Path == "" {
		return ""
	}
	return filepath.Join(l.ProjectDir(p.ID), filepath.FromSlash(t.Path))
}

// ReadSubtitle 读取字幕内容。
func (l *Library) ReadSubtitle(p *Project, t SubtitleTrack) ([]byte, error) {
	path := l.SubtitleAbsPath(p, t)
	if path == "" {
		return nil, ErrNotFound
	}
	return os.ReadFile(path)
}

// FindSubtitle 在项目中按 ID 查找字幕。
func FindSubtitle(p *Project, id string) (SubtitleTrack, bool) {
	for _, t := range p.Subtitles {
		if t.ID == id {
			return t, true
		}
	}
	return SubtitleTrack{}, false
}

// Recover 扫描 projects/ 与 assets/，把索引里缺失的条目补回来。
// 用于 library.json 丢失或损坏后的自愈。
func (l *Library) Recover() (int, error) {
	recovered := 0
	projRoot := filepath.Join(l.root, data.ProjectsDir)
	entries, err := os.ReadDir(projRoot)
	if err != nil {
		return 0, err
	}

	l.mu.Lock()
	for _, e := range entries {
		if !e.IsDir() || l.index.Projects[e.Name()] != nil {
			continue
		}
		var p Project
		meta := filepath.Join(projRoot, e.Name(), "project.json")
		if err := data.ReadJSON(meta, &p); err != nil || p.ID == "" {
			continue
		}
		// 中断的任务状态回落为 draft，避免界面显示「运行中」但其实没有进程。
		if p.Status == StatusProcessing {
			p.Status = StatusDraft
		}
		l.index.Projects[p.ID] = &p
		if p.Media != nil {
			l.index.Assets[p.Media.ID] = p.Media
		}
		recovered++
	}
	l.mu.Unlock()

	if recovered == 0 {
		return 0, nil
	}
	return recovered, l.Save()
}

func cloneProject(p *Project) *Project {
	cp := *p
	if p.Media != nil {
		m := *p.Media
		cp.Media = &m
	}
	cp.Subtitles = append([]SubtitleTrack(nil), p.Subtitles...)
	cp.Outputs = append([]*Asset(nil), p.Outputs...)
	cp.Jobs = append([]JobRef(nil), p.Jobs...)
	cp.Attachments = append([]*Attachment(nil), p.Attachments...)
	return &cp
}
