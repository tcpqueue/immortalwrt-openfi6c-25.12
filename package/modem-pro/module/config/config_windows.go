//go:build windows

package config

// ---- from: config/paths_windows.go ----

func defaultLogFile() string { return "modemd.log" }

// defaultPidFile Windows 下默认 PID 文件路径
func defaultPidFile() string { return "modemd.pid" }
