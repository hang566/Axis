package library

// 组织与文件管理：文件夹（可嵌套）、项目集、附件、回收站、复制/移动。
//
// 交互语义对齐 Windows 资源管理器：
//   - 文件夹是唯一的层级分组，删除文件夹会连同其中项目一起进回收站；
//   - 删除先进回收站，可还原，也可彻底删除；
//   - 复制项目会复制一份独立的项目目录，素材仍按内容哈希共享；
//   - 同一文件夹内不允许重名，自动追加 (2)(3)… 后缀。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/local/stt-sub/data"
)

// subtitleTimecode 匹配 SRT/VTT 的时间码行（含 -->）。
var subtitleTimecode = regexp.MustCompile(`-->|\d{1,2}:\d{2}:\d{2}`)

// subtitlePlainText 从字幕原文中提取可搜索纯文本：去掉时间码行与序号。
func subtitlePlainText(content []byte) string {
	lines := strings.Split(string(content), "\n")
	var b strings.Builder
	for _, ln := range lines {
		s := strings.TrimSpace(ln)
		if s == "" || subtitleTimecode.MatchString(s) {
			continue
		}
		// 跳过纯序号行（SRT 的 1、2、3…）
		if isDigits(s) {
			continue
		}
		b.WriteString(s)
		b.WriteByte(' ')
	}
	return strings.TrimSpace(b.String())
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// guessCategory 根据探测信息与扩展名推断附件分类。
func guessCategory(a *Asset) string {
	if a == nil {
		return "other"
	}
	if a.Probe != nil {
		if a.Probe.HasVideo {
			return "video"
		}
		if a.Probe.HasAudio {
			return "audio"
		}
	}
	ext := strings.ToLower(strings.TrimPrefix(filepathExt(a.FileName), "."))
	switch ext {
	case "srt", "vtt", "ass", "ssa", "sbv", "sub", "lrc":
		return "subtitle"
	case "mp3", "wav", "flac", "aac", "m4a", "ogg", "opus", "wma":
		return "audio"
	case "txt", "md", "pdf", "doc", "docx", "csv":
		return "document"
	default:
		return "other"
	}
}

func filepathExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

// ---- 文件夹 ----

// ListFolders 返回全部未删除的文件夹（嵌套关系由 ParentID 表达）。
func (l *Library) ListFolders() []*Folder {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]*Folder, 0, len(l.index.Folders))
	for _, f := range l.index.Folders {
		if f.Deleted {
			continue
		}
		cp := *f
		out = append(out, &cp)
	}
	return out
}

// ListTrashedFolders 返回回收站里的文件夹。
func (l *Library) ListTrashedFolders() []*Folder {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]*Folder, 0, len(l.index.Folders))
	for _, f := range l.index.Folders {
		if !f.Deleted {
			continue
		}
		cp := *f
		out = append(out, &cp)
	}
	return out
}

// FolderSubtree 返回某文件夹及其全部子孙的 ID（仅未删除的节点）。
func (l *Library) FolderSubtree(id string) map[string]bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.folderSubtreeLocked(id, false)
}

func (l *Library) folderSubtreeLocked(id string, includeDeleted bool) map[string]bool {
	out := map[string]bool{}
	if _, ok := l.index.Folders[id]; !ok {
		return out
	}
	children := map[string][]*Folder{}
	for _, f := range l.index.Folders {
		if f.Deleted && !includeDeleted {
			continue
		}
		children[f.ParentID] = append(children[f.ParentID], f)
	}
	queue := []string{id}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if out[cur] {
			continue
		}
		out[cur] = true
		for _, child := range children[cur] {
			queue = append(queue, child.ID)
		}
	}
	return out
}

// uniqueFolderNameLocked 在 parentID 下生成不重名的文件夹名。
func (l *Library) uniqueFolderNameLocked(parentID, name, excludeID string) string {
	base := strings.TrimSpace(name)
	if base == "" {
		base = "新建文件夹"
	}
	taken := map[string]bool{}
	for _, f := range l.index.Folders {
		if f.Deleted || f.ParentID != parentID || f.ID == excludeID {
			continue
		}
		taken[strings.ToLower(f.Name)] = true
	}
	if !taken[strings.ToLower(base)] {
		return base
	}
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s (%d)", base, i)
		if !taken[strings.ToLower(cand)] {
			return cand
		}
	}
	return fmt.Sprintf("%s (%s)", base, data.NewID(""))
}

