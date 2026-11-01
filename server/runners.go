package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/local/stt-sub/adapters"
	"github.com/local/stt-sub/data"
	"github.com/local/stt-sub/jobs"
	"github.com/local/stt-sub/library"
	"github.com/local/stt-sub/media"
	"github.com/local/stt-sub/pipeline"
	"github.com/local/stt-sub/subtitle"
)

// TranscribeParams 是转写任务的参数（提交时保存，重试/续跑时复用）。
type TranscribeParams struct {
	Format      string `json:"format"`
	Language    string `json:"language"`
	Engine      string `json:"engine"`
	ModelID     string `json:"modelId"`
	ChunkMinutes int   `json:"chunkMinutes"`
	Prompt      string `json:"prompt"`
	SubtitleID  string `json:"subtitleId,omitempty"` // 覆盖已有字幕时指定
}

// TranscodeParams 是转码任务的参数。
type TranscodeParams struct {
	Preset       string  `json:"preset"`
	Format       string  `json:"format"`
	VideoCodec   string  `json:"videoCodec"`
	AudioCodec   string  `json:"audioCodec"`
	PresetSpeed  string  `json:"speed"`
	CRF          int     `json:"crf"`
	Bitrate      string  `json:"bitrate"`
	AudioBitrate string  `json:"audioBitrate"`
	Resolution   string  `json:"resolution"`
	FrameRate    float64 `json:"frameRate"`
	SampleRate   int     `json:"sampleRate"`
	Channels     int     `json:"channels"`
	Start        float64 `json:"start"`
	End          float64 `json:"end"`
}

// registerRunners 把任务类型与执行函数绑定。
func (a *App) registerRunners() {
	a.Jobs.Register(jobs.TypeTranscribe, a.runTranscribe)
	a.Jobs.Register(jobs.TypeTranscode, a.runTranscode)
	a.Jobs.Register(jobs.TypeExport, a.runExport)
	a.Jobs.Register(jobs.TypeDownload, a.runDownload)
}

// ---- 转写 ----

func (a *App) runTranscribe(ctx context.Context, sink *jobs.Sink, job *jobs.Job) (*jobs.Result, error) {
	params := TranscribeParams{}
	readParams(job, &params)
	p, err := a.Lib.GetProject(job.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("项目不存在：%w", err)
	}
	if p.Media == nil {
		return nil, errors.New("项目没有素材")
	}
	input := a.Lib.AssetPath(p.Media)

	settings := a.Settings.Get()
	format := params.Format
	if format == "" {
		format = settings.OutputFormat
	}
	language := params.Language
	if language == "" {
		language = settings.Language
	}

	// 引擎按任务参数选择：允许单次任务覆盖全局设置。
	tr, err := a.transcriberFor(params.Engine, params.ModelID)
	if err != nil {
		return nil, err
	}
	sink.Logf("引擎：%s", tr.Name())
	sink.Logf("素材：%s（%s）", p.Media.FileName, data.HumanBytes(p.Media.Size))
	if p.Duration > 0 {
		sink.Logf("时长：%s", formatDuration(p.Duration))
	}

	chunk := time.Duration(settings.ChunkMinutes) * time.Minute
	if params.ChunkMinutes > 0 {
		chunk = time.Duration(params.ChunkMinutes) * time.Minute
	}
	maxLine, maxLines, maxCue, minCue, maxPause, sentence, strip := a.Settings.SubtitleOptions()

	runner := pipeline.New(a.Media, pipeline.Config{
		FFmpegPath:   settings.FFmpegPath,
		SampleRate:   16000,
		Channels:     1,
		ChunkSeconds: chunk,
		Subtitle: subtitle.Options{
			MaxLineLength:    maxLine,
			MaxLines:         maxLines,
			MaxCharsPerCue:   maxLine * maxLines,
			MaxDuration:      time.Duration(maxCue) * time.Second,
			MinDuration:      time.Duration(minCue) * time.Millisecond,
			MaxPause:         time.Duration(maxPause) * time.Millisecond,
			SentenceMerge:    sentence,
			StripPunctuation: strip,
		},
	})

	tempDir := filepath.Join(a.Lib.TempDir(), "job-"+job.ID)
	res, err := runner.Run(ctx, tr, pipeline.Request{
		InputPath:    input,
		TempDir:      tempDir,
		Format:       format,
		SampleRate:   16000,
		Channels:     1,
		ChunkSeconds: chunk,
		Transcribe: adapters.Options{
			Language: language,
			Prompt:   params.Prompt,
			Model:    params.ModelID,
		},
	}, func(overall float64, phase string) {
		sink.Progress(overall, phase)
		if phase != "" {
			sink.Step("transcribe", phase, 0, 0, overall)
		}
	})
	// 无论成功失败都清理临时音频，避免长音频把磁盘吃满。
	_ = data.RemoveDir(tempDir)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}

	if res.Silent {
		hint := "这段音频里没有检测到语音（可能是纯音乐、静音或音量过低）。"
		sink.Logf("%s", hint)
		// 仍然写出一份空字幕，避免用户以为任务失败。
		empty := []subtitle.TrackCue{}
		body, ext, _ := subtitle.ToBytes(empty, format)
		track, terr := a.saveTrack(p, params, ext, language, res, body, 0)
		if terr != nil {
			return nil, terr
		}
		return &jobs.Result{
			SubtitleID: track.ID,
			Message:    hint,
			Elapsed:    res.Elapsed.Seconds(),
			Duration:   res.Duration.Seconds(),
		}, nil
	}

	track, err := a.saveTrack(p, params, res.Format, language, res, res.Bytes, len(res.Cues))
	if err != nil {
		return nil, err
	}
	sink.Logf("完成：%d 条字幕，用时 %s", len(res.Cues), res.Elapsed.Round(time.Second))
	return &jobs.Result{
		SubtitleID: track.ID,
		CueCount:   len(res.Cues),
		Duration:   res.Duration.Seconds(),
		Elapsed:    res.Elapsed.Seconds(),
		Detected:   track.Language,
		Message:    fmt.Sprintf("已生成 %d 条字幕", len(res.Cues)),
	}, nil
}

