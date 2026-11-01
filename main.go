// Command stt-sub 是「视听工作台」：本地音视频转字幕、字幕编辑、媒体分析与转码。
//
// 最常用的方式：双击 start.bat（或直接运行 stt-sub.exe serve），浏览器里操作。
// 命令行子命令保留给脚本与高级用户：
//
//	stt-sub serve      [-listen 127.0.0.1:8420] [-open] [-data <目录>]
//	stt-sub stt        -i input.mp4 [-o out.srt]
//	stt-sub info       -i input.mp4 [-json]
//	stt-sub transcode  -i input.mp4 -o out.mp4 [参数]
//	stt-sub models     list | download <id...>
//	stt-sub doctor     环境自检
//	stt-sub version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/local/stt-sub/adapters"
	"github.com/local/stt-sub/config"
	"github.com/local/stt-sub/data"
	"github.com/local/stt-sub/jobs"
	"github.com/local/stt-sub/library"
	"github.com/local/stt-sub/media"
	"github.com/local/stt-sub/pipeline"
	"github.com/local/stt-sub/server"
	"github.com/local/stt-sub/store"
	"github.com/local/stt-sub/subtitle"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return cmdServe(nil)
	}
	switch os.Args[1] {
	case "serve":
		return cmdServe(os.Args[2:])
	case "stt":
		return cmdSTT(os.Args[2:])
	case "info":
		return cmdInfo(os.Args[2:])
	case "transcode":
		return cmdTranscode(os.Args[2:])
	case "models":
		return cmdModels(os.Args[2:])
	case "doctor":
		return cmdDoctor()
	case "version", "--version", "-v":
		fmt.Printf("视听工作台 %s\n", server.Version)
		return nil
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		if strings.HasPrefix(os.Args[1], "-") {
			// 旧式写法：stt-sub -serve -listen :9000
			return cmdServe(os.Args[1:])
		}
		printUsage()
		return fmt.Errorf("未知命令 %q", os.Args[1])
	}
}

func printUsage() {
	fmt.Print(`视听工作台 ` + server.Version + `

用法：
  stt-sub                      启动工作台（等同 serve，自动打开浏览器）
  stt-sub serve [选项]         启动本地服务
      -listen 127.0.0.1:8420   监听地址（0 表示自动选择空闲端口）
      -open                    启动后自动打开浏览器
      -data <目录>             指定数据目录（默认程序目录下的 data）
      -config <文件>           指定 config.yaml
  stt-sub stt -i <文件> [-o 字幕] [-format srt|vtt|json] [识别参数]
  stt-sub info -i <文件> [-json]
  stt-sub transcode -i <文件> -o <输出> [-resolution 720] [-crf 23] ...
  stt-sub models list | download <id...>
  stt-sub doctor               检查 ffmpeg / Python / 模型是否就绪
`)
}

// ---- serve ----

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8420", "监听地址")
	openBrowser := fs.Bool("open", true, "启动后打开浏览器")
	dataDir := fs.String("data", "", "数据目录")
	configPath := fs.String("config", "config.yaml", "配置文件")
	noCheck := fs.Bool("no-check", false, "跳过启动自检")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.LoadFile(*configPath, false)
	if err != nil {
		return err
	}
	cfg.Normalize()
	dir := data.ResolveDir(firstNonEmpty(*dataDir, cfg.General.DataDir))
	app, err := server.NewApp(cfg, dir)
	if err != nil {
		return err
	}
	defer app.Close()

	if !*noCheck {
		env := app.CheckEnvironment()
		printBanner(env, dir)
	}

	addr := resolveListen(*listen)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *openBrowser {
		go func() {
			time.Sleep(600 * time.Millisecond)
			openURL(browserURL(addr))
		}()
	}
	fmt.Printf("\n  浏览器访问： %s\n", browserURL(addr))
	fmt.Printf("  数据目录：   %s\n", dir)
	fmt.Println("\n  按 Ctrl+C 停止服务。")
	return app.Listen(ctx, addr)
}

func browserURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// resolveListen 在端口被占用时自动换一个空闲端口，避免「双击打不开」。
func resolveListen(addr string) string {
	if addr == "" || addr == "0" || addr == ":0" {
		return "127.0.0.1:8420"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if port == "0" {
		return net.JoinHostPort(host, freePort(host, 8420))
	}
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		_ = ln.Close()
		return addr
	}
	fmt.Printf("  端口 %s 已被占用，自动改用其它端口。\n", port)
	return net.JoinHostPort(host, freePort(host, 8420))
}

func freePort(host string, start int) string {
	for p := start; p < start+50; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", p)))
		if err == nil {
			_ = ln.Close()
			return fmt.Sprintf("%d", p)
		}
	}
	return "8420"
}

