package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// remoteAPI 对应「HTTP 上传音频 + JSON/文本返回」的在线识别服务，
// 兼容 OpenAI Whisper / faster-whisper-server / 各类中转接口。
type remoteAPI struct {
	name      string
	cfg       Config
	fileField string
	model     string
	timeout   time.Duration
	client    *http.Client
}

func init() {
	Register("remote", newRemote)
	Register("whisper_api", newRemote)
	Register("openai_whisper", newRemote)
}

func newRemote(cfg Config) (Transcriber, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, fmt.Errorf("在线识别需要在设置里填写接口地址（endpoint），\n例如 https://api.openai.com/v1/audio/transcriptions")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &remoteAPI{
		name:      "在线接口",
		cfg:       cfg,
		fileField: firstNonEmpty(cfg.Extra["file_field"], "file"),
		model:     firstNonEmpty(cfg.Model, "whisper-1"),
		timeout:   timeout,
		client:    &http.Client{Timeout: timeout},
	}, nil
}

func (r *remoteAPI) Name() string { return r.name + "（" + r.model + "）" }

func (r *remoteAPI) Transcribe(audioPath string, opts Options, onProgress func(float64)) ([]Segment, Info, error) {
	start := time.Now()
	fields := map[string]string{
		"model": firstNonEmpty(opts.Model, r.cfg.Model, r.model),
	}
	if !r.wantsVerbose() {
		fields["response_format"] = "verbose_json"
	} else if v := r.cfg.Extra["response_format"]; v != "" {
		fields["response_format"] = v
	}
	if lang := firstNonEmpty(opts.Language, r.cfg.Language); lang != "" {
		fields["language"] = lang
	}
	if prompt := firstNonEmpty(opts.Prompt, r.cfg.Extra["prompt"]); prompt != "" {
		fields["prompt"] = prompt
	}
	if opts.Temperature > 0 {
		fields["temperature"] = strconv.FormatFloat(opts.Temperature, 'f', 2, 64)
	}
	for k, v := range r.cfg.Extra {
		if v == "" || isMetaKey(k) {
			continue
		}
		if _, exists := fields[k]; !exists {
			fields[k] = v
		}
	}

	body, err := r.postFile(audioPath, fields, opts)
	if err != nil {
		return nil, Info{}, err
	}
	segs, err := ParseTranscription(body, ParseTimeUnit(r.cfg.Extra["time_unit"]))
	if err != nil {
		// 有些服务即使要求 verbose_json 也只返回纯字幕文本。
		if srtSegs, srtErr := ParseSRT(body); srtErr == nil {
			segs = srtSegs
		} else {
			return nil, Info{}, err
		}
	}
	if onProgress != nil {
		onProgress(1)
	}
	return segs, Info{Model: fields["model"], ElapsedSeconds: time.Since(start).Seconds()}, nil
}

func (r *remoteAPI) wantsVerbose() bool {
	return r.cfg.Extra["response_format"] != ""
}

func (r *remoteAPI) postFile(audioPath string, fields map[string]string, opts Options) ([]byte, error) {
	file, err := os.Open(audioPath)
	if err != nil {
		return nil, fmt.Errorf("无法读取音频：%w", err)
	}
	defer file.Close()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile(r.fileField, filepath.Base(audioPath))
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(part, file); err != nil {
		return nil, err
	}
	for k, v := range fields {
		_ = writer.WriteField(k, v)
	}
	for k, v := range opts.Extra {
		if v != "" {
			_ = writer.WriteField(k, v)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.Endpoint, &buf)
	if err != nil {
		return nil, fmt.Errorf("接口地址不合法：%w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if key := r.apiKey(); key != "" {
		scheme := firstNonEmpty(r.cfg.Extra["auth_scheme"], "Bearer")
		req.Header.Set("Authorization", scheme+" "+key)
	}
	for k, v := range parseHeaderList(r.cfg.Extra["headers"]) {
		req.Header.Set(k, v)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("接口请求超时（%s）", r.timeout)
		}
		return nil, fmt.Errorf("无法连接识别接口：%v\n请检查网络、代理设置与接口地址", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("识别接口返回 %d：%s", resp.StatusCode, Clip(string(raw), 400))
	}
	return raw, nil
}

func (r *remoteAPI) apiKey() string {
	if key := strings.TrimSpace(r.cfg.APIKey); key != "" {
		return key
	}
	if env := strings.TrimSpace(r.cfg.APIKeyEnv); env != "" {
		return strings.TrimSpace(os.Getenv(env))
	}
	return ""
}

func isMetaKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "file_field", "time_unit", "headers", "auth_scheme", "prompt", "env":
		return true
	default:
		return false
	}
}

func parseHeaderList(s string) map[string]string {
	out := map[string]string{}
	for _, item := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(item), "=")
		if ok && strings.TrimSpace(k) != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// MarshalInfo 便于日志输出。
func MarshalInfo(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