// saveTrack 把字幕落盘并挂到项目上。
func (a *App) saveTrack(p *library.Project, params TranscribeParams, format, language string, res *pipeline.Result, body []byte, cueCount int) (library.SubtitleTrack, error) {
	filename := p.Title
	if format == "vtt" {
		filename += "-vtt"
	}
	track := library.SubtitleTrack{
		ID:       params.SubtitleID,
		Format:   format,
		Language: language,
		CueCount: cueCount,
		Duration: p.Duration,
	}
	if res != nil {
		track.Model = res.Model
		track.Provider = "本地识别"
		if res.Duration > 0 {
			track.Duration = res.Duration.Seconds()
		}
	}
	if language == "" && res != nil {
		track.Language = res.Language
	}
	track.FileName = filename + " 字幕"
	updated, err := a.Lib.SaveSubtitleTrack(p.ID, track, body)
	if err != nil {
		return library.SubtitleTrack{}, err
	}
	saved, _ := library.FindSubtitle(updated, track.ID)
	// 项目状态与服务端上下文同步。
	_, _ = a.Lib.UpdateProject(p.ID, func(pp *library.Project) {
		pp.Status = library.StatusReady
		pp.LastError = ""
		pp.Kind = library.KindTranscribe
		if pp.Language == "" && track.Language != "" {
			pp.Language = track.Language
		}
	})
	return saved, nil
}

func (a *App) transcriberFor(engine, modelID string) (adapters.Transcriber, error) {
	settings := a.Settings.Get()
	if engine == "" || engine == settings.Engine {
		if modelID == "" || modelID == settings.ModelID {
			return a.Transcriber()
		}
	}
	// 单次任务覆盖：临时构造一个适配器（用完即弃）。
	cfg := adapters.Config{
		Endpoint:   settings.RemoteEndpoint,
		APIKey:     settings.APIKey,
		APIKeyEnv:  settings.APIKeyEnv,
		Model:      settings.RemoteModel,
		ModelsDir:  a.ModelsDir,
		ModelID:    firstNonEmptyStr(modelID, settings.ModelID),
		Language:   settings.Language,
		Command:    a.Cfg.STT.Command,
		Args:       a.Cfg.STT.Args,
		OutputFile: a.Cfg.STT.OutputFile,
		Timeout:    time.Duration(settings.TimeoutSeconds) * time.Second,
		Extra:      a.Cfg.STT.Extra,
		ScriptRoot: a.ScriptDir,
	}
	name := firstNonEmptyStr(engine, settings.Engine)
	if name == "" {
		name = "local"
	}
	return adapters.New(name, cfg)
}