// CreateFolder 新建文件夹；同名时自动追加序号（与资源管理器一致）。
func (l *Library) CreateFolder(name, parentID string) (*Folder, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("文件夹名称不能为空")
	}
	l.mu.Lock()
	if parentID != "" {
		parent, ok := l.index.Folders[parentID]
		if !ok || parent.Deleted {
			l.mu.Unlock()
			return nil, ErrNotFound
		}
	}
	f := &Folder{
		ID:        data.NewID("f_"),
		Name:      l.uniqueFolderNameLocked(parentID, name, ""),
		ParentID:  parentID,
		CreatedAt: time.Now(),
	}
	l.index.Folders[f.ID] = f
	cp := *f
	l.mu.Unlock()
	if err := l.Save(); err != nil {
		return nil, err
	}
	return &cp, nil
}

// RenameFolder 重命名文件夹（同目录下自动去重）。
func (l *Library) RenameFolder(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("文件夹名称不能为空")
	}
	l.mu.Lock()
	f, ok := l.index.Folders[id]
	if !ok || f.Deleted {
		l.mu.Unlock()
		return ErrNotFound
	}
	f.Name = l.uniqueFolderNameLocked(f.ParentID, name, id)
	l.mu.Unlock()
	return l.Save()
}

// MoveFolder 把文件夹移动到另一个父文件夹下（parentID 为空表示移到根目录）。
// 会拒绝移动到自身或自己的子孙之下，避免出现环；目标目录重名时自动追加序号。
func (l *Library) MoveFolder(id, parentID string) error {
	l.mu.Lock()
	f, ok := l.index.Folders[id]
	if !ok || f.Deleted {
		l.mu.Unlock()
		return ErrNotFound
	}
	if parentID != "" {
		if parentID == id {
			l.mu.Unlock()
			return errors.New("文件夹不能移动到自身")
		}
		target, ok := l.index.Folders[parentID]
		if !ok || target.Deleted {
			l.mu.Unlock()
			return ErrNotFound
		}
		// 沿目标向上回溯，遇到自身说明会形成环。
		for cur := target; cur != nil; {
			if cur.ID == id {
				l.mu.Unlock()
				return errors.New("文件夹不能移动到自己的子文件夹下")
			}
			if cur.ParentID == "" {
				break
			}
			cur = l.index.Folders[cur.ParentID]
		}
	}
	f.ParentID = parentID
	f.Name = l.uniqueFolderNameLocked(parentID, f.Name, id)
	l.mu.Unlock()
	return l.Save()
}

// TrashFolder 把文件夹（含子文件夹与其中项目）移入回收站。
func (l *Library) TrashFolder(id string) error {
	now := time.Now()
	l.mu.Lock()
	if _, ok := l.index.Folders[id]; !ok {
		l.mu.Unlock()
		return ErrNotFound
	}
	sub := l.folderSubtreeLocked(id, true)
	for fid := range sub {
		if f := l.index.Folders[fid]; f != nil && !f.Deleted {
			f.Deleted = true
			f.DeletedAt = now
		}
	}
	changed := make([]*Project, 0)
	for _, p := range l.index.Projects {
		if p.Deleted || !sub[p.FolderID] {
			continue
		}
		p.Deleted = true
		p.DeletedAt = now
		p.UpdatedAt = now
		changed = append(changed, cloneProject(p))
	}
	l.mu.Unlock()
	for _, p := range changed {
		_ = l.persistProject(p)
	}
	return l.Save()
}

// RestoreFolder 从回收站还原文件夹及其内容。
func (l *Library) RestoreFolder(id string) error {
	l.mu.Lock()
	f, ok := l.index.Folders[id]
	if !ok {
		l.mu.Unlock()
		return ErrNotFound
	}
	sub := l.folderSubtreeLocked(id, true)
	// 父级若仍在回收站，还原到根目录，避免出现「父级不可见」的孤儿。
	if pid := f.ParentID; pid != "" {
		if parent, ok := l.index.Folders[pid]; !ok || parent.Deleted {
			f.ParentID = ""
		}
	}
	for fid := range sub {
		if x := l.index.Folders[fid]; x != nil && x.Deleted {
			x.Deleted = false
			x.DeletedAt = time.Time{}
		}
	}
	changed := make([]*Project, 0)
	for _, p := range l.index.Projects {
		if !p.Deleted || !sub[p.FolderID] {
			continue
		}
		p.Deleted = false
		p.DeletedAt = time.Time{}
		p.UpdatedAt = time.Now()
		changed = append(changed, cloneProject(p))
	}
	l.mu.Unlock()
	for _, p := range changed {
		_ = l.persistProject(p)
	}
	return l.Save()
}

