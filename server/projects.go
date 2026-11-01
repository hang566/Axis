package server

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/local/stt-sub/data"
	"github.com/local/stt-sub/jobs"
	"github.com/local/stt-sub/library"
	"github.com/local/stt-sub/media"
	"github.com/local/stt-sub/subtitle"
)

// libraryListAll 返回「全部非临时项目」的过滤条件。
func libraryListAll() library.ProjectListOptions {
	return library.ProjectListOptions{Sort: "recent"}
}

// ---- 项目列表 / 创建 ----

func (a *App) handleProjects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		offset, _ := strconv.Atoi(q.Get("offset"))
		view := strings.TrimSpace(q.Get("view"))
		if q.Get("deleted") == "1" {
			view = library.ViewTrash
		}
		if view == "" {
			view = library.ViewAll
		}
		opts := library.ProjectListOptions{
			Query:        q.Get("q"),
			Kind:         q.Get("kind"),
			Favorite:     q.Get("favorite") == "1",
			Temporary:    q.Get("temporary") == "1",
			FolderID:     q.Get("folderId"),
			CollectionID: q.Get("collectionId"),
			View:         view,
			Sort:         q.Get("sort"),
			Desc:         q.Get("desc") == "1",
			Limit:        limit,
			Offset:       offset,
		}
		list := a.Lib.ListProjects(opts)
		// 附带每个项目最近任务状态，便于列表里显示进度条。
		type row struct {
			*library.Project
			ActiveJob *jobs.Job `json:"activeJob,omitempty"`
		}
		rows := make([]row, 0, len(list))
		for _, p := range list {
			active := a.activeJobFor(p.ID)
			rows = append(rows, row{Project: p, ActiveJob: active})
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": rows, "count": len(rows)})

	case http.MethodPost:
		var body struct {
			AssetID   string `json:"assetId"`
			Title     string `json:"title"`
			Kind      string `json:"kind"`
			Language  string `json:"language"`
			Temporary bool   `json:"temporary"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, err)
			return
		}
		asset, ok := a.Lib.Asset(body.AssetID)
		if !ok {
			writeError(w, errNotFound("素材不存在，请重新上传"))
			return
		}
		kind := normalizeKind(body.Kind)
		var opts []library.ProjectOption
		if body.Temporary {
			opts = append(opts, library.WithTemporary())
		}
		if body.Language != "" {
			opts = append(opts, library.WithLanguage(body.Language))
		}
		p, err := a.Lib.CreateProject(r.Context(), body.Title, kind, asset, asset.Probe, opts...)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"project": p})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 GET / POST"})
	}
}

func normalizeKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case library.KindTranscode:
		return library.KindTranscode
	case library.KindInfo:
		return library.KindInfo
	default:
		return library.KindTranscribe
	}
}

// activeJobFor 找项目当前正在跑的任务。
func (a *App) activeJobFor(projectID string) *jobs.Job {
	list := a.Jobs.List(jobs.ListFilter{ProjectID: projectID, Active: true, Limit: 1})
	if len(list) == 0 {
		return nil
	}
	return list[0]
}

// ---- 上传导入 ----

func (a *App) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 POST"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, errBad("读取上传内容失败（文件过大或网络中断）："+err.Error()))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, errBad("没有收到文件（字段名应为 file）"))
		return
	}
	defer file.Close()

	// 先落到临时目录，边收边写，超大文件也不占内存。
	tmpDir := a.Lib.TempDir()
	tmp, err := os.CreateTemp(tmpDir, "upload-*"+strings.ToLower(filepath.Ext(header.Filename)))
	if err != nil {
		writeError(w, errServer("创建临时文件失败："+err.Error()))
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
		writeError(w, errBad("接收文件失败："+err.Error()))
		return
	}
	if err := tmp.Close(); err != nil {
		writeError(w, errServer("写入临时文件失败："+err.Error()))
		return
	}

	probe, probeErr := a.Media.Probe(r.Context(), tmpPath)
	if probeErr != nil {
		writeError(w, errBad(probeErr.Error()))
		return
	}
	info := toProbeInfo(probe)

	asset, err := a.Lib.Ingest(r.Context(), library.IngestOptions{
		SourcePath: tmpPath,
		FileName:   header.Filename,
		Origin:     "upload",
		Copy:       true,
		Probe:      info,
	})
	if err != nil {
		writeError(w, errServer("保存素材失败："+err.Error()))
		return
	}
	removeTmp = false // 已复制进素材库，交给 defer 清理临时文件即可
	_ = os.Remove(tmpPath)
	removeTmp = false

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename))
		if probe.Title() != "" {
			title = probe.Title()
		}
	}
	temporary := r.FormValue("temporary") == "1"
	// 临时工作区重复导入同一个文件时复用原项目，避免「当前媒体」堆出一串重复条目。
	if temporary {
		if existing := a.Lib.FindProjectByAsset(asset.ID, true); existing != nil {
			a.Lib.TouchProject(existing.ID)
			writeJSON(w, http.StatusOK, map[string]any{
				"project": existing,
				"asset":   asset,
				"probe":   probe,
				"reused":  true,
			})
			return
		}
	}
	var opts []library.ProjectOption
	if temporary {
		opts = append(opts, library.WithTemporary())
	}
	if lang := strings.TrimSpace(r.FormValue("language")); lang != "" {
		opts = append(opts, library.WithLanguage(lang))
	}
	project, err := a.Lib.CreateProject(r.Context(), title, normalizeKind(r.FormValue("kind")), asset, info, opts...)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"project": project,
		"asset":   asset,
		"probe":   probe,
	})
}

// toProbeInfo 把完整探测结果投影成资料库里保存的精简信息。
func toProbeInfo(p *media.ProbeResult) *library.ProbeInfo {
	if p == nil {
		return nil
	}
	info := &library.ProbeInfo{
		FormatName: p.Format.FormatName,
		LongName:   p.Format.LongName,
		Duration:   p.Format.Duration,
		Size:       p.Format.Size,
		BitRate:    p.Format.BitRate,
		HasAudio:   p.HasAudio(),
		HasVideo:   p.HasVideo(),
	}
	if v, ok := p.FirstStream("video"); ok {
		info.VideoCodec = v.Codec
		info.Width = v.Width
		info.Height = v.Height
		info.FrameRate = v.FrameRate
		info.VideoBitRate = v.BitRate
	}
	if au, ok := p.FirstStream("audio"); ok {
		info.AudioCodec = au.Codec
		info.SampleRate = au.SampleRate
		info.Channels = au.Channels
	}
	return info
}

// ---- 单个项目 ----

func (a *App) handleProjectRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/projects/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, errBad("缺少项目 ID"))
		return
	}
	projectID := parts[0]
	action := ""
	subID := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	if len(parts) > 2 {
		subID = parts[2]
	}

	p, err := a.Lib.GetProject(projectID)
	if err != nil {
		writeError(w, errNotFound("项目不存在或已被删除"))
		return
	}

	switch action {
	case "":
		switch r.Method {
		case http.MethodGet:
			a.Lib.TouchProject(projectID)
			writeJSON(w, http.StatusOK, map[string]any{
				"project": p,
				"jobs":    a.Jobs.List(jobs.ListFilter{ProjectID: projectID, Limit: 30}),
				"active":  a.activeJobFor(projectID),
			})
		case http.MethodPatch, http.MethodPut:
			var body struct {
				Title         *string   `json:"title"`
				Notes         *string   `json:"notes"`
				Favorite      *bool     `json:"favorite"`
				Language      *string   `json:"language"`
				Temporary     *bool     `json:"temporary"`
				LastError     *string   `json:"lastError"`
				FolderID      *string   `json:"folderId"`
				Deleted       *bool     `json:"deleted"`
				CollectionIDs []string  `json:"collectionIds"`
			}
			if err := decodeJSON(r, &body); err != nil {
				writeError(w, err)
				return
			}
			// 归属与回收站走专门方法，保证「文件夹必须存在」「删除可还原」等约束。
			if body.FolderID != nil {
				if err := a.Lib.MoveProjects([]string{projectID}, *body.FolderID); err != nil {
					writeError(w, err)
					return
				}
			}
			if body.Deleted != nil {
				var err error
				if *body.Deleted {
					err = a.Lib.TrashProjects([]string{projectID})
				} else {
					err = a.Lib.RestoreProjects([]string{projectID})
				}
				if err != nil {
					writeError(w, err)
					return
				}
			}
			updated, err := a.Lib.UpdateProject(projectID, func(pp *library.Project) {
				if body.Title != nil && strings.TrimSpace(*body.Title) != "" {
					pp.Title = strings.TrimSpace(*body.Title)
				}
				if body.Notes != nil {
					pp.Notes = *body.Notes
				}
				if body.Favorite != nil {
					pp.Favorite = *body.Favorite
				}
				if body.Language != nil {
					pp.Language = *body.Language
				}
				if body.Temporary != nil {
					pp.Temporary = *body.Temporary
				}
				if body.LastError != nil {
					pp.LastError = *body.LastError
				}
				if body.CollectionIDs != nil {
					pp.CollectionIDs = body.CollectionIDs
				}
			})
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"project": updated})
		case http.MethodDelete:
			if a.activeJobFor(projectID) != nil {
				writeError(w, errConflict("项目还有任务在运行，请先取消任务再删除"))
				return
			}
			// 默认进回收站（可还原）；?permanent=1 才是彻底删除。
			if r.URL.Query().Get("permanent") == "1" {
				if err := a.Lib.DeleteProject(projectID); err != nil {
					writeError(w, err)
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "permanent": true})
				return
			}
			if err := a.Lib.TrashProjects([]string{projectID}); err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "trash": true})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "不支持的方法"})
		}

	case "subtitle":
		if r.Method == http.MethodDelete {
			a.handleSubtitleDelete(w, p, subID)
			return
		}
		a.handleSubtitleSave(w, r, p, subID)

	case "exports":
		a.handleExports(w, r, p)

	case "probe":
		if p.Media == nil {
			writeError(w, errNotFound("项目没有素材"))
			return
		}
		probe, perr := a.Media.Probe(r.Context(), a.Lib.AssetPath(p.Media))
		if perr != nil {
			writeError(w, errServer("读取媒体信息失败："+perr.Error()))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"probe": probe,
			"raw":   probe.JSON,
			"asset": p.Media,
		})

	case "thumbnail":
		a.handleThumbnail(w, r, p)

	case "attachments":
		if r.Method == http.MethodDelete {
			a.handleAttachmentDelete(w, r, p, subID)
			return
		}
		a.handleAttachmentUpload(w, r, p)

	default:
		writeError(w, errNotFound("未知的项目操作："+action))
	}
}

// handleSubtitleDelete 删除项目中的一份字幕（文件与索引一起清理）。
func (a *App) handleSubtitleDelete(w http.ResponseWriter, p *library.Project, trackID string) {
	if trackID == "" {
		writeError(w, errBad("缺少字幕 ID"))
		return
	}
	track, ok := library.FindSubtitle(p, trackID)
	if !ok {
		writeError(w, errNotFound("字幕不存在"))
		return
	}
	if path := a.Lib.SubtitleAbsPath(p, track); path != "" {
		_ = os.Remove(path)
	}
	updated, err := a.Lib.UpdateProject(p.ID, func(pp *library.Project) {
		kept := pp.Subtitles[:0]
		for _, t := range pp.Subtitles {
			if t.ID != trackID {
				kept = append(kept, t)
			}
		}
		pp.Subtitles = kept
		if len(pp.Subtitles) == 0 && len(pp.Outputs) == 0 {
			pp.Status = library.StatusDraft
		}
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "project": updated})
}

// handleSubtitleSave 保存/更新字幕（时间轴编辑器直接提交 cues）。
func (a *App) handleSubtitleSave(w http.ResponseWriter, r *http.Request, p *library.Project, subID string) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 POST"})
		return
	}
	var body struct {
		ID       string             `json:"id"`
		Cues     []subtitle.TrackCue `json:"cues"`
		Format   string             `json:"format"`
		Language string             `json:"language"`
		FileName string             `json:"fileName"`
		Content  string             `json:"content"` // 直接提交字幕文本（导入场景）
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	format := strings.ToLower(strings.TrimSpace(body.Format))
	if format == "" {
		format = "srt"
	}
	var cues []subtitle.TrackCue
	if body.Content != "" {
		parsed, err := subtitle.Parse([]byte(body.Content))
		if err != nil {
			writeError(w, errBad("字幕内容解析失败："+err.Error()))
			return
		}
		cues = parsed
	} else {
		cues = body.Cues
	}
	if len(cues) == 0 {
		writeError(w, errBad("字幕内容为空"))
		return
	}
	// 规范化：排序、去重叠、时间不越界。
	sortCues(cues)
	cues = subtitle.MergeOverlaps(cues, 0.02)
	if p.Duration > 0 {
		for i := range cues {
			if cues[i].End > p.Duration+0.5 {
				cues[i].End = p.Duration
			}
			if cues[i].Start > cues[i].End {
				cues[i].Start = cues[i].End
			}
		}
	}
	payload, _, err := subtitle.ToBytes(cues, format)
	if err != nil {
		writeError(w, err)
		return
	}
	trackID := strings.TrimSpace(body.ID)
	if subID != "" {
		trackID = subID
	}
	track := library.SubtitleTrack{
		ID:       trackID,
		Format:   format,
		Language: body.Language,
		CueCount: len(cues),
		Edited:   true,
		Duration: p.Duration,
	}
	if base := strings.TrimSpace(body.FileName); base != "" {
		track.FileName = base
	} else if existing, ok := findTrack(p, trackID); ok {
		track.FileName = existing.FileName
		track.Provider = existing.Provider
		track.Model = existing.Model
		track.Language = firstNonEmptyStr(body.Language, existing.Language)
	} else {
		track.FileName = p.Title + "-字幕"
		track.Provider = "手工编辑"
	}
	updated, err := a.Lib.SaveSubtitleTrack(p.ID, track, payload)
	if err != nil {
		writeError(w, errServer("保存字幕失败："+err.Error()))
		return
	}
	saved, _ := library.FindSubtitle(updated, trackID)
	writeJSON(w, http.StatusOK, map[string]any{"project": updated, "track": saved})
}

func findTrack(p *library.Project, id string) (library.SubtitleTrack, bool) {
	if id == "" {
		return library.SubtitleTrack{}, false
	}
	return library.FindSubtitle(p, id)
}

func sortCues(cues []subtitle.TrackCue) {
	for i := 1; i < len(cues); i++ {
		for j := i; j > 0 && cues[j].Start < cues[j-1].Start; j-- {
			cues[j], cues[j-1] = cues[j-1], cues[j]
		}
	}
}

// handleExports 输出字幕文件（浏览器下载）或返回纯文本内容。
func (a *App) handleExports(w http.ResponseWriter, r *http.Request, p *library.Project) {
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "srt"
	}
	trackID := r.URL.Query().Get("track")
	var track library.SubtitleTrack
	var ok bool
	if trackID != "" {
		track, ok = library.FindSubtitle(p, trackID)
	} else if len(p.Subtitles) > 0 {
		track, ok = p.Subtitles[0], true
	}
	if !ok {
		writeError(w, errNotFound("这个项目还没有字幕，请先转写或导入字幕"))
		return
	}
	raw, err := a.Lib.ReadSubtitle(p, track)
	if err != nil {
		writeError(w, errServer("读取字幕失败："+err.Error()))
		return
	}
	cues, perr := subtitle.Parse(raw)
	if perr != nil {
		writeError(w, errServer("字幕文件损坏："+perr.Error()))
		return
	}
	body, ext, err := subtitle.ToBytes(cues, format)
	if err != nil {
		writeError(w, err)
		return
	}
	if r.URL.Query().Get("inline") == "1" {
		writeJSON(w, http.StatusOK, map[string]any{"content": string(body), "format": ext})
		return
	}
	name := data.SafeName(strings.TrimSuffix(track.FileName, filepath.Ext(track.FileName)))
	if name == "" || name == "file" {
		name = p.Title
	}
	w.Header().Set("Content-Type", exportContentType(ext))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"; filename*=UTF-8''%s`,
		asciiFallback(name), ext, urlEncode(name+"."+ext)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func exportContentType(ext string) string {
	switch ext {
	case "srt":
		return "application/x-subrip; charset=utf-8"
	case "vtt":
		return "text/vtt; charset=utf-8"
	default:
		return "application/json; charset=utf-8"
	}
}

func asciiFallback(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 128 && r != '"' && r != '\\' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "subtitle"
	}
	return b.String()
}

func urlEncode(s string) string {
	var b strings.Builder
	for _, r := range []byte(s) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			b.WriteByte(r)
		} else {
			b.WriteString(fmt.Sprintf("%%%02X", r))
		}
	}
	return b.String()
}