// ---- 转码 ----

func (a *App) runTranscode(ctx context.Context, sink *jobs.Sink, job *jobs.Job) (*jobs.Result, error) {
	params := TranscodeParams{}
	readParams(job, &params)
	p, err := a.Lib.GetProject(job.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("项目不存在：%w", err)
	}
	if p.Media == nil {
		return nil, errors.New("项目没有素材")
	}
	input := a.Lib.AssetPath(p.Media)
	displayName := p.Media.FileName
	if displayName == "" {
		displayName = filepath.Base(input)
	}

	req, outName, err := a.buildTranscodeRequest(input, displayName, params)
	if err != nil {
		return nil, err
	}

	outDir := filepath.Join(a.Lib.ProjectDir(p.ID), "out")
	_ = os.MkdirAll(outDir, 0o755)
	outPath := filepath.Join(outDir, outName)
	req.Output = outPath
	sink.Logf("目标文件：%s", outName)
	sink.Logf("参数：%s", media.PreviewCommand(req))
	if err := a.Media.Transcode(ctx, req, func(frac float64) {
		sink.Progress(0.02+0.96*frac, "转码中")
	}); err != nil {
		_ = os.Remove(outPath)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	start := time.Now()
	probe, _ := a.Media.Probe(ctx, outPath)
	// 转码产物留在项目 out/ 里（路径另行登记），不复制进 assets，避免占两倍空间。
	asset := &library.Asset{
		ID:        data.NewID("a_"),
		Path:      a.Lib.Rel(outPath),
		FileName:  outName,
		Origin:    "output",
		CreatedAt: time.Now(),
		Probe:     toProbeInfo(probe),
	}
	if st, serr := os.Stat(outPath); serr == nil {
		asset.Size = st.Size()
	}
	updated, err := a.Lib.UpdateProject(p.ID, func(pp *library.Project) {
		pp.Outputs = append(pp.Outputs, asset)
		pp.Status = library.StatusReady
		pp.LastError = ""
	})
	if err != nil {
		return nil, err
	}
	_ = updated
	size := asset.Size
	sink.Logf("完成：%s（%s，用时 %s）", outName, data.HumanBytes(size), time.Since(start).Round(time.Second))
	return &jobs.Result{
		OutputID:   asset.ID,
		OutputName: outName,
		Duration:   p.Duration,
		Message:    "转码完成：" + data.HumanBytes(size),
		Extra:      map[string]any{"size": size},
	}, nil
}

// buildTranscodeRequest 把预设/自定义参数翻译成 ffmpeg 请求。
// displayName 用于生成输出文件名（素材的真实文件名，而不是哈希）。
func (a *App) buildTranscodeRequest(input, displayName string, p TranscodeParams) (media.TranscodeRequest, string, error) {
	req := media.TranscodeRequest{
		Input:        input,
		VideoCodec:   p.VideoCodec,
		AudioCodec:   p.AudioCodec,
		Preset:       p.PresetSpeed,
		CRF:          p.CRF,
		Bitrate:      p.Bitrate,
		AudioBitrate: p.AudioBitrate,
		Resolution:   p.Resolution,
		FrameRate:    p.FrameRate,
		SampleRate:   p.SampleRate,
		Channels:     p.Channels,
		Start:        floatSeconds(p.Start),
		End:          floatSeconds(p.End),
	}
	req.Format = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(p.Format)), ".")
	outExt := req.Format
	if outExt == "" {
		outExt = "mp4"
		req.Format = ""
	}
	if req.VideoCodec == "" && req.AudioCodec == "" {
		// 没有显式编码器时按容器给默认值，避免 mp4 里塞进不兼容的编码。
		if outExt == "webm" {
			req.VideoCodec = "libvpx-vp9"
			req.AudioCodec = "libopus"
		} else {
			req.VideoCodec = "libx264"
			req.AudioCodec = "aac"
		}
	}
	if req.End > 0 && req.Start > 0 && req.End <= req.Start {
		return req, "", fmt.Errorf("剪辑终点必须大于起点")
	}
	name := fmt.Sprintf("%s.%s", data.SafeName(strings.TrimSuffix(displayName, filepath.Ext(displayName))), outExt)
	if req.Start > 0 || req.End > 0 {
		name = fmt.Sprintf("%s_%s-%s.%s",
			data.SafeName(strings.TrimSuffix(displayName, filepath.Ext(displayName))),
			clockName(req.Start), clockName(req.End), outExt)
	}
	req.Output = name
	return req, name, nil
}