// DeleteFolder 彻底删除文件夹（连同子文件夹和其中项目）。
func (l *Library) DeleteFolder(id string) error {
	l.mu.Lock()
	if _, ok := l.index.Folders[id]; !ok {
		l.mu.Unlock()
		return ErrNotFound
	}
	sub := l.folderSubtreeLocked(id, true)
	var projectIDs []string
	for _, p := range l.index.Projects {
		if sub[p.FolderID] {
			projectIDs = append(projectIDs, p.ID)
		}
	}
	l.mu.Unlock()

	for _, pid := range projectIDs {
		_ = l.DeleteProject(pid)
	}
	l.mu.Lock()
	for fid := range sub {
		delete(l.index.Folders, fid)
	}
	l.mu.Unlock()
	return l.Save()
}

// CopyFolder 深拷贝一个文件夹（含子文件夹与项目）到目标父目录。
func (l *Library) CopyFolder(id, parentID string) (*Folder, error) {
	l.mu.RLock()
	src, ok := l.index.Folders[id]
	if !ok || src.Deleted {
		l.mu.RUnlock()
		return nil, ErrNotFound
	}
	if parentID != "" {
		if p, ok := l.index.Folders[parentID]; !ok || p.Deleted {
			l.mu.RUnlock()
			return nil, ErrNotFound
		}
	}
	type snap struct {
		folder Folder
		kids   []string
	}
	nodes := map[string]*snap{}
	childrenOf := map[string][]string{}
	for _, f := range l.index.Folders {
		if f.Deleted {
			continue
		}
		nodes[f.ID] = &snap{folder: *f}
		childrenOf[f.ParentID] = append(childrenOf[f.ParentID], f.ID)
	}
	projectsOf := map[string][]*Project{}
	for _, p := range l.index.Projects {
		if p.Deleted {
			continue
		}
		projectsOf[p.FolderID] = append(projectsOf[p.FolderID], cloneProject(p))
	}
	l.mu.RUnlock()

	if nodes[id] == nil {
		return nil, ErrNotFound
	}

	var copyTree func(oldID, newParent string) (string, error)
	copyTree = func(oldID, newParent string) (string, error) {
		node := nodes[oldID]
		created, err := l.CreateFolder(node.folder.Name, newParent)
		if err != nil {
			return "", err
		}
		for _, childID := range childrenOf[oldID] {
			if _, err := copyTree(childID, created.ID); err != nil {
				return "", err
			}
		}
		for _, p := range projectsOf[oldID] {
			if _, err := l.CopyProject(p.ID, created.ID); err != nil {
				return "", err
			}
		}
		return created.ID, nil
	}
	newID, err := copyTree(id, parentID)
	if err != nil {
		return nil, err
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	cp := *l.index.Folders[newID]
	return &cp, nil
}

// ---- 项目：移动 / 复制 / 回收站 ----

// uniqueTitleLocked 在文件夹内生成不重名的项目标题。
func (l *Library) uniqueTitleLocked(folderID, title, excludeID string) string {
	base := strings.TrimSpace(title)
	if base == "" {
		base = "未命名项目"
	}
	taken := map[string]bool{}
	for _, p := range l.index.Projects {
		if p.Deleted || p.FolderID != folderID || p.ID == excludeID {
			continue
		}
		taken[strings.ToLower(p.Title)] = true
	}
	if !taken[strings.ToLower(base)] {
		return base
	}
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s (%d)", base, i)
		if !taken[strings.ToLower(cand)] {
			return cand
		}
	}
	return base
}

// mutateProjects 批量修改项目并落盘（单次 Save）。
func (l *Library) mutateProjects(ids []string, fn func(p *Project)) error {
	now := time.Now()
	var snapshots []*Project
	l.mu.Lock()
	for _, id := range ids {
		p, ok := l.index.Projects[id]
		if !ok {
			continue
		}
		fn(p)
		p.UpdatedAt = now
		snapshots = append(snapshots, cloneProject(p))
	}
	l.mu.Unlock()
	for _, p := range snapshots {
		if err := l.persistProject(p); err != nil {
			return err
		}
	}
	return l.Save()
}

