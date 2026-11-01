package pipeline

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"
)

// WAVDuration 直接从 PCM WAV 头部计算时长，避免额外调用 ffprobe。
func WAVDuration(path string) (time.Duration, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()

	var header [12]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return 0, fmt.Errorf("读取 WAV 头失败：%w", err)
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return 0, errors.New("不是合法的 WAV 文件")
	}

	var channels uint16
	var sampleRate uint32
	var bitsPerSample uint16
	var dataSize uint32
	gotFmt, gotData := false, false

	chunk := make([]byte, 8)
parseLoop:
	for {
		if _, err := io.ReadFull(file, chunk); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return 0, err
		}
		size := binary.LittleEndian.Uint32(chunk[4:8])
		id := string(chunk[0:4])
		switch id {
		case "fmt ":
			body := make([]byte, size)
			if _, err := io.ReadFull(file, body); err != nil {
				return 0, err
			}
			if len(body) < 16 {
				return 0, errors.New("WAV fmt 段过短")
			}
			channels = binary.LittleEndian.Uint16(body[2:4])
			sampleRate = binary.LittleEndian.Uint32(body[4:8])
			bitsPerSample = binary.LittleEndian.Uint16(body[14:16])
			gotFmt = true
		case "data":
			if info, err := file.Stat(); err == nil {
				// ffmpeg 写出的 WAV 可能带 LIST 块，data 之后才是真正结尾；
				// 用文件大小反推更稳妥。
				rest := info.Size() - 44
				if int64(size) > rest && rest > 0 {
					dataSize = uint32(rest)
				} else {
					dataSize = size
				}
			} else {
				dataSize = size
			}
			gotData = true
			break parseLoop
		default:
			if size > math.MaxInt32 {
				return 0, errors.New("WAV 分块尺寸异常")
			}
			if _, err := file.Seek(int64(size), io.SeekCurrent); err != nil {
				return 0, err
			}
		}
		if size%2 == 1 {
			if _, err := file.Seek(1, io.SeekCurrent); err != nil {
				return 0, err
			}
		}
	}

	if !gotFmt || !gotData {
		return 0, errors.New("WAV 缺少 fmt 或 data 段")
	}
	byteRate := float64(sampleRate) * float64(channels) * float64(bitsPerSample) / 8
	if byteRate <= 0 {
		return 0, errors.New("WAV 头部参数非法")
	}
	return time.Duration(float64(dataSize) / byteRate * float64(time.Second)), nil
}