// handleThumbnail 抽取封面帧（磁盘缓存，避免重复解码）。
func (a *App) handleThumbnail(w http.ResponseWriter, r *http.Request, p *library.Project) {
	if p.Media == nil {
		writeError(w, errNotFound("项目没有素材"))
		return
	}
	at := 0.0
	if v := r.URL.Query().Get("at"); v != "" {
		at, _ = strconv.ParseFloat(v, 64)
	}
	if p.Duration > 1 && at >= p.Duration {
		at = p.Duration / 2
	}
	cacheDir := filepath.Join(a.Lib.Root(), data.TempDir, "thumbs")
	_ = os.MkdirAll(cacheDir, 0o755)
	cache := filepath.Join(cacheDir, p.ID+"_"+strconv.Itoa(int(at))+".jpg")
	if st, err := os.Stat(cache); err == nil && st.Size() > 0 {
		http.ServeFile(w, r, cache)
		return
	}
	if err := a.Media.Thumbnail(r.Context(), a.Lib.AssetPath(p.Media), cache, time.Duration(at*float64(time.Second)), 720); err != nil {
		writeError(w, errServer("抽取封面失败："+err.Error()))
		return
	}
	http.ServeFile(w, r, cache)
}

// ---- 素材流（支持 Range，供播放器拖动） ----

