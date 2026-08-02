package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
)

var semanticVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)

func main() {
	version := flag.String("version", "", "不带 v 前缀的发布版本")
	configPath := flag.String("config", "wails.json", "Wails 配置文件路径")
	flag.Parse()

	if !semanticVersion.MatchString(*version) {
		fatalf("版本号 %q 不符合语义化版本格式", *version)
	}
	content, err := os.ReadFile(*configPath)
	if err != nil {
		fatalf("读取 %s 失败：%v", *configPath, err)
	}
	var config map[string]any
	if err := json.Unmarshal(content, &config); err != nil {
		fatalf("解析 %s 失败：%v", *configPath, err)
	}
	info, ok := config["info"].(map[string]any)
	if !ok {
		fatalf("%s 缺少 info 配置", *configPath)
	}
	info["productVersion"] = *version
	updated, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		fatalf("生成 Wails 配置失败：%v", err)
	}
	updated = append(updated, '\n')
	if err := os.WriteFile(*configPath, updated, 0o644); err != nil {
		fatalf("写入 %s 失败：%v", *configPath, err)
	}
	fmt.Printf("已把 %s 的产品版本更新为 %s\n", *configPath, *version)
}

func fatalf(format string, values ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", values...)
	os.Exit(1)
}
