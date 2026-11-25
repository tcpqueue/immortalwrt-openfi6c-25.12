package kernel

// ====================== 温度：读内核温度区（展锐 SIPC / 中兴 F50 专用） ======================
//
// 背景（2026-10-06 真机取证）：
// 中兴 F50 的 5G 基带就是 **SoC 自己**（Unisoc UMS9620），它的固件**没有任何温度 AT 指令**
// —— `AT+QTEMP` / `AT+CPMUTEMP` / `AT+MTSM` 全部回 `+CME ERROR: 4`，固件镜像里也搜不到
// TEMP 类命令名（只有内部 NV/DSM 变量 `vddmodem_thermal_control`）。**但是**内核把 SoC 上
// 19 个温度传感器都挂在 sysfs 上，其中四个就是基带的：
//
//	/sys/class/thermal/thermal_zone16/type = lte-thmzone   temp = 46655   (46.655 ℃)
//	/sys/class/thermal/thermal_zone17/type = nr0-thmzone   temp = 46898
//	/sys/class/thermal/thermal_zone18/type = nr1-thmzone   temp = 46169
//	/sys/class/thermal/thermal_zone15/type = mm-thmzone    temp = 46391
//	（其余在 AP 侧：apcpu0/apcpu1、gpu、big7、lit0-3、mid4-6、ai0/ai1）
//
// ⇒ 对这台机器温度走**零 AT** 的 sysfs，`temp` 的单位是**毫摄氏度**。
//
// ⚠️⚠️ **只给展锐 SIPC 这一族用**（`isMu300SIPCChan`）。USB 模组插在路由器上时，
// `/sys/class/thermal` 是**路由器 SoC** 的温度，与模组无关 —— 拿它当"模组温度"就是骗人。

import (
	"modemd/module/atparse"
	"modemd/module/netdev"
	"modemd/module/sysfs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// thermalClassRoot 内核温度区目录（可注入：单测里换成假目录）
var thermalClassRoot = "/sys/class/thermal"

// 温度区读数合理区间（℃）：毫摄氏度换算错、传感器坏掉都会给出怪值，宁可不报
const (
	thermalMinC = 0
	thermalMaxC = 125
)

// thermalZoneLabel 温度区类型 → 界面显示名。
//
// ⚠️ 命名刻意让 `PickSummary` 的关键词匹配认得出：
// 含 "5g" 的会被挑成 **5G 基带**关键点、含 "cpu" 的会被挑成 **CPU** 关键点。
// 展锐的 zone 命名（实测 F50 全部 19 个）：nr0/nr1 = 5G NR 接收链，lte = LTE 基带，
// mm = 模组多媒体（modem multimedia），apcpuN = 应用处理器核，big/lit/mid = 大/小/中核，
// gpu、ai0/ai1。认不出的照实显示 sysfs 名字，不硬编。
func thermalZoneLabel(zone string) string {
	z := strings.ToLower(strings.TrimSpace(zone))
	z = strings.TrimSuffix(z, "-thmzone")
	suffix := func(prefix string) string { return strings.TrimPrefix(z, prefix) }
	switch {
	case z == "nr0":
		return "5G NR0"
	case z == "nr1":
		return "5G NR1"
	case strings.HasPrefix(z, "nr"):
		return "5G NR" + suffix("nr")
	case z == "lte":
		return "LTE 基带"
	case z == "mm":
		return "模组多媒体"
	case strings.HasPrefix(z, "apcpu"):
		return "AP CPU" + suffix("apcpu")
	case z == "gpu":
		return "GPU"
	case z == "big7mid4":
		return "大核7+中核4" // 真机上这是 big7 与 mid4 的合并温度区
	case z == "mid5mid6":
		return "中核5+中核6" // 同理（mid5mid6-thmzone）
	case strings.HasPrefix(z, "big"):
		return "大核" + suffix("big")
	case strings.HasPrefix(z, "lit"):
		return "小核" + suffix("lit")
	case strings.HasPrefix(z, "mid"):
		return "中核" + suffix("mid")
	case strings.HasPrefix(z, "ai"):
		return "AI" + suffix("ai")
	}
	return zone // 认不出：照实显示 sysfs 里的 type
}

// thermalZoneTemps 读 root 下所有 `thermal_zone*`，返回传感器列表（读不到返回 nil）。
// root 可注入，单测在 Windows 上也能跑（假目录 + 假文本文件）。
func thermalZoneTemps(root string) []atparse.Sensor {
	dirs, err := filepath.Glob(filepath.Join(root, "thermal_zone*"))
	if err != nil || len(dirs) == 0 {
		return nil
	}
	sort.Strings(dirs)
	out := make([]atparse.Sensor, 0, len(dirs))
	used := map[string]bool{}
	for _, d := range dirs {
		zone := sysfs.ReadSysfsLine(filepath.Join(d, "type"))
		raw := sysfs.ReadSysfsLine(filepath.Join(d, "temp"))
		if zone == "" || raw == "" {
			continue
		}
		milli, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		c := (milli + 500) / 1000 // 毫摄氏度 → ℃（四舍五入，与 ParseGTLADC 一致）
		if c < thermalMinC || c > thermalMaxC {
			continue
		}
		name := thermalZoneLabel(zone)
		if used[name] { // 同名（罕见）加原 zone 名区分，别在界面上出现两个一样的
			name = name + " " + zone
		}
		used[name] = true
		out = append(out, atparse.Sensor{Name: name, Celsius: c})
	}
	if len(out) == 0 {
		return nil
	}
	atparse.SortDesc(out) // 界面按温度从高到低看
	return out
}

// thermalTemps 本机能不能用内核温度区当温度源：只有展锐 SIPC（模组 = SoC）才成立。
func (mod *Modem) thermalTemps() []atparse.Sensor {
	if mod == nil || !netdev.IsMu300SIPCChan(mod.ATPort) {
		return nil
	}
	return thermalZoneTemps(thermalClassRoot)
}

// thermalTempNote 温度来源说明（界面直接显示，纯文本、无 markdown 记号）。
const thermalTempNote = "来源：内核温度区 /sys/class/thermal（零 AT）。本机固件没有温度 AT 指令" +
	"（AT+QTEMP / AT+CPMUTEMP / AT+MTSM 都回 +CME ERROR: 4），但 5G 基带就是 SoC 自己，" +
	"内核里有 lte / nr0 / nr1 / mm 四个基带温度区，另有 AP CPU、GPU、大中小核与 AI 核。"
