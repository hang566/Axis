package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/local/stt-sub/library"
)

// ---- 文件夹 ----

func (a *App) handleFolders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// 一次返回文件夹树、已删除文件夹与各位置计数，前端无需再单独统计。
		writeJSON(w, http.StatusOK, map[string]any{
			"folders":      a.Lib.ListFolders(),
			"trashFolders": a.Lib.ListTrashedFolders(),
			"counts":       a.Lib.Counts(),
		})
	case http.MethodPost:
		var body struct {
			Name     string `json:"name"`
			ParentID string `json:"parentId"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, err)
			return
		}
		f, err := a.Lib.CreateFolder(body.Name, body.ParentID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"folder": f})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 GET / POST"})
	}
}

func (a *App) handleFolderRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/folders/"), "/")
	parts := strings.Split(rest, "/")
	id := parts[0]
	if id == "" {
		writeError(w, errBad("缺少文件夹 ID"))
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	// 复制文件夹：POST /api/folders/{id}/copy {parentId}
	if action == "copy" {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 POST"})
			return
		}
		var body struct {
			ParentID string `json:"parentId"`
		}
		_ = decodeJSON(r, &body)
		f, err := a.Lib.CopyFolder(id, body.ParentID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"folder": f})
		return
	}

	switch r.Method {
	case http.MethodGet:
		f, ok := findFolder(a, id)
		if !ok {
			writeError(w, errNotFound("文件夹不存在"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"folder": f})
	case http.MethodPatch, http.MethodPut:
		var body struct {
			Name string `json:"name"`
			// ParentID 为指针：区分「未提供」与「移动到根目录（空串）」。
			ParentID *string `json:"parentId"`
			// Deleted 为指针：true 进回收站，false 还原。
			Deleted *bool `json:"deleted"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, err)
			return
		}
		if body.Name != "" {
			if err := a.Lib.RenameFolder(id, body.Name); err != nil {
				writeError(w, err)
				return
			}
		}
		if body.ParentID != nil {
			if err := a.Lib.MoveFolder(id, *body.ParentID); err != nil {
				writeError(w, err)
				return
			}
		}
		if body.Deleted != nil {
			var err error
			if *body.Deleted {
				err = a.Lib.TrashFolder(id)
			} else {
				err = a.Lib.RestoreFolder(id)
			}
			if err != nil {
				writeError(w, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case http.MethodDelete:
		// 默认进回收站；?permanent=1 才彻底删除。
		if r.URL.Query().Get("permanent") == "1" {
			if err := a.Lib.DeleteFolder(id); err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "permanent": true})
			return
		}
		if err := a.Lib.TrashFolder(id); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "trash": true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "不支持的方法"})
	}
}

func findFolder(a *App, id string) (*library.Folder, bool) {
	for _, f := range a.Lib.ListFolders() {
		if f.ID == id {
			return f, true
		}
	}
	for _, f := range a.Lib.ListTrashedFolders() {
		if f.ID == id {
			return f, true
		}
	}
	return nil, false
}

// ---- 批量操作（资源管理器的剪切/复制/粘贴、删除、还原） ----

// BatchRequest 是 /api/library/batch 的请求体。
type BatchRequest struct {
	Action         string   `json:"action"` // move / copy / trash / restore / delete / favorite
	ProjectIDs     []string `json:"projectIds"`
	FolderIDs      []string `json:"folderIds"`
	TargetFolderID string   `json:"targetFolderId"`
	Favorite       *bool    `json:"favorite"`
}

