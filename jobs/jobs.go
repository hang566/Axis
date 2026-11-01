// Package jobs 提供持久化任务队列：每个任务的状态、进度与日志都落盘，
// 进程重启后能恢复历史、识别中断，并支持取消与重试。
//
// 任务执行是异步的：HTTP 提交后立即返回 job id，前端通过 SSE 订阅进度，
// 因此长音频/大文件不会把浏览器请求拖到超时。
package jobs

import (
	"context"
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

// 任务状态。
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"
	StatusInterrupted = "interrupted" // 进程退出时还在跑的任务
)

// 任务类型。
const (
	TypeTranscribe  = "transcribe"
	TypeTranscode   = "transcode"
	TypeExport      = "export"
	TypeDownload    = "download"
)

// Step 是任务内部的一个阶段，前端据此展示「正在做什么」。
type Step struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Index int     `json:"index"`
	Total int     `json:"total"`
	Pct   float64 `json:"pct"`
}

// Result 是任务完成后可用的产出摘要。
type Result struct {
	SubtitleID string  `json:"subtitleId,omitempty"`
	OutputID   string  `json:"outputId,omitempty"`
	OutputName string  `json:"outputName,omitempty"`
	CueCount   int     `json:"cueCount,omitempty"`
	Duration   float64 `json:"duration,omitempty"`
	Elapsed    float64 `json:"elapsed,omitempty"`
	Detected   string  `json:"detectedLanguage,omitempty"`
	Message    string  `json:"message,omitempty"`
	Extra      map[string]any `json:"extra,omitempty"`
}

