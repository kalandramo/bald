package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/transport/webrtc"
	pionwebrtc "github.com/pion/webrtc/v4"
)

// 示例：SFU 架构的音视频会议服务器
func main() {
	fmt.Println("=== WebRTC SFU Server Example ===")

	// 配置 STUN/TURN 服务器（用于 NAT 穿透）
	webrtcConfig := pionwebrtc.Configuration{
		ICEServers: []pionwebrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
			// 如果有 TURN 服务器，可以添加：
			// {
			// 	URLs:       []string{"turn:your-turn-server.com:3478"},
			// 	Username:   "user",
			// 	Credential: "pass",
			// },
		},
	}

	// 创建支持媒体的 WebRTC API
	settingEngine := pionwebrtc.SettingEngine{}
	settingEngine.SetICETimeouts(5*time.Second, 5*time.Second, 2*time.Second)

	mediaEngine := &pionwebrtc.MediaEngine{}

	// 注册视频编解码器
	if err := mediaEngine.RegisterCodec(pionwebrtc.RTPCodecParameters{
		RTPCodecCapability: pionwebrtc.RTPCodecCapability{
			MimeType:     pionwebrtc.MimeTypeH264,
			ClockRate:    90000,
			Channels:     0,
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
			RTCPFeedback: nil,
		},
		PayloadType: 96,
	}, pionwebrtc.RTPCodecTypeVideo); err != nil {
		log.Error(context.Background(), "register H264 codec failed", "error", err)
	}

	if err := mediaEngine.RegisterCodec(pionwebrtc.RTPCodecParameters{
		RTPCodecCapability: pionwebrtc.RTPCodecCapability{
			MimeType:     pionwebrtc.MimeTypeVP8,
			ClockRate:    90000,
			Channels:     0,
			SDPFmtpLine:  "",
			RTCPFeedback: nil,
		},
		PayloadType: 96,
	}, pionwebrtc.RTPCodecTypeVideo); err != nil {
		log.Error(context.Background(), "register VP8 codec failed", "error", err)
	}

	// 注册音频编解码器
	if err := mediaEngine.RegisterCodec(pionwebrtc.RTPCodecParameters{
		RTPCodecCapability: pionwebrtc.RTPCodecCapability{
			MimeType:     pionwebrtc.MimeTypeOpus,
			ClockRate:    48000,
			Channels:     2,
			SDPFmtpLine:  "minptime=10;useinbandfec=1",
			RTCPFeedback: nil,
		},
		PayloadType: 111,
	}, pionwebrtc.RTPCodecTypeAudio); err != nil {
		log.Error(context.Background(), "register Opus codec failed", "error", err)
	}

	api := pionwebrtc.NewAPI(
		pionwebrtc.WithMediaEngine(mediaEngine),
		pionwebrtc.WithSettingEngine(settingEngine),
	)

	// 创建 WebRTC 服务器
	server := webrtc.NewServer(
		webrtc.WithAddress(":9999"),
		webrtc.WithPath("/signal"),
		webrtc.WithWebRTCAPI(api),
		webrtc.WithWebRTCConfiguration(webrtcConfig),
		webrtc.WithDataChannelLabel("wind"),
		webrtc.WithAllowAnyDataChannelLabel(true),
		webrtc.WithSocketConnectHandler(func(sessionId webrtc.SessionID, queries url.Values, connect bool) {
			if connect {
				log.Info(context.Background(), "webrtc client connected", "session", sessionId)
			} else {
				log.Info(context.Background(), "webrtc client disconnected", "session", sessionId)
			}
		}),
	)

	if server == nil {
		log.Error(context.Background(), "failed to create server")
		os.Exit(1)
	}

	// 注册消息处理器（用于数据通道信令）
	webrtc.RegisterServerMessageHandler(server, 1, func(sessionId webrtc.SessionID, msg *TestMessage) error {
		log.Info(context.Background(), "webrtc message received", "session", sessionId, "msg", msg)

		// 广播消息给所有客户端
		server.Broadcast(1, msg)

		return nil
	})

	// 启动服务器
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := server.Start(ctx); err != nil {
			log.Error(context.Background(), "webrtc server error", "error", err)
			os.Exit(1)
		}
	}()

	log.Info(context.Background(), "webrtc SFU server started", "addr", ":9999")
	log.Info(context.Background(), "webrtc signal endpoint", "url", "http://localhost:9999/signal")
	log.Info(context.Background(), "press Ctrl+C to stop")

	// 等待中断信号
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Info(context.Background(), "webrtc server shutting down")

	// 优雅关闭
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := server.Stop(shutdownCtx); err != nil {
		log.Error(context.Background(), "webrtc server stop failed", "error", err)
	}

	log.Info(context.Background(), "webrtc server stopped")
}

// TestMessage 测试消息结构
type TestMessage struct {
	Type      int    `json:"type"`
	Sender    string `json:"sender"`
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"`
}