func (a *App) handleAsset(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/assets/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, errBad("缺少素材 ID"))
		return
	}
	assetID := parts[0]
	mode := ""
	if len(parts) > 1 {
		mode = parts[1]
	}
	asset, ok := a.Lib.Asset(assetID)
	if !ok {
		// 转码产物只挂在项目 Outputs 上，不在全局索引里，回退查找。
		asset, ok = a.Lib.OutputAsset(assetID)
	}
	if !ok {
		writeError(w, errNotFound("素材不存在"))
		return
	}
	path := a.Lib.AssetPath(asset)
	// meta 模式只返回元数据与「文件是否还在」，供播放页在打开前判断，
	// 避免前端直接请求媒体地址时看到一段裸 JSON 错误。
	if mode == "meta" || r.URL.Query().Get("meta") == "1" {
		exists := true
		if info, statErr := os.Stat(path); statErr != nil || info.IsDir() {
			exists = false
		}
		writeJSON(w, http.StatusOK, map[string]any{"asset": asset, "exists": exists})
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, errNotFound("素材文件丢失"))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeError(w, errServer("读取素材失败"))
		return
	}

	if mode == "media" {
		// 浏览器能直接播的容器/编码才内联播放，其余提示先转码。
		w.Header().Set("Content-Type", mediaContentType(asset.FileName))
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeContent(w, r, asset.FileName, st.ModTime(), f)
		return
	}
	if mode == "download" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
			asciiFallback(asset.FileName), urlEncode(asset.FileName)))
		http.ServeContent(w, r, asset.FileName, st.ModTime(), f)
		return
	}
	writeJSON(w, http.StatusOK, asset)
}

func mediaContentType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	switch ext {
	case ".mkv":
		return "video/x-matroska"
	case ".m4a":
		return "audio/mp4"
	case ".flac":
		return "audio/flac"
	case ".opus":
		return "audio/opus"
	default:
		return "application/octet-stream"
	}
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