// Job 是一次可持久化的任务。
type Job struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Status      string         `json:"status"`
	Phase       string         `json:"phase,omitempty"`
	Step        Step           `json:"step"`
	Overall     float64        `json:"overall"` // 0-1
	ProjectID   string         `json:"projectId,omitempty"`
	Title       string         `json:"title,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
	Result      *Result        `json:"result,omitempty"`
	Error       string         `json:"error,omitempty"`
	ErrorHint   string         `json:"errorHint,omitempty"`
	Logs        []string       `json:"-"`
	LogFile     string         `json:"logFile,omitempty"`
	CreatedAt   time.Time      `json:"createdAt"`
	StartedAt   time.Time      `json:"startedAt,omitempty"`
	FinishedAt  time.Time      `json:"finishedAt,omitempty"`
	UpdatedAt   time.Time      `json:"updatedAt"`
	Resumable   bool           `json:"resumable,omitempty"`
	Canceled    bool           `json:"canceled,omitempty"`
}

// Active 表示任务仍在队列或执行中。
func (j *Job) Active() bool {
	return j.Status == StatusPending || j.Status == StatusRunning
}

// Sink 是任务执行期间写回进度、日志与产出的接口。
// 它保证所有写操作都经过 Manager 的锁，避免并发竞争的脏数据。
type Sink struct {
	m     *Manager
	jobID string
	ctx   context.Context
}

// Ctx 返回任务上下文（取消时会被 cancel）。
func (s *Sink) Ctx() context.Context { return s.ctx }

// Progress 更新总进度（0-1）与当前阶段描述。
func (s *Sink) Progress(overall float64, phase string) {
	s.m.mutate(s.jobID, func(j *Job) {
		if overall < j.Overall {
			overall = j.Overall // 进度只增不减，避免来回跳动
		}
		j.Overall = clamp01(overall)
		if phase != "" {
			j.Phase = phase
		}
	})
}

// Step 更新子步骤进度（如「第 3/12 段」）。
func (s *Sink) Step(key, label string, index, total int, pct float64) {
	s.m.mutate(s.jobID, func(j *Job) {
		j.Step = Step{Key: key, Label: label, Index: index, Total: total, Pct: clamp01(pct)}
		j.Phase = label
	})
}

// Logf 追加一条日志（同时写内存与日志文件）。
func (s *Sink) Logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	s.m.appendLog(s.jobID, line)
}

// Done 记录任务产出。
func (s *Sink) Done(res *Result) {
	s.m.mutate(s.jobID, func(j *Job) {
		j.Result = res
		j.Overall = 1
	})
}

// Runner 执行任务体。返回 error 表示失败；ctx 被取消时应尽快返回。
type Runner func(ctx context.Context, sink *Sink, job *Job) (*Result, error)

// Manager 管理全部任务及其持久化。
type Manager struct {
	mu       sync.RWMutex
	dir      string
	jobs     map[string]*Job
	runners  map[string]Runner
	active   map[string]context.CancelFunc
	subs     map[chan struct{}]struct{}
	subMu    sync.Mutex
	seq      int
	logDir   string
}

// NewManager 加载 jobs/ 下的历史任务。
func NewManager(dir string) (*Manager, error) {
	m := &Manager{
		dir:     dir,
		jobs:    map[string]*Job{},
		runners: map[string]Runner{},
		active:  map[string]context.CancelFunc{},
		subs:    map[chan struct{}]struct{}{},
		logDir:  filepath.Join(dir, "logs"),
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	_ = os.MkdirAll(m.logDir, 0o755)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var j Job
		if err := data.ReadJSON(filepath.Join(dir, e.Name()), &j); err != nil || j.ID == "" {
			continue
		}
		// 上次退出时还在执行的任务：标记中断，并允许用户重试。
		if j.Active() {
			j.Status = StatusInterrupted
			j.Phase = "上次运行被中断"
			j.Resumable = true
			j.FinishedAt = time.Now()
			m.persist(&j)
		}
		m.jobs[j.ID] = &j
	}
	return m, nil
}

// Register 注册某一类型任务的执行函数。
func (m *Manager) Register(jobType string, r Runner) {
	m.mu.Lock()
	m.runners[jobType] = r
	m.mu.Unlock()
}

// Create 创建任务记录（尚未执行）。
func (m *Manager) Create(jobType, projectID, title string, params map[string]any) (*Job, error) {
	m.mu.Lock()
	now := time.Now()
	j := &Job{
		ID:        data.NewID("j_"),
		Type:      jobType,
		Status:    StatusPending,
		ProjectID: projectID,
		Title:     title,
		Params:    params,
		CreatedAt: now,
		UpdatedAt: now,
		Phase:     "等待开始",
	}
	j.LogFile = filepath.ToSlash(filepath.Join("logs", j.ID+".log"))
	m.jobs[j.ID] = j
	m.mu.Unlock()
	m.persist(j)
	cp := *j
	return &cp, nil
}

// Start 异步执行任务。同一任务重复 Start 会被忽略。
func (m *Manager) Start(jobID string) error {
	m.mu.Lock()
	j, ok := m.jobs[jobID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("jobs: 未知任务 %s", jobID)
	}
	if j.Status == StatusRunning {
		m.mu.Unlock()
		return nil
	}
	runner := m.runners[j.Type]
	if runner == nil {
		m.mu.Unlock()
		return fmt.Errorf("jobs: 没有注册处理 %q 的执行器", j.Type)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.active[jobID] = cancel
	j.Status = StatusRunning
	j.StartedAt = time.Now()
	j.FinishedAt = time.Time{}
	j.Canceled = false
	j.Error = ""
	j.ErrorHint = ""
	j.Overall = 0
	j.Phase = "开始"
	m.mu.Unlock()

	m.publish()
	m.persist(j)

	go func() {
		sink := &Sink{m: m, jobID: jobID, ctx: ctx}
		defer func() {
			m.mu.Lock()
			delete(m.active, jobID)
			m.mu.Unlock()
			cancel()
			m.publish()
		}()

		res, err := m.runSafely(ctx, runner, sink, j)
		now := time.Now()
		m.mu.Lock()
		cur := m.jobs[jobID]
		if cur != nil {
			cur.FinishedAt = now
			cur.UpdatedAt = now
			cur.Resumable = false
			switch {
			case ctx.Err() != nil:
				cur.Status = StatusCanceled
				cur.Phase = "已取消"
			case err != nil:
				cur.Status = StatusFailed
				cur.Error = err.Error()
				cur.ErrorHint = hintFor(err)
				cur.Phase = "失败"
				cur.Resumable = true
			default:
				cur.Status = StatusSucceeded
				cur.Phase = "完成"
				cur.Overall = 1
				if res != nil {
					cur.Result = res
				}
			}
			// 在锁内落盘，保证「状态可见」与「状态已保存」一致，
			// 避免进程紧接着退出时丢掉终态。
			m.persistLocked(cur)
		}
		m.mu.Unlock()
		if cur != nil && err != nil && ctx.Err() == nil {
			m.appendLog(jobID, "错误："+err.Error())
		}
		m.publish()
	}()
	return nil
}

// runSafely 捕获 panic，避免单个任务拖垮整个服务。
func (m *Manager) runSafely(ctx context.Context, runner Runner, sink *Sink, j *Job) (res *Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("任务内部错误：%v", r)
			m.appendLog(j.ID, fmt.Sprintf("panic: %v", r))
		}
	}()
	return runner(ctx, sink, j)
}

// Cancel 请求取消任务。
func (m *Manager) Cancel(jobID string) error {
	m.mu.Lock()
	cancel, ok := m.active[jobID]
	j := m.jobs[jobID]
	m.mu.Unlock()
	if !ok || j == nil {
		return errors.New("jobs: 任务不在运行中")
	}
	cancel()
	m.appendLog(jobID, "收到取消请求，正在安全停止…")
	return nil
}

// Get 返回任务深拷贝。
func (m *Manager) Get(jobID string) (*Job, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[jobID]
	if !ok {
		return nil, false
	}
	cp := *j
	if j.Result != nil {
		r := *j.Result
		cp.Result = &r
	}
	if j.Params != nil {
		cp.Params = map[string]any{}
		for k, v := range j.Params {
			cp.Params[k] = v
		}
	}
	return &cp, true
}

// ListFilter 过滤任务列表。
type ListFilter struct {
	ProjectID string
	Type      string
	Active    bool
	Limit     int
}

// List 返回按创建时间倒序的任务。
func (m *Manager) List(f ListFilter) []*Job {
	m.mu.RLock()
	out := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		if f.ProjectID != "" && j.ProjectID != f.ProjectID {
			continue
		}
		if f.Type != "" && j.Type != f.Type {
			continue
		}
		if f.Active && !j.Active() {
			continue
		}
		cp := *j
		cp.Logs = nil
		out = append(out, &cp)
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out
}

// Logs 读取任务日志（内存里没有时回落到磁盘）。
func (m *Manager) Logs(jobID string, tail int) []string {
	m.mu.RLock()
	j, ok := m.jobs[jobID]
	var lines []string
	if ok {
		lines = append(lines, j.Logs...)
		logFile := j.LogFile
		m.mu.RUnlock()
		if len(lines) == 0 && logFile != "" {
			if raw, err := os.ReadFile(filepath.Join(m.dir, filepath.FromSlash(logFile))); err == nil {
				lines = strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
			}
		}
	} else {
		m.mu.RUnlock()
		return nil
	}
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return lines
}

// ActiveCount 返回运行中的任务数。
func (m *Manager) ActiveCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, j := range m.jobs {
		if j.Status == StatusRunning {
			n++
		}
	}
	return n
}

// Prune 删除超过保留期的已结束任务记录及其日志。
func (m *Manager) Prune(maxAge time.Duration) int {
	m.mu.Lock()
	var ids []string
	for id, j := range m.jobs {
		if j.Active() || time.Since(j.UpdatedAt) < maxAge {
			continue
		}
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.mu.Lock()
		j := m.jobs[id]
		delete(m.jobs, id)
		m.mu.Unlock()
		_ = os.Remove(filepath.Join(m.dir, id+".json"))
		if j != nil && j.LogFile != "" {
			_ = os.Remove(filepath.Join(m.dir, filepath.FromSlash(j.LogFile)))
		}
	}
	if len(ids) > 0 {
		m.publish()
	}
	return len(ids)
}

// Subscribe 返回一个信号 channel：任务状态有变化时会收到通知。
// 消费方只应把它当作「有新数据」的提示，具体数据通过 Get/List 读取。
func (m *Manager) Subscribe() (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	m.subMu.Lock()
	m.subs[ch] = struct{}{}
	m.subMu.Unlock()
	return ch, func() {
		m.subMu.Lock()
		delete(m.subs, ch)
		m.subMu.Unlock()
	}
}

func (m *Manager) publish() {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for ch := range m.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// mutate 在锁内修改任务；只在状态真正变化时触发事件与落盘。
func (m *Manager) mutate(jobID string, fn func(j *Job)) {
	m.mu.Lock()
	j, ok := m.jobs[jobID]
	if !ok {
		m.mu.Unlock()
		return
	}
	beforeStatus, beforePhase := j.Status, j.Phase
	fn(j)
	j.UpdatedAt = time.Now()
	changed := j.Status != beforeStatus || j.Phase != beforePhase
	m.mu.Unlock()
	m.publish()
	if changed {
		m.persist(j)
	}
}

// appendLog 追加日志并节流落盘。
func (m *Manager) appendLog(jobID, line string) {
	line = strings.TrimRight(line, "\n")
	if line == "" {
		return
	}
	m.mu.Lock()
	j, ok := m.jobs[jobID]
	if !ok {
		m.mu.Unlock()
		return
	}
	j.Logs = append(j.Logs, line)
	if len(j.Logs) > 400 {
		j.Logs = j.Logs[len(j.Logs)-400:]
	}
	logFile := j.LogFile
	m.mu.Unlock()

	if logFile != "" {
		f, err := os.OpenFile(filepath.Join(m.dir, filepath.FromSlash(logFile)),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().Format("15:04:05"), line)
			_ = f.Close()
		}
	}
	m.publish()
}

func (m *Manager) persist(j *Job) {
	m.mu.RLock()
	m.persistLocked(j)
	m.mu.RUnlock()
}

// persistLocked 调用方需持有 m.mu（读或写锁）。
func (m *Manager) persistLocked(j *Job) {
	cp := *j
	cp.Logs = nil
	_ = data.WriteJSON(filepath.Join(m.dir, cp.ID+".json"), &cp)
}

// WaitIdle 阻塞到没有运行中的任务（用于优雅退出与测试）。
func (m *Manager) WaitIdle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m.ActiveCount() == 0 {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return m.ActiveCount() == 0
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// hintFor 把常见错误翻译成可操作的中文提示。
func hintFor(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "context canceled"):
		return "任务已取消"
	case strings.Contains(msg, "no such file"), strings.Contains(msg, "找不到"), strings.Contains(msg, "not found"):
		return "文件可能已被移动或删除，请重新导入素材"
	case strings.Contains(msg, "ffmpeg"):
		return "请检查设置中的 ffmpeg 路径，或确认文件本身可正常播放"
	case strings.Contains(msg, "python"), strings.Contains(msg, "sherpa"):
		return "本地语音引擎未就绪：请在「模型中心」检查 Python 与模型，或改用在线接口"
	case strings.Contains(msg, "超时"), strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline"):
		return "处理超时。可在设置中调大超时时间，或把长音频先切分成几段"
	case strings.Contains(msg, "no audio"), strings.Contains(msg, "没有音频"):
		return "该文件没有音轨，无法识别语音"
	default:
		return "可展开日志查看详细原因；若可复现，请把日志反馈给开发者"
	}
}
