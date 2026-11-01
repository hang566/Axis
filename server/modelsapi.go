package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/local/stt-sub/data"
	"github.com/local/stt-sub/jobs"
	"github.com/local/stt-sub/media"
	"github.com/local/stt-sub/store"
)

// Preset 是转码预设，界面直接展示与应用。
type Preset struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Icon        string  `json:"icon"`
	Format      string  `json:"format"`
	VideoCodec  string  `json:"videoCodec"`
	AudioCodec  string  `json:"audioCodec"`
	PresetSpeed string  `json:"speed"`
	CRF         int     `json:"crf"`
	Bitrate     string  `json:"bitrate"`
	AudioBitrate string `json:"audioBitrate"`
	Resolution  string  `json:"resolution"`
	FrameRate   float64 `json:"frameRate"`
	AudioOnly   bool    `json:"audioOnly"`
}

// Presets 是内置转码预设（覆盖绝大多数日常需求）。
var Presets = []Preset{
	{
		ID: "web-mp4", Name: "网页通用 MP4", Icon: "globe",
		Description: "H.264 + AAC，浏览器、手机、电视都能直接播放",
		Format: "mp4", VideoCodec: "libx264", AudioCodec: "aac",
		PresetSpeed: "medium", CRF: 23, AudioBitrate: "160k",
	},
	{
		ID: "high-mp4", Name: "高画质 MP4", Icon: "sparkles",
		Description: "H.264 高码率，适合投稿、归档与二次剪辑",
		Format: "mp4", VideoCodec: "libx264", AudioCodec: "aac",
		PresetSpeed: "slow", CRF: 18, AudioBitrate: "192k",
	},
	{
		ID: "hevc-compact", Name: "H.265 小体积", Icon: "archive",
		Description: "H.265 编码，同画质体积约为 H.264 的 60%",
		Format: "mp4", VideoCodec: "libx265", AudioCodec: "aac",
		PresetSpeed: "medium", CRF: 26, AudioBitrate: "128k",
	},
	{
		ID: "hd720", Name: "720p 适配", Icon: "monitor",
		Description: "降到 720p，适合网课、会议录屏快速分享",
		Format: "mp4", VideoCodec: "libx264", AudioCodec: "aac",
		PresetSpeed: "fast", CRF: 24, AudioBitrate: "128k", Resolution: "720",
	},
	{
		ID: "mkv-remux", Name: "快速封装 MKV", Icon: "shuffle",
		Description: "不重新编码，只换容器，几十秒完成（画质零损失）",
		Format: "mkv", VideoCodec: "copy", AudioCodec: "copy",
	},
	{
		ID: "mp4-remux", Name: "快速封装 MP4", Icon: "shuffle",
		Description: "不重新编码换 MP4，让浏览器能直接播放",
		Format: "mp4", VideoCodec: "copy", AudioCodec: "copy",
	},
	{
		ID: "audio-m4a", Name: "只提取音频 (M4A)", Icon: "audio",
		Description: "抽成 AAC 音频，方便听写、播客与语音备忘",
		Format: "m4a", VideoCodec: "none", AudioCodec: "aac",
		AudioBitrate: "192k", AudioOnly: true,
	},
	{
		ID: "audio-mp3", Name: "只提取音频 (MP3)", Icon: "audio",
		Description: "兼容性最好的音频格式，任何设备都能播",
		Format: "mp3", VideoCodec: "none", AudioCodec: "libmp3lame",
		AudioBitrate: "192k", AudioOnly: true,
	},
}

func init() {
}

// handleTranscodePresets 返回预设与自定义参数预览。
func (a *App) handleTranscodePresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"presets": Presets})
}

