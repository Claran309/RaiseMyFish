# 0.12 看直播派发单 · 截图/听声（MVP 范围）

> 背景：P0 最后一大块。文件式流水线：录 10 秒 → 抽帧 → deepseek 读帧 → ASR 转写 → 失败降级。
> 视觉模型已实测可用：deepseek 走现有渠道支持 `image_url`（1x1 粉色图返回「solid light pink image」）。
> 本批只做 0.12，不碰 0.13/Presence。

## 允许路径

- 新增 `internal/watch/`（包：录制/抽帧/读图/转写/流水线）
- 配置：`internal/config/watch.go` + `config/fishd.toml` 的 `[watch]` 段
- 测试文件随实现
- 不碰 `contracts/`、迁移、人格/知识/调度/发送

## 模块设计（按此结构，勿改语义）

```go
package watch

// Clip 是一段录制结果。
type Clip struct {
    VideoPath string        // ffmpeg 录制的文件
    Frames    []string      // 抽帧输出路径
    AudioPath string        // 抽出的音频
    Seconds   int
}

// Recorder 录制直播流（或屏幕）指定秒数。
type Recorder interface {
    Record(ctx context.Context, seconds int) (string, error) // 返回视频路径
}

// Framer 从视频抽帧。
type Framer interface {
    Frames(ctx context.Context, videoPath string, count int) ([]string, error)
}

// Describer 读帧：调视觉模型描述画面。
type Describer interface {
    Describe(ctx context.Context, frames []string, prompt string) (string, error)
}

// Transcriber 转写音频。
type Transcriber interface {
    Transcribe(ctx context.Context, audioPath string) (string, error)
}
```

## 实现要求

1. **Recorder**：用 ffmpeg 录 B站直播流
   - 先调 B站 `room_play_info` API（`https://api.live.bilibili.com/xlive/web-room/v2/index/getRoomPlayInfo`，带 cookie）拿流地址
   - 拿不到流 → 返回明确错误（不编造）
   - `[watch] source = "stream"` 时走直播流；预留 `screen` 模式但可不实现
2. **Framer**：ffmpeg 抽 3~5 帧（均匀间隔），输出临时目录
3. **Describer**：调 Brain 的视觉能力（`image_url` base64 或本地路径转 base64），prompt 固定「描述画面：主播在做什么/画面文字/氛围」
4. **Transcriber**：调本地 ASR 服务（`http://127.0.0.1:8077/transcribe`，faster-whisper）——服务不可用返回错误
5. **Pipeline**：串联 + 失败降级
   - 任一步失败 → 降级（记日志 + 返回部分结果），**不阻塞弹幕闭环**
   - 黑屏检测：抽帧后做亮度检查（ffmpeg signalstats 或简单像素均值），过暗/过亮告警
   - 无音轨：ffprobe 检查，无音轨告警
6. **配置**：`[watch] enabled=false`（默认关）、`seconds=10`、`frames=4`、`asr_url`、`stream_quality`、`cookie_env`（复用 FISH_BILI_COOKIE）

## 禁止

- ❌ 不改 contracts、不碰调度/发送/人格/知识
- ❌ 不伪造出画面/字幕；ffmpeg/ASR/API 失败必须显式报错
- ❌ 不真实调用（测试用 mock；真调留验收时）
- ❌ 不执行 git

## 交付格式

- 文件列表、测试结果、配置说明、失败降级行为说明、未做项、是否暂停写入
