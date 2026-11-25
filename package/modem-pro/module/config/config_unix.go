//go:build !windows

package config

// ---- from: config/paths_unix.go ----

func defaultLogFile() string { return "/var/log/modemd.log" }

// defaultPidFile Linux/Unix 下默认 PID 文件路径
func defaultPidFile() string { return "/var/run/modemd.pid" }