// MoveProjects 把项目移动到目标文件夹（空字符串表示未分类）。
func (l *Library) MoveProjects(ids []string, folderID string) error {
	if folderID != "" {
		l.mu.RLock()
		f, ok := l.index.Folders[folderID]
		l.mu.RUnlock()
		if !ok || f.Deleted {
			return ErrNotFound
		}
	}
	return l.mutateProjects(ids, func(p *Project) {
		p.FolderID = folderID
	})
}

// TrashProjects 把项目移入回收站。
func (l *Library) TrashProjects(ids []string) error {
	now := time.Now()
	return l.mutateProjects(ids, func(p *Project) {
		if p.Deleted {
			return
		}
		p.Deleted = true
		p.DeletedAt = now
	})
}

// RestoreProjects 从回收站还原项目。
func (l *Library) RestoreProjects(ids []string) error {
	return l.mutateProjects(ids, func(p *Project) {
		if !p.Deleted {
			return
		}
		p.Deleted = false
		p.DeletedAt = time.Time{}
		// 原文件夹已不存在时回到根目录。
		if p.FolderID != "" {
			if f, ok := l.index.Folders[p.FolderID]; !ok || f.Deleted {
				p.FolderID = ""
			}
		}
	})
}

// DeleteProjectsPermanently 彻底删除项目（含项目目录）。
func (l *Library) DeleteProjectsPermanently(ids []string) error {
	for _, id := range ids {
		_ = l.DeleteProject(id)
	}
	return nil
}

// EmptyTrash 清空回收站：彻底删除所有已删除项目，并移除已删除文件夹记录。
func (l *Library) EmptyTrash() (int, error) {
	l.mu.RLock()
	var ids []string
	for id, p := range l.index.Projects {
		if p.Deleted {
			ids = append(ids, id)
		}
	}
	l.mu.RUnlock()
	for _, id := range ids {
		_ = l.DeleteProject(id)
	}
	l.mu.Lock()
	for id, f := range l.index.Folders {
		if f.Deleted {
			delete(l.index.Folders, id)
		}
	}
	l.mu.Unlock()
	return len(ids), l.Save()
}

// CopyProject 复制一个项目到目标文件夹（含项目目录与字幕/成片文件）。
func (l *Library) CopyProject(id, folderID string) (*Project, error) {
	src, err := l.GetProject(id)
	if err != nil {
		return nil, err
	}
	if folderID != "" {
		l.mu.RLock()
		f, ok := l.index.Folders[folderID]
		l.mu.RUnlock()
		if !ok || f.Deleted {
			return nil, ErrNotFound
		}
	}
	newID := data.NewID("p_")
	srcDir := l.projectDir(id)
	dstDir := l.projectDir(newID)
	if err := copyDir(srcDir, dstDir); err != nil {
		_ = data.RemoveDir(dstDir)
		return nil, fmt.Errorf("复制项目失败：%w", err)
	}

	l.mu.Lock()
	title := l.uniqueTitleLocked(folderID, src.Title+" - 副本", "")
	l.mu.Unlock()

	now := time.Now()
	copied := cloneProject(src)
	copied.ID = newID
	copied.Title = title
	copied.FolderID = folderID
	copied.Deleted = false
	copied.DeletedAt = time.Time{}
	copied.Favorite = false
	copied.Jobs = nil
	copied.CreatedAt = now
	copied.UpdatedAt = now
	copied.OpenedAt = now
	if copied.Media != nil {
		copied.PrimaryID = copied.Media.ID
	}

	if err := l.persistProject(copied); err != nil {
		_ = data.RemoveDir(dstDir)
		return nil, err
	}
	l.mu.Lock()
	l.index.Projects[newID] = copied
	l.mu.Unlock()
	if err := l.Save(); err != nil {
		return nil, err
	}
	return l.GetProject(newID)
}

// CopyProjects 批量复制。
func (l *Library) CopyProjects(ids []string, folderID string) ([]*Project, error) {
	out := make([]*Project, 0, len(ids))
	for _, id := range ids {
		p, err := l.CopyProject(id, folderID)
		if err != nil {
			return out, err
		}
		out = append(out, p)
	}
	return out, nil
}

// copyDir 递归复制目录（只复制普通文件）。
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return data.CopyFile(path, target, nil)
	})
}