// handleBatch 统一处理多选后的批量操作。
func (a *App) handleBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 POST"})
		return
	}
	var body BatchRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	// 有任务在跑的项目不允许删除/移动，避免把正在写的目录搬走。
	if body.Action == "trash" || body.Action == "delete" || body.Action == "move" {
		for _, id := range body.ProjectIDs {
			if a.activeJobFor(id) != nil {
				writeError(w, errConflict("有项目正在处理中，请先取消任务"))
				return
			}
		}
	}

	affected := 0
	switch body.Action {
	case "move":
		if err := a.Lib.MoveProjects(body.ProjectIDs, body.TargetFolderID); err != nil {
			writeError(w, err)
			return
		}
		for _, id := range body.FolderIDs {
			if err := a.Lib.MoveFolder(id, body.TargetFolderID); err != nil {
				writeError(w, err)
				return
			}
		}
		affected = len(body.ProjectIDs) + len(body.FolderIDs)

	case "copy":
		projects, err := a.Lib.CopyProjects(body.ProjectIDs, body.TargetFolderID)
		if err != nil {
			writeError(w, err)
			return
		}
		for _, id := range body.FolderIDs {
			if _, err := a.Lib.CopyFolder(id, body.TargetFolderID); err != nil {
				writeError(w, err)
				return
			}
		}
		affected = len(projects) + len(body.FolderIDs)

	case "trash":
		if err := a.Lib.TrashProjects(body.ProjectIDs); err != nil {
			writeError(w, err)
			return
		}
		for _, id := range body.FolderIDs {
			if err := a.Lib.TrashFolder(id); err != nil {
				writeError(w, err)
				return
			}
		}
		affected = len(body.ProjectIDs) + len(body.FolderIDs)

	case "restore":
		if err := a.Lib.RestoreProjects(body.ProjectIDs); err != nil {
			writeError(w, err)
			return
		}
		for _, id := range body.FolderIDs {
			if err := a.Lib.RestoreFolder(id); err != nil {
				writeError(w, err)
				return
			}
		}
		affected = len(body.ProjectIDs) + len(body.FolderIDs)

	case "delete":
		if err := a.Lib.DeleteProjectsPermanently(body.ProjectIDs); err != nil {
			writeError(w, err)
			return
		}
		for _, id := range body.FolderIDs {
			if err := a.Lib.DeleteFolder(id); err != nil {
				writeError(w, err)
				return
			}
		}
		affected = len(body.ProjectIDs) + len(body.FolderIDs)

	case "favorite":
		fav := true
		if body.Favorite != nil {
			fav = *body.Favorite
		}
		for _, id := range body.ProjectIDs {
			if _, err := a.Lib.UpdateProject(id, func(p *library.Project) { p.Favorite = fav }); err != nil {
				writeError(w, err)
				return
			}
			affected++
		}

	default:
		writeError(w, errBad("未知的批量操作："+body.Action))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"action":   body.Action,
		"affected": affected,
		"counts":   a.Lib.Counts(),
	})
}

// handleEmptyTrash 清空回收站。
func (a *App) handleEmptyTrash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 POST"})
		return
	}
	n, err := a.Lib.EmptyTrash()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": n})
}

// ---- 项目集 ----

func (a *App) handleCollections(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"collections": a.Lib.ListCollections()})
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, err)
			return
		}
		c, err := a.Lib.CreateCollection(body.Name)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"collection": c})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 GET / POST"})
	}
}

func (a *App) handleCollectionRoutes(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/collections/"), "/")
	if id == "" {
		writeError(w, errBad("缺少项目集 ID"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		c, ok := findCollection(a, id)
		if !ok {
			writeError(w, errNotFound("项目集不存在"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"collection": c})
	case http.MethodPatch, http.MethodPut:
		var body struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, err)
			return
		}
		if err := a.Lib.RenameCollection(id, body.Name); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case http.MethodDelete:
		if err := a.Lib.DeleteCollection(id); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "不支持的方法"})
	}
}

func findCollection(a *App, id string) (*library.Collection, bool) {
	for _, c := range a.Lib.ListCollections() {
		if c.ID == id {
			return c, true
		}
	}
	return nil, false
}

// ---- 项目附件 ----

// handleAttachmentUpload 上传并追加一个项目附件（音频 / 字幕文件 / 文档等）。
func (a *App) handleAttachmentUpload(w http.ResponseWriter, r *http.Request, p *library.Project) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 POST"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, errBad("读取上传内容失败（文件过大或网络中断）：" + err.Error()))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, errBad("没有收到文件（字段名应为 file）"))
		return
	}
	defer file.Close()

	tmpDir := a.Lib.TempDir()
	tmp, err := os.CreateTemp(tmpDir, "att-*"+strings.ToLower(filepath.Ext(header.Filename)))
	if err != nil {
		writeError(w, errServer("创建临时文件失败：" + err.Error()))
		return
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := io.Copy(tmp, file); err != nil {
		_ = tmp.Close()
		writeError(w, errBad("接收文件失败：" + err.Error()))
		return
	}
	if err := tmp.Close(); err != nil {
		writeError(w, errServer("写入临时文件失败：" + err.Error()))
		return
	}

	// 媒体类文件尝试探测，失败不影响入库（非媒体文件没有 probe 也合法）。
	var info *library.ProbeInfo
	if probe, perr := a.Media.Probe(r.Context(), tmpPath); perr == nil {
		info = toProbeInfo(probe)
	}

	asset, err := a.Lib.Ingest(r.Context(), library.IngestOptions{
		SourcePath: tmpPath,
		FileName:   header.Filename,
		Origin:     "upload",
		Copy:       true,
		Probe:      info,
	})
	if err != nil {
		writeError(w, errServer("保存附件失败：" + err.Error()))
		return
	}
	removeTmp = false
	_ = os.Remove(tmpPath)

	updated, err := a.Lib.AddAttachment(p.ID, asset, r.FormValue("category"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": updated, "asset": asset})
}

// handleAttachmentDelete 删除项目附件（并回收素材文件）。
func (a *App) handleAttachmentDelete(w http.ResponseWriter, r *http.Request, p *library.Project, assetID string) {
	if assetID == "" {
		writeError(w, errBad("缺少附件 ID"))
		return
	}
	updated, err := a.Lib.RemoveAttachment(p.ID, assetID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "project": updated})
}