// handleTranscodePreview 生成 ffmpeg 命令预览（不执行）。
func (a *App) handleTranscodePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 POST"})
		return
	}
	var body struct {
		ProjectID string          `json:"projectId"`
		Params    TranscodeParams `json:"params"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	p, err := a.Lib.GetProject(body.ProjectID)
	if err != nil || p.Media == nil {
		writeError(w, errNotFound("项目或素材不存在"))
		return
	}
	req, name, err := a.buildTranscodeRequest(a.Lib.AssetPath(p.Media), p.Media.FileName, body.Params)
	if err != nil {
		writeError(w, err)
		return
	}
	// 预览里的输出路径展示为项目 out/ 目录，与真实执行一致。
	previewReq := req
	previewReq.Output = filepath.Join("out", name)
	writeJSON(w, http.StatusOK, map[string]any{
		"command":  media.PreviewCommand(previewReq),
		"fileName": name,
		"outputDir": filepath.ToSlash(filepath.Join("projects", p.ID, "out")),
	})
}

// ---- 模型中心 ----

// CatalogItem 是模型商店的展示条目。
type CatalogItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Languages   string `json:"languages"`
	License     string `json:"license"`
	Size        string `json:"size"`
	Tier        string `json:"tier"`
	Dest        string `json:"dest"`
	Installed   bool   `json:"installed"`
	InstalledSize int64 `json:"installedSize"`
	Recommended bool   `json:"recommended"`
	Current     bool   `json:"current"`
}

func (a *App) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 GET"})
		return
	}
	settings := a.Settings.Get()
	activeModel := settings.ModelID
	if activeModel == "" {
		activeModel = preferredLocalModel(scanInstalledModels(a.ModelsDir))
	}
	items := make([]CatalogItem, 0, len(store.Catalog))
	for _, m := range store.Catalog {
		item := CatalogItem{
			ID:          m.ID,
			Name:        m.Name,
			Description: m.Description,
			Languages:   m.Languages,
			License:     m.License,
			Size:        m.Size,
			Tier:        string(m.Tier),
			Dest:        m.Dest,
			Installed:   store.IsInstalled(m, a.ModelsDir),
		}
		target := filepath.Join(a.ModelsDir, m.Dest)
		if st, err := os.Stat(target); err == nil {
			if st.IsDir() {
				item.InstalledSize = data.DirSize(target)
			} else {
				item.InstalledSize = st.Size()
			}
		}
		// 推荐：中文场景优先 SenseVoice（多语种、自带标点）。
		item.Recommended = m.ID == "sense-voice" || m.ID == "paraformer-zh"
		item.Current = activeModel != "" && (m.Dest == activeModel || strings.HasPrefix(m.Dest, activeModel) || m.ID == activeModel)
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models":            items,
		"modelsDir":         a.ModelsDir,
		"installed":         scanInstalledModels(a.ModelsDir),
		"currentModelId":    activeModel,
		"storeCatalogCount": len(store.Catalog),
	})
}

func (a *App) handleModelRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/models/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, errBad("缺少模型标识"))
		return
	}
	switch parts[0] {
	case "download":
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 POST"})
			return
		}
		var body struct {
			ID string `json:"id"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, err)
			return
		}
		if _, ok := modelByID(body.ID); !ok {
			writeError(w, errNotFound("未知模型："+body.ID))
			return
		}
		job, err := a.Jobs.Create(jobs.TypeDownload, "", "下载模型 "+body.ID, map[string]any{"modelId": body.ID})
		if err != nil {
			writeError(w, errServer("创建下载任务失败：" + err.Error()))
			return
		}
		if err := a.Jobs.Start(job.ID); err != nil {
			writeError(w, errServer("启动下载任务失败：" + err.Error()))
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"job": job})

	case "remove":
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 POST"})
			return
		}
		var body struct {
			ID string `json:"id"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, err)
			return
		}
		m, ok := modelByID(body.ID)
		if !ok {
			writeError(w, errNotFound("未知模型"))
			return
		}
		target := filepath.Join(a.ModelsDir, m.Dest)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(a.ModelsDir)) {
			writeError(w, errBad("非法路径"))
			return
		}
		if err := data.RemoveDir(target); err != nil {
			writeError(w, errServer("删除模型失败（可能正在使用）："+err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": true})

	case "select":
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "仅支持 POST"})
			return
		}
		var body struct {
			ModelID string `json:"modelId"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, err)
			return
		}
		settings := a.Settings.Get()
		settings.ModelID = body.ModelID
		if err := a.Settings.Update(settings); err != nil {
			writeError(w, errServer("保存设置失败"))
			return
		}
		a.ResetTranscriber()
		writeJSON(w, http.StatusOK, map[string]any{"modelId": body.ModelID})

	default:
		writeError(w, errNotFound("未知模型操作"))
	}
}

func modelByID(id string) (store.Model, bool) {
	return store.ByID(id)
}

func storeDownload(ctx context.Context, m store.Model, modelsDir string, progress func(done, total int64)) error {
	return store.Download(ctx, m, modelsDir, progress)
}