// ---- 计数 ----

// Counts 是导航面板需要的统计数字。
type Counts struct {
	Total        int            `json:"total"`
	Unfiled      int            `json:"unfiled"`
	Trash        int            `json:"trash"`
	Favorites    int            `json:"favorites"`
	Recent       int            `json:"recent"`
	Ready        int            `json:"ready"`
	Processing   int            `json:"processing"`
	Failed       int            `json:"failed"`
	Folders      map[string]int `json:"folders"`
	TrashFolders int            `json:"trashFolders"`
}

// Counts 汇总各视图 / 文件夹的项目数量。
func (l *Library) Counts() Counts {
	c := Counts{Folders: map[string]int{}}
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, p := range l.index.Projects {
		if p.Temporary {
			continue
		}
		if p.Deleted {
			c.Trash++
			continue
		}
		c.Total++
		if p.FolderID == "" {
			c.Unfiled++
		} else {
			c.Folders[p.FolderID]++
		}
		if p.Favorite {
			c.Favorites++
		}
		switch p.Status {
		case StatusReady:
			c.Ready++
		case StatusProcessing:
			c.Processing++
		case StatusFailed:
			c.Failed++
		}
		at := p.OpenedAt
		if at.IsZero() {
			at = p.UpdatedAt
		}
		if time.Since(at) <= 30*24*time.Hour {
			c.Recent++
		}
	}
	for _, f := range l.index.Folders {
		if f.Deleted {
			c.TrashFolders++
		}
	}
	return c
}

// ---- 项目集（保留数据模型，界面暂不强调） ----

// ListCollections 返回全部项目集。
func (l *Library) ListCollections() []*Collection {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]*Collection, 0, len(l.index.Collections))
	for _, c := range l.index.Collections {
		cp := *c
		out = append(out, &cp)
	}
	return out
}

// CreateCollection 新建项目集。
func (l *Library) CreateCollection(name string) (*Collection, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("项目集名称不能为空")
	}
	c := &Collection{ID: data.NewID("c_"), Name: name, CreatedAt: time.Now()}
	l.mu.Lock()
	l.index.Collections[c.ID] = c
	l.mu.Unlock()
	if err := l.Save(); err != nil {
		return nil, err
	}
	cp := *c
	return &cp, nil
}

// RenameCollection 重命名项目集。
func (l *Library) RenameCollection(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("项目集名称不能为空")
	}
	l.mu.Lock()
	c, ok := l.index.Collections[id]
	if ok {
		c.Name = name
	}
	l.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	return l.Save()
}

// DeleteCollection 删除项目集：从各项目中移除该集引用。
func (l *Library) DeleteCollection(id string) error {
	l.mu.Lock()
	if _, ok := l.index.Collections[id]; !ok {
		l.mu.Unlock()
		return ErrNotFound
	}
	delete(l.index.Collections, id)
	for _, p := range l.index.Projects {
		if len(p.CollectionIDs) == 0 {
			continue
		}
		kept := p.CollectionIDs[:0]
		for _, cid := range p.CollectionIDs {
			if cid != id {
				kept = append(kept, cid)
			}
		}
		p.CollectionIDs = kept
	}
	l.mu.Unlock()
	return l.Save()
}

// ---- 附件 ----

// AddAttachment 给项目追加一个附件（asset 通常来自 Ingest，已进全局索引）。
func (l *Library) AddAttachment(projectID string, asset *Asset, category string) (*Project, error) {
	if asset == nil {
		return nil, errors.New("library: 缺少附件")
	}
	if category == "" || category == "auto" {
		category = guessCategory(asset)
	}
	return l.UpdateProject(projectID, func(p *Project) {
		p.Attachments = append(p.Attachments, &Attachment{Asset: *asset, Category: category})
	})
}

// RemoveAttachment 移除项目附件并回收无引用的素材文件。
func (l *Library) RemoveAttachment(projectID, assetID string) (*Project, error) {
	updated, err := l.UpdateProject(projectID, func(p *Project) {
		kept := make([]*Attachment, 0, len(p.Attachments))
		for _, at := range p.Attachments {
			if at == nil || at.ID != assetID {
				kept = append(kept, at)
			}
		}
		p.Attachments = kept
	})
	if err != nil {
		return nil, err
	}
	l.gcAssetsLocked()
	if err := l.Save(); err != nil {
		return nil, err
	}
	return updated, nil
}