func printBanner(env server.EnvStatus, dir string) {
	fmt.Println()
	fmt.Println("  ┌──────────────────────────────────────────────┐")
	fmt.Printf("  │  视听工作台 %-8s  ·  本地音视频字幕工作台  │\n", server.Version)
	fmt.Println("  └──────────────────────────────────────────────┘")
	mark := func(ok bool) string {
		if ok {
			return "[就绪]"
		}
		return "[缺失]"
	}
	fmt.Printf("  %s ffmpeg      %s\n", mark(env.FFmpeg.OK), compact(env.FFmpeg.Version, 48))
	fmt.Printf("  %s ffprobe     %s\n", mark(env.FFprobe.OK), compact(env.FFprobe.Version, 48))
	fmt.Printf("  %s Python      %s\n", mark(env.Python.OK), compact(firstNonEmpty(env.Python.Version, env.Python.Message), 48))
	fmt.Printf("        语音模型    %d 个\n", len(env.InstalledModels))
	if len(env.Problems) > 0 {
		fmt.Println("  --------------------------------------------------")
		for _, p := range env.Problems {
			fmt.Println("  提示：" + p)
		}
	}
	fmt.Println()
}

func compact(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// ---- stt（命令行转写，复用同一套流水线） ----

func cmdSTT(args []string) error {
	fs := flag.NewFlagSet("stt", flag.ContinueOnError)
	input := fs.String("i", "", "输入音视频")
	output := fs.String("o", "", "输出字幕路径")
	format := fs.String("format", "srt", "字幕格式 srt/vtt/json")
	language := fs.String("language", "", "语言，如 zh")
	modelID := fs.String("model", "", "本地模型目录名")
	configPath := fs.String("config", "config.yaml", "配置文件")
	dataDir := fs.String("data", "", "数据目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*input) == "" {
		return errors.New("请用 -i 指定输入文件")
	}
	cfg, err := config.LoadFile(*configPath, false)
	if err != nil {
		return err
	}
	cfg.Normalize()
	dir := data.ResolveDir(firstNonEmpty(*dataDir, cfg.General.DataDir))
	app, err := server.NewApp(cfg, dir)
	if err != nil {
		return err
	}
	defer app.Close()

	settings := app.Settings.Get()
	settings.ModelID = firstNonEmpty(*modelID, settings.ModelID)
	tr, err := adapters.New("local", adapters.Config{
		ModelsDir:  app.ModelsDir,
		ModelID:    settings.ModelID,
		Language:   firstNonEmpty(*language, settings.Language),
		Timeout:    time.Duration(settings.TimeoutSeconds) * time.Second,
		ScriptRoot: app.ScriptDir,
	})
	if err != nil {
		return err
	}
	if initer, ok := tr.(adapters.Initer); ok {
		if err := initer.Init(); err != nil {
			return err
		}
		defer initer.Close()
	}
	runner := pipeline.New(app.Media, pipeline.Config{SampleRate: 16000, Channels: 1, Subtitle: subtitle.DefaultOptions()})
	fmt.Printf("正在识别：%s\n", *input)
	res, err := runner.Run(context.Background(), tr, pipeline.Request{
		InputPath: *input,
		Format:    *format,
		Transcribe: adapters.Options{
			Language: firstNonEmpty(*language, settings.Language),
			Model:    settings.ModelID,
		},
	}, func(overall float64, phase string) {
		fmt.Printf("\r  %5.1f%%  %-40s", overall*100, compact(phase, 40))
	})
	fmt.Println()
	if err != nil {
		return err
	}
	out := *output
	if out == "" {
		out = strings.TrimSuffix(*input, filepath.Ext(*input)) + "." + res.Format
	}
	if err := os.WriteFile(out, res.Bytes, 0o644); err != nil {
		return err
	}
	if res.Silent {
		fmt.Println("完成：没有检测到语音，已输出空字幕。")
	} else {
		fmt.Printf("完成：%d 条字幕（%s）→ %s\n", len(res.Cues), res.Elapsed.Round(time.Millisecond), out)
	}
	return nil
}

// ---- info ----

func cmdInfo(args []string) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	input := fs.String("i", "", "输入文件")
	asJSON := fs.Bool("json", false, "输出 JSON")
	ffmpeg := fs.String("ffmpeg", "", "ffmpeg 路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return errors.New("请用 -i 指定输入文件")
	}
	m := media.New(*ffmpeg)
	probe, err := m.Probe(context.Background(), *input)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(probe)
	}
	fmt.Printf("容器：%s（%s）\n", probe.Format.FormatName, probe.Format.LongName)
	fmt.Printf("时长：%s\n", probe.Duration().Round(time.Millisecond))
	fmt.Printf("大小：%s\n", data.HumanBytes(probe.Format.Size))
	for _, s := range probe.Streams {
		switch s.Type {
		case "video":
			fmt.Printf("  #%d 视频  %s  %dx%d @ %.2ffps\n", s.Index, s.Codec, s.Width, s.Height, s.FrameRate)
		case "audio":
			fmt.Printf("  #%d 音频  %s  %dHz %dch\n", s.Index, s.Codec, s.SampleRate, s.Channels)
		default:
			fmt.Printf("  #%d %s  %s\n", s.Index, s.Type, s.Codec)
		}
	}
	return nil
}

