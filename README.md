# HeitingCLI

在终端里听 B 站直播：播放直播间音频、看弹幕、发弹幕，所有配置都在软件内完成。

基于 [shr-go/bili_live_tui](https://github.com/shr-go/bili_live_tui)（MIT）开发，保留了它的弹幕协议和 WBI 签名实现，其余部分重写。

## 安装

需要 Go 1.26+，不依赖 mpv、ffmpeg 或 cgo：

```bash
go install github.com/di404/HeitingCLI/cmd/heiting@latest
```

Linux 上需要系统运行着 PulseAudio（或 PipeWire 的兼容层），或者装有 ALSA 的 `libasound2`。

## 使用

```bash
heiting                                # 打开上次的直播间，首次运行时添加房间
heiting 7777                           # 按房间号打开
heiting https://live.bilibili.com/7777 # 按链接打开
```

### 按键

| 按键 | 功能 |
| --- | --- |
| `tab` | 切换到弹幕输入 / 返回 |
| `m` | 静音 |
| `+` `-` | 调节音量 |
| `p` | 播放 / 停止 |
| `r` | 房间列表 |
| `s` | 设置 |
| `↑` `↓` `j` `k` `PgUp` `PgDn` 鼠标滚轮 | 滚动弹幕 |
| `q` | 退出 |

### 房间列表

开播的房间排在最前面。`a` 添加（房间号或链接），`d` 删除，`r` 刷新，`enter` 进入。

### 设置

按 `s` 打开，修改后立即生效并自动保存。

- 音频：启动时播放、音量、缓冲秒数
- 弹幕：缓存条数、彩色弹幕、舰长标识、粉丝牌名称和等级
- 界面：显示直播标题、显示房间号
- 网络：User-Agent
- 账号：扫码登录、退出登录

未登录时不能发送弹幕，部分用户名会被 B 站打码。

## 音频实现

`getRoomPlayInfo` 接口带上 `only_audio=1` 后，FLV 线路返回的是真正的纯音频流，实测约 130 kbps（16 KB/s），不下载画面。这个参数不公开，只对 FLV 有效，只提供 HLS 的直播间无法播放。

播放在进程内完成：HTTP 拉流、解析 FLV、用 [go-aac](https://github.com/tphakala/go-aac) 解码 AAC-LC、用 [oto](https://github.com/ebitengine/oto) 输出到系统默认设备，软件内不能单独选择设备。

断流后自动重新取流，未开播时等待开播后自动播放。

## 文件位置

| 文件 | macOS | Linux |
| --- | --- | --- |
| 配置和登录状态 | `~/Library/Application Support/heiting/` | `~/.config/heiting/` |
| 日志和登录二维码 | `~/Library/Caches/heiting/` | `~/.cache/heiting/` |

登录 Cookie 保存在配置目录的 `cookie` 文件中，权限为仅当前用户可读。

## 许可证

[MIT](LICENSE)

依赖 go-aac 使用 LGPL-2.1-or-later 授权，HeitingCLI 通过 Go 模块引用它，可以替换其版本后重新编译。