func clockName(d time.Duration) string {
	total := int(d.Seconds())
	return fmt.Sprintf("%02d%02d%02d", total/3600, (total%3600)/60, total%60)
}

func floatSeconds(v float64) time.Duration {
	if v <= 0 {
		return 0
	}
	return time.Duration(v * float64(time.Second))
}

// ---- 导出 / 下载任务 ----

func (a *App) runExport(ctx context.Context, sink *jobs.Sink, job *jobs.Job) (*jobs.Result, error) {
	params := map[string]any{}
	readParams(job, &params)
	format, _ := params["format"].(string)
	trackID, _ := params["trackId"].(string)
	p, err := a.Lib.GetProject(job.ProjectID)
	if err != nil {
		return nil, err
	}
	var track library.SubtitleTrack
	if trackID != "" {
		track, _ = library.FindSubtitle(p, trackID)
	} else if len(p.Subtitles) > 0 {
		track = p.Subtitles[0]
	}
	if track.ID == "" {
		return nil, errors.New("没有可导出的字幕")
	}
	raw, err := a.Lib.ReadSubtitle(p, track)
	if err != nil {
		return nil, err
	}
	cues, err := subtitle.Parse(raw)
	if err != nil {
		return nil, err
	}
	body, ext, err := subtitle.ToBytes(cues, firstNonEmptyStr(format, "srt"))
	if err != nil {
		return nil, err
	}
	outPath := filepath.Join(a.Lib.ProjectDir(p.ID), "out", data.SafeName(p.Title)+"."+ext)
	if err := data.WriteFile(outPath, body, 0o644); err != nil {
		return nil, err
	}
	return &jobs.Result{OutputName: filepath.Base(outPath), CueCount: len(cues), Message: "已导出字幕"}, nil
}

// runDownload 下载模型（带进度与可取消）。
func (a *App) runDownload(ctx context.Context, sink *jobs.Sink, job *jobs.Job) (*jobs.Result, error) {
	params := map[string]any{}
	readParams(job, &params)
	modelID, _ := params["modelId"].(string)
	if modelID == "" {
		return nil, errors.New("缺少模型 ID")
	}
	m, ok := modelByID(modelID)
	if !ok {
		return nil, fmt.Errorf("未知模型 %s", modelID)
	}
	sink.Logf("下载 %s（%s）", m.Name, m.Size)
	lastPct := -1
	err := storeDownload(ctx, m, a.ModelsDir, func(done, total int64) {
		if total <= 0 {
			sink.Progress(0.3, fmt.Sprintf("已下载 %s", data.HumanBytes(done)))
			return
		}
		pct := int(100 * done / total)
		if pct != lastPct && pct%2 == 0 {
			lastPct = pct
			sink.Progress(float64(pct)/100*0.98, fmt.Sprintf("下载中 %d%%（%s / %s）", pct, data.HumanBytes(done), data.HumanBytes(total)))
		}
	})
	if err != nil {
		return nil, err
	}
	// 下载完成后刷新设置里的模型选择（如果是第一个模型）。
	settings := a.Settings.Get()
	if settings.ModelID == "" {
		settings.ModelID = m.Dest
		_ = a.Settings.Update(settings)
	}
	a.ResetTranscriber()
	sink.Logf("完成：%s", m.Dest)
	return &jobs.Result{Message: "模型已就绪：" + m.Name, Extra: map[string]any{"modelId": m.ID}}, nil
}

// readParams 把任务参数（map）解回结构体。
func readParams(job *jobs.Job, dst any) {
	if job.Params == nil {
		return
	}
	raw, err := jsonMarshal(job.Params)
	if err != nil {
		return
	}
	_ = jsonUnmarshal(raw, dst)
}

func formatDuration(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