// ---- transcode ----

func cmdTranscode(args []string) error {
	fs := flag.NewFlagSet("transcode", flag.ContinueOnError)
	input := fs.String("i", "", "输入")
	output := fs.String("o", "", "输出")
	vc := fs.String("video-codec", "", "视频编码")
	ac := fs.String("audio-codec", "", "音频编码")
	preset := fs.String("preset", "", "编码预设")
	crf := fs.Int("crf", 0, "CRF")
	resolution := fs.String("resolution", "", "分辨率")
	ss := fs.Float64("ss", 0, "起点（秒）")
	to := fs.Float64("to", 0, "终点（秒）")
	ffmpeg := fs.String("ffmpeg", "", "ffmpeg 路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *output == "" {
		return errors.New("需要同时指定 -i 与 -o")
	}
	m := media.New(*ffmpeg)
	req := media.TranscodeRequest{
		Input: *input, Output: *output,
		VideoCodec: *vc, AudioCodec: *ac, Preset: *preset, CRF: *crf,
		Resolution: *resolution,
		Start:      time.Duration(*ss * float64(time.Second)),
		End:        time.Duration(*to * float64(time.Second)),
	}
	fmt.Println("命令：" + media.PreviewCommand(req))
	if err := m.Transcode(context.Background(), req, func(frac float64) {
		fmt.Printf("\r  %5.1f%%", frac*100)
	}); err != nil {
		return err
	}
	fmt.Printf("\n完成：%s\n", *output)
	return nil
}

// ---- models ----

func cmdModels(args []string) error {
	if len(args) == 0 || args[0] == "list" {
		fmt.Printf("%-16s %-8s %-10s %s\n", "ID", "集合", "体积", "名称")
		for _, m := range store.Catalog {
			fmt.Printf("%-16s %-8s %-10s %s\n", m.ID, m.Tier, m.Size, m.Name)
		}
		return nil
	}
	if args[0] != "download" {
		return fmt.Errorf("未知 models 子命令 %q", args[0])
	}
	cfg, _ := config.LoadFile("config.yaml", false)
	dir := data.ResolveDir(cfg.General.DataDir)
	modelsDir := cfg.General.ModelsDir
	if !filepath.IsAbs(modelsDir) {
		modelsDir = filepath.Join(data.ExeDir(), modelsDir)
	}
	_ = os.MkdirAll(modelsDir, 0o755)
	ids := args[1:]
	if len(ids) == 0 {
		return errors.New("请指定模型 id（可用 stt-sub models list 查看）")
	}
	for _, id := range ids {
		m, ok := store.ByID(id)
		if !ok {
			return fmt.Errorf("未知模型 %q", id)
		}
		fmt.Printf("下载 %s（%s）…\n", m.Name, m.Size)
		if err := store.Download(context.Background(), m, modelsDir, func(done, total int64) {
			if total > 0 {
				fmt.Printf("\r  %3.0f%%  %s / %s", 100*float64(done)/float64(total), data.HumanBytes(done), data.HumanBytes(total))
			}
		}); err != nil {
			return err
		}
		fmt.Printf("\n  完成 → %s\n", filepath.Join(modelsDir, m.Dest))
	}
	_ = dir
	return nil
}

// ---- doctor ----

func cmdDoctor() error {
	cfg, _ := config.LoadFile("config.yaml", false)
	cfg.Normalize()
	dir := data.ResolveDir(cfg.General.DataDir)
	app, err := server.NewApp(cfg, dir)
	if err != nil {
		return err
	}
	defer app.Close()
	env := app.CheckEnvironment()
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		return err
	}
	if !env.Ready {
		fmt.Println("\n还有未就绪的项目，请按上面的 fix 提示处理（也可在网页「设置」里查看）。")
	}
	return nil
}


// firstNonEmpty 返回第一个非空白字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
// modelJobs 保留对 jobs 包的显式引用，便于将来扩展 CLI 任务查询。
var _ = jobs.TypeTranscribe
var _ library.Project

