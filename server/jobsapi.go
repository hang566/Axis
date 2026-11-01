package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/local/stt-sub/jobs"
	"github.com/local/stt-sub/library"
)

// jsonMarshal / jsonUnmarshal 是给泛型参数搬运用的小包装。
func jsonMarshal(v any) ([]byte, error)       { return json.Marshal(v) }
func jsonUnmarshal(raw []byte, v any) error   { return json.Unmarshal(raw, v) }

// ---- 任务接口 ----

func (a *App) handleJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 {
			limit = 50
		}
		filter := jobs.ListFilter{
			ProjectID: q.Get("projectId"),
			Type:      q.Get("type"),
			Active:    q.Get("active") == "1",
			Limit:     limit,
		}
		list := a.Jobs.List(filter)
		writeJSON(w, http.StatusOK, map[string]any{"jobs": list, "active": a.Jobs.ActiveCount()})

	case http.MethodPost:
		var body struct {
			Type      string          `json:"type"`
			ProjectID string          `json:"projectId"`
			ProjectIDs []string       `json:"projectIds"`
			Params    json.RawMessage `json:"params"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, err)
			return
		}
		ids := body.ProjectIDs
		if body.ProjectID != "" {
			ids = append(ids, body.ProjectID)
		}
		if len(ids) == 0 {
			writeError(w, errBad("缺少项目 ID"))
			return
		}
		var created []*jobs.Job
		var skipped []map[string]string
		for _, pid := range ids {
			job, err := a.createJob(r, body.Type, pid, body.Params)
			if err != nil {
				skipped = append(skipped, map[string]string{"id": pid, "reason": err.Error()})
				continue
			}
			created = append(created, job)
		}
		if len(created) == 0 {
			reason := "没有符合条件的项目"
			if len(skipped) > 0 {
				reason = skipped[0]["reason"]
			}
			writeError(w, errBad("没有可执行的任务："+reason))
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"jobs": created, "skipped": skipped})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 GET / POST"})
	}
}

// createJob 创建并立即启动任务。
func (a *App) createJob(r *http.Request, jobType, projectID string, raw json.RawMessage) (*jobs.Job, error) {
	p, err := a.Lib.GetProject(projectID)
	if err != nil {
		return nil, errNotFound("项目不存在或已被删除")
	}
	if a.activeJobFor(projectID) != nil {
		return nil, errConflict("这个项目已有任务在运行，请等待完成或先取消")
	}
	params := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, errBad("任务参数格式不正确：" + err.Error())
		}
	}
	switch jobType {
	case jobs.TypeTranscribe:
		if p.Media == nil {
			return nil, errBad("项目没有可识别的素材")
		}
		if p.Media.Probe != nil && !p.Media.Probe.HasAudio {
			return nil, errBad("这个文件没有音轨，无法识别语音")
		}
		// 允许覆盖已有字幕：默认新建一份，避免误删历史版本。
		if _, ok := params["format"]; !ok {
			params["format"] = a.Settings.Get().OutputFormat
		}
	case jobs.TypeTranscode:
		if p.Media == nil {
			return nil, errBad("项目没有可转码的素材")
		}
	case jobs.TypeExport, jobs.TypeDownload:
		// 无需额外校验
	default:
		return nil, errBad("未知任务类型：" + jobType)
	}

	title := jobTitle(jobType, p)
	job, err := a.Jobs.Create(jobType, projectID, title, params)
	if err != nil {
		return nil, errServer("创建任务失败：" + err.Error())
	}
	_, _ = a.Lib.UpdateProject(projectID, func(pp *library.Project) {
		pp.Status = library.StatusProcessing
		pp.Jobs = append([]library.JobRef{{
			ID:        job.ID,
			Type:      jobType,
			Status:    jobs.StatusPending,
			CreatedAt: job.CreatedAt,
		}}, pp.Jobs...)
		if len(pp.Jobs) > 20 {
			pp.Jobs = pp.Jobs[:20]
		}
	})
	if err := a.Jobs.Start(job.ID); err != nil {
		return nil, errServer("启动任务失败：" + err.Error())
	}
	// 任务完成时同步项目状态。
	go a.watchJob(job.ID, projectID)
	return job, nil
}

// watchJob 在任务结束后把状态写回项目，保证刷新后项目状态正确。
func (a *App) watchJob(jobID, projectID string) {
	ch, unsubscribe := a.Jobs.Subscribe()
	defer unsubscribe()
	ctx, cancel := contextWithTimeout(6 * 60 * 60)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			job, ok := a.Jobs.Get(jobID)
			if !ok || job.Active() {
				continue
			}
			_, _ = a.Lib.UpdateProject(projectID, func(pp *library.Project) {
				for i := range pp.Jobs {
					if pp.Jobs[i].ID == jobID {
						pp.Jobs[i].Status = job.Status
					}
				}
				switch job.Status {
				case jobs.StatusSucceeded:
					pp.Status = library.StatusReady
					pp.LastError = ""
					pp.Kind = kindForJob(job.Type, pp.Kind)
				case jobs.StatusFailed, jobs.StatusInterrupted:
					if len(pp.Subtitles) == 0 && len(pp.Outputs) == 0 {
						pp.Status = library.StatusFailed
					}
					pp.LastError = job.Error
				case jobs.StatusCanceled:
					if len(pp.Subtitles) == 0 && len(pp.Outputs) == 0 {
						pp.Status = library.StatusDraft
					}
				}
			})
			return
		}
	}
}

func kindForJob(jobType, current string) string {
	switch jobType {
	case jobs.TypeTranscribe:
		return library.KindTranscribe
	case jobs.TypeTranscode:
		if current == library.KindTranscribe {
			return current
		}
		return library.KindTranscode
	}
	return current
}

func jobTitle(jobType string, p *library.Project) string {
	switch jobType {
	case jobs.TypeTranscribe:
		return "转写 · " + p.Title
	case jobs.TypeTranscode:
		return "转码 · " + p.Title
	case jobs.TypeExport:
		return "导出 · " + p.Title
	case jobs.TypeDownload:
		return "下载模型"
	}
	return p.Title
}

// ---- 单个任务 ----

func (a *App) handleJobRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	jobID := parts[0]
	if jobID == "" {
		writeError(w, errBad("缺少任务 ID"))
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch action {
	case "":
		job, ok := a.Jobs.Get(jobID)
		if !ok {
			writeError(w, errNotFound("任务不存在"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"job": job, "logs": a.Jobs.Logs(jobID, 200)})

	case "cancel":
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 POST"})
			return
		}
		if err := a.Jobs.Cancel(jobID); err != nil {
			writeError(w, errConflict(err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"canceled": true})

	case "retry":
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 POST"})
			return
		}
		old, ok := a.Jobs.Get(jobID)
		if !ok {
			writeError(w, errNotFound("任务不存在"))
			return
		}
		params, _ := json.Marshal(old.Params)
		job, err := a.createJob(r, old.Type, old.ProjectID, params)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"job": job})

	case "logs":
		writeJSON(w, http.StatusOK, map[string]any{"logs": a.Jobs.Logs(jobID, 500)})

	default:
		writeError(w, errNotFound("未知任务操作：" + action))
	}
}

// ---- SSE 事件流 ----

// handleEvents 推送任务变化与心跳。前端只把它当「有更新」的信号，数据仍走 REST。
func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, errServer("当前连接不支持流式输出"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsubscribe := a.Jobs.Subscribe()
	defer unsubscribe()

	send := func(event string, payload any) bool {
		raw, err := json.Marshal(payload)
		if err != nil {
			return true
		}
		if _, err := w.Write([]byte("event: " + event + "\ndata: " + string(raw) + "\n\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// 首帧推送当前快照，前端无需额外请求。
	if !send("snapshot", map[string]any{
		"jobs":   a.Jobs.List(jobs.ListFilter{Limit: 30}),
		"active": a.Jobs.ActiveCount(),
	}) {
		return
	}

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-ch:
			if !send("jobs", map[string]any{
				"jobs":   a.Jobs.List(jobs.ListFilter{Limit: 30}),
				"active": a.Jobs.ActiveCount(),
			}) {
				return
			}
		}
	}
}

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
