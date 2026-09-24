//go:build ignore

// 一次性校验：确认直插数据库的部署配置 JSON 能被后端正确解析。
// 运行：go run ./tools/verify_deployment_profile_json.go
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/nuomiiiii/lite/database/clients"
)

// 与写入服务器 client_deployment_profiles.config 的内容逐字节一致。
const raw = `{"platform":"linux","disable_auto_update":false,"ignore_unsafe_cert":false,"get_ip_addr_from_nic":false,"memory_include_cache":false,"enable_gpu":false,"enable_remote_control":true,"enable_ghproxy":false,"ghproxy":"","enable_custom_dir":false,"dir":"","enable_custom_service_name":false,"service_name":"","enable_include_nics":false,"include_nics":"","enable_exclude_nics":false,"exclude_nics":"","enable_include_mountpoints":false,"include_mountpoints":"","enable_interval":true,"interval":600,"enable_month_rotate":false,"month_rotate":0,"month_rotate_time":"00:00:00","month_rotate_timezone":"Asia/Shanghai"}`

func main() {
	var profile clients.DeploymentProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		fmt.Println("Unmarshal 失败:", err)
		os.Exit(1)
	}
	runtime := profile.RuntimeConfig()

	fmt.Printf("platform            = %s\n", profile.Platform)
	fmt.Printf("enable_remote_control = %v\n", profile.EnableRemoteControl)
	fmt.Printf("enable_interval       = %v\n", profile.EnableInterval)
	fmt.Printf("interval              = %v\n", profile.Interval)
	if runtime.Interval != nil {
		fmt.Printf("runtime.interval      = %v  <- agent 实际收到的 -i\n", *runtime.Interval)
	}
	if runtime.EnableGPU != nil {
		fmt.Printf("runtime.enable_gpu    = %v\n", *runtime.EnableGPU)
	}
	if runtime.MonthRotate != nil {
		fmt.Printf("runtime.month_rotate  = %v\n", *runtime.MonthRotate)
	}

	if !profile.EnableRemoteControl {
		fmt.Println("FAIL: 远程控制未开启")
		os.Exit(1)
	}
	if runtime.Interval == nil || *runtime.Interval != 600 {
		fmt.Println("FAIL: 下发间隔不是 600")
		os.Exit(1)
	}
	fmt.Println("OK: 配置可被后端解析，且会下发给 agent interval=600 / remote_control=true")
}
