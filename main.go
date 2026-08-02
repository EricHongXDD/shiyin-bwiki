package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

// assets 包含已经构建好的前端资源，因此最终程序不依赖 Node.js 运行时。
//
//go:embed all:frontend/dist
var assets embed.FS

// appIcon 是桌面窗口、应用简介和安装包共用的品牌图标。
//
//go:embed build/appicon.png
var appIcon []byte

func main() {
	app, err := NewApp()
	if err != nil {
		log.Fatalf("初始化应用失败：%v", err)
	}

	err = wails.Run(&options.App{
		Title:            "拾音 Shiyin · BWIKI 语音下载器",
		Width:            1440,
		Height:           900,
		MinWidth:         860,
		MinHeight:        640,
		BackgroundColour: options.NewRGB(245, 247, 251),
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind: []interface{}{
			app,
		},
		Windows: &windows.Options{
			Theme:                               windows.SystemDefault,
			BackdropType:                        windows.Mica,
			WebviewIsTransparent:                false,
			IsZoomControlEnabled:                false,
			DisablePinchZoom:                    true,
			EnableSwipeGestures:                 false,
			ResizeDebounceMS:                    8,
			WebviewGpuIsDisabled:                false,
			WebviewDisableRendererCodeIntegrity: false,
		},
		Mac: &mac.Options{
			Appearance:  mac.NSAppearanceNameAqua,
			DisableZoom: true,
			About: &mac.AboutInfo{
				Title:   "拾音 Shiyin",
				Message: "跨平台 BWIKI 语音下载器",
				Icon:    appIcon,
			},
		},
		Linux: &linux.Options{
			Icon:             appIcon,
			ProgramName:      "Shiyin",
			WebviewGpuPolicy: linux.WebviewGpuPolicyNever,
		},
	})
	if err != nil {
		log.Fatalf("启动应用失败：%v", err)
	}
}
