package kernel

import (
	"modemd/module/netdev"
	"modemd/module/sysfs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ====================== PCIe（MHI / wwan）假 sysfs 测试 ======================
//
// 与 USB 那套测试同一思路（见 ports_unix_test.go 的 fakeSysfs / usbdesc_unix_test.go 的
// writeFile）：把 pciDevicesRoot / wwanClassRoot / netClassRoot / devRoot 指到 t.TempDir()
// 里搭出来的假树，验证"认不认得出这块卡、AT 候选口与控制口是什么、分组键怎么算"。
// 不用真机 —— 真机上插着模组时跑测试反而会去认真实的 /sys，把结论算到别的设备头上。

// testBDF 假树里用的 PCI 地址。
//
// ⚠️ Windows 的文件名**不能含冒号**，所以这里用 `0000-01-00.0` 这种退化写法
// （与 USB 那套假 sysfs 用 `1-1.0` 代替 `1-1:1.0` 同一个原因）；
// 生产代码（looksLikeBDF / pathHasBDF）两种写法都认。
const testBDF = "0000-01-00.0"

// fakePCIeSysfs 搭一棵假 sysfs 树：一块 PCIe 模组（BDF=testBDF）+ 一张 MHI 网卡。
//
// ⚠️ 不真建符号链接：Windows 上那需要特权（开发者模式/管理员）。改成注入一个假解析器
// （pcieEvalSymlinks），把“软链指向谁”直接告诉代码 —— 逻辑一样，开发机上也能跑。
//
// 返回 (pciRoot, wwanRoot, netRoot, devRootPath)。
func fakePCIeSysfs(t *testing.T, driver string, vid, class string) (string, string, string, string) {
	t.Helper()
	dir := t.TempDir()

	pciRoot := filepath.Join(dir, "bus", "pci", "devices")
	devDir := filepath.Join(pciRoot, testBDF)
	pcieMkdir(t, devDir)
	pcieWriteFile(t, filepath.Join(devDir, "vendor"), "0x"+vid+"\n")
	pcieWriteFile(t, filepath.Join(devDir, "device"), "0x0306\n")
	pcieWriteFile(t, filepath.Join(devDir, "class"), "0x"+class+"\n")
	if driver != "" {
		pcieMkdir(t, filepath.Join(dir, "bus", "pci", "drivers", driver))
	}

	netRoot := filepath.Join(dir, "class", "net")
	pcieMkdir(t, filepath.Join(netRoot, "wwan0"))

	wwanRoot := filepath.Join(dir, "class", "wwan")
	for _, p := range []string{"wwan0at0", "wwan0qmi0"} {
		pcieMkdir(t, filepath.Join(wwanRoot, p))
	}

	// /dev：MHI 的 AT 与控制口（**普通文件**即可 —— 只是名字参与枚举与分类）
	devRootPath := filepath.Join(dir, "dev")
	pcieMkdir(t, devRootPath)
	// 真实系统里 MHI / wwan 的口都会在 /dev 下建出节点，所以候选表是 glob /dev 得到的
	for _, f := range []string{"mhi_DUN", "mhi_QMI0", "wwan0at0", "wwan0qmi0", "ttyUSB0"} {
		pcieWriteFile(t, filepath.Join(devRootPath, f), "")
	}

	// 假软链解析：
	//   <dev>/driver            → <dir>/bus/pci/drivers/<driver>（驱动名是目录基名）
	//   <net>/<iface>/device    → 含 BDF 的路径（证明这张网卡归这块卡）
	//   <wwan>/<port>/device    → 同上
	oldEval := netdev.PcieEvalSymlinks
	netdev.PcieEvalSymlinks = func(p string) (string, error) {
		switch {
		case strings.HasSuffix(p, string(filepath.Separator)+"driver"):
			if driver == "" {
				return "", os.ErrNotExist // 没绑驱动
			}
			return filepath.Join(dir, "bus", "pci", "drivers", driver), nil
		case strings.HasSuffix(p, string(filepath.Separator)+"device"):
			return filepath.Join(dir, "devices", "pci0000-00", testBDF, filepath.Base(filepath.Dir(p))), nil
		}
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { netdev.PcieEvalSymlinks = oldEval })

	return pciRoot, wwanRoot, netRoot, devRootPath
}

func pcieMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建目录 %s 失败：%v", dir, err)
	}
}

// pcieWriteFile 写一个假 sysfs 属性文件（内核风格带换行）。
// 名字带前缀是为了不与 unix-only 测试文件里的 writeFile（usbdesc_unix_test.go）撞名 ——
// 本文件要跨平台跑（Windows 上也要能验证判定逻辑）。
func pcieWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// withPCIeFakeSysfs 把包级路径变量换成假树，测试结束自动还原。
func withPCIeFakeSysfs(t *testing.T, driver, vid, class string) {
	t.Helper()
	pciRoot, wwanRoot, netRoot, dev := fakePCIeSysfs(t, driver, vid, class)
	oldPCI, oldWwan, oldNet, oldDev := netdev.PciDevicesRoot, netdev.WwanClassRoot, sysfs.NetClassRoot, sysfs.DevRoot
	netdev.PciDevicesRoot, netdev.WwanClassRoot, sysfs.NetClassRoot, sysfs.DevRoot = pciRoot, wwanRoot, netRoot, dev
	t.Cleanup(func() {
		netdev.PciDevicesRoot, netdev.WwanClassRoot, sysfs.NetClassRoot, sysfs.DevRoot = oldPCI, oldWwan, oldNet, oldDev
	})
}

// TestPCIeListDetectsBoundModem 绑着 mhi_pci_generic 的设备要认成模组，
// 并把网卡 / AT 候选口 / QMI 控制口都归到它身上。
func TestPCIeListDetectsBoundModem(t *testing.T) {
	withPCIeFakeSysfs(t, "mhi_pci_generic", "17cb", "0b40")

	devs := netdev.ListPCIModemDevices()
	if len(devs) != 1 {
		t.Fatalf("应该认出 1 块 PCIe 模组，实际 %d 块：%+v", len(devs), devs)
	}
	d := devs[0]
	if d.BDF != testBDF {
		t.Errorf("BDF = %q，想要 %q", d.BDF, testBDF)
	}
	if !d.Bound || d.Driver != "mhi_pci_generic" {
		t.Errorf("驱动 = %q（bound=%v），想要 mhi_pci_generic", d.Driver, d.Bound)
	}
	if !d.MHI {
		t.Errorf("mhi_pci_generic 应标记为 MHI")
	}
	if d.VID != "17cb" || d.PID != "0306" {
		t.Errorf("VID:PID = %q:%q，想要 17cb:0306", d.VID, d.PID)
	}
	if !contains(d.Netdevs, "wwan0") {
		t.Errorf("网卡应包含 wwan0，实际 %v", d.Netdevs)
	}
	if !contains(d.ATPorts, "wwan0at0") && len(d.ATPorts) == 0 {
		t.Errorf("AT 候选口不该为空（wwan0at0 在假树里）")
	}
	if d.QMICtrl == "" {
		t.Errorf("QMI 控制口应认出来（假树里有 wwan0qmi0）")
	}
}

// TestPCIeUnboundDeviceStillListed 没绑驱动的模组也要列出来（带"去绑驱动"的说明），
// 否则用户插上卡之后页面上什么都看不到，根本不知道该装哪个 kmod。
func TestPCIeUnboundDeviceStillListed(t *testing.T) {
	withPCIeFakeSysfs(t, "", "17cb", "0b40")

	devs := netdev.ListPCIModemDevices()
	if len(devs) != 1 {
		t.Fatalf("没绑驱动的模组也要列出来，实际 %d 块", len(devs))
	}
	if devs[0].Bound {
		t.Errorf("没绑驱动时 Bound 应为 false")
	}
	if !strings.Contains(devs[0].Note, "驱动") {
		t.Errorf("应提示驱动没绑：%q", devs[0].Note)
	}
}

// TestPCIeIgnoresNonModemPCIe 不能把别的东西当模组：
//
//	① 绑着 ath11k（无线网卡）的 17cb 设备 —— 17cb 是高通通用 VID，只看 VID 就会误判；
//	② 没绑驱动、class 是 0x0280（网络控制器）的 17cb 设备 —— 不是无线控制器，不当模组。
func TestPCIeIgnoresNonModemPCIe(t *testing.T) {
	withPCIeFakeSysfs(t, "ath11k", "17cb", "0280")
	if devs := netdev.ListPCIModemDevices(); len(devs) != 0 {
		t.Fatalf("绑着 ath11k 的设备不该算模组：%+v", devs)
	}
	withPCIeFakeSysfs(t, "", "17cb", "0280")
	if devs := netdev.ListPCIModemDevices(); len(devs) != 0 {
		t.Fatalf("class=0x0280 的未绑定设备不该算模组：%+v", devs)
	}
}

// TestPCIeATPortCandidatesAndQMI AT 候选口只收 AT 类口，QMI 控制口单独一组
// （把 QMI 口当 AT 口去发 AT 是白费探测时间，还可能把控制通道搅乱）。
func TestPCIeATPortCandidatesAndQMI(t *testing.T) {
	withPCIeFakeSysfs(t, "mhi_pci_generic", "17cb", "0b40")

	at := netdev.PcieATPortCandidates()
	if !contains(at, "wwan0at0") {
		t.Errorf("AT 候选口应含 wwan0at0，实际 %v", at)
	}
	for _, name := range at {
		if strings.Contains(strings.ToLower(name), "qmi") {
			t.Errorf("QMI 口不该出现在 AT 候选里：%v", at)
		}
	}
	qmi := netdev.PcieQMIControlCandidates()
	if !contains(qmi, "wwan0qmi0") {
		t.Errorf("QMI 候选口应含 wwan0qmi0，实际 %v", qmi)
	}
	if !contains(qmi, "mhi_QMI0") {
		t.Errorf("QMI 候选口应含 mhi_QMI0，实际 %v", qmi)
	}
}

// TestIsCharATDevice 分类：MHI / wwan 的字符设备算"非 tty AT 口"，串口与 COM 不算。
// 判据决定了走裸 open 还是走 tarm/serial（见 atdev.go）。
func TestIsCharATDevice(t *testing.T) {
	yes := []string{"/dev/mhi_DUN", "/dev/mhi_QMI0", "/dev/wwan0at0", "/dev/wwan0qmi0", "qcqmi0"}
	for _, n := range yes {
		if !netdev.IsCharATDevice(n) {
			t.Errorf("%s 应算字符设备 AT 口", n)
		}
	}
	no := []string{"/dev/ttyUSB0", "/dev/ttyACM0", "COM3", `\\.\COM3`, ""}
	for _, n := range no {
		if netdev.IsCharATDevice(n) {
			t.Errorf("%s 不该算字符设备 AT 口", n)
		}
	}
}

// TestPCIeGroupKey 同一台模组的 AT 口 / QMI 口必须归到同一组，
// 否则发现流程会把它们当三台模组分别探测（每台都探一遍 AT，白费时间还乱）。
func TestPCIeGroupKey(t *testing.T) {
	withPCIeFakeSysfs(t, "mhi_pci_generic", "17cb", "0b40")

	if k := netdev.PcieGroupKeyForPort("/dev/wwan0at0"); k != "/wwan/wwan0" {
		t.Errorf("wwan0at0 的分组键 = %q，想要 /wwan/wwan0", k)
	}
	if k := netdev.PcieGroupKeyForPort("/dev/wwan0qmi0"); k != "/wwan/wwan0" {
		t.Errorf("wwan0qmi0 的分组键 = %q，想要 /wwan/wwan0", k)
	}
	// MHI 的字符设备名字里没有控制器号：能反查到唯一一块模组就归它
	if k := netdev.PcieGroupKeyForPort("/dev/mhi_DUN"); k != "/pci/"+testBDF {
		t.Errorf("mhi_DUN 的分组键 = %q，想要 /pci/%s", k, testBDF)
	}
	if k := netdev.PcieGroupKeyForPort("/dev/ttyUSB0"); k != "" {
		t.Errorf("USB 串口不该有 PCIe 分组键，实际 %q", k)
	}
}

// TestPCIeInfoKind 协议判定：有 QMI 控制口 → qmi；只有 MBIM → mbim；都没有 → 空（不猜）。
func TestPCIeInfoKind(t *testing.T) {
	cases := []struct {
		name string
		info PCIeInfo
		want string
	}{
		{"QMI 控制口", PCIeInfo{QMICtrl: "/dev/mhi_QMI0"}, "qmi"},
		{"只有 MBIM", PCIeInfo{MBIMDev: "/dev/wwan0mbim0"}, "mbim"},
		{"两个都有（先 QMI）", PCIeInfo{QMICtrl: "/dev/mhi_QMI0", MBIMDev: "/dev/wwan0mbim0"}, "qmi"},
		{"都没有", PCIeInfo{}, ""},
	}
	for _, c := range cases {
		if got := c.info.Kind(); got != c.want {
			t.Errorf("%s：Kind() = %q，想要 %q", c.name, got, c.want)
		}
	}
	if got := (*PCIeInfo)(nil).Kind(); got != "" {
		t.Errorf("nil 的 Kind() 应为空串，实际 %q", got)
	}
}

// TestHostDialArgDevSkipsIfaceForPCIe 拨号器的 `-i`：PCIe 模组不传（让拨号器自己认 MHI），
// USB 模组照传 —— 这条是从 vendored 的 quectel-CM 源码（-i 是筛选语义）得出的结论，
// 改错会让 PCIe 模组全都拨不上号。
func TestHostDialArgDevSkipsIfaceForPCIe(t *testing.T) {
	// PCIe：AT 口是 MHI 字符设备 → 不传 -i（即使我们找到了网卡名）
	pcieMod := &Modem{ID: 1, ATPort: "/dev/mhi_DUN"}
	if got := hostDialArgDev(pcieMod, "wwan0"); got != "" {
		t.Errorf("PCIe 模组不该传 -i，实际传了 %q", got)
	}
	// USB：照旧传
	usbMod := &Modem{ID: 2, ATPort: "/dev/ttyUSB2"}
	if got := hostDialArgDev(usbMod, "wwan0"); got != "wwan0" {
		t.Errorf("USB 模组应传 -i wwan0，实际 %q", got)
	}
}

// TestPCIeHostSetupText PCIe 模组的"主机侧还要做"说明必须是 PCIe 口径：
// 不能说 `-i wwan0`（MHI 上不能传 -i）也不能说 `/dev/cdc-wdm0`（那是 USB 的控制口）。
// 这段文字是直接显示给用户照抄的，写错了比不写还糟。
func TestPCIeHostSetupText(t *testing.T) {
	qmi := &DialTypeInfo{PCIe: &PCIeInfo{QMICtrl: "/dev/mhi_QMI0", Netdevs: []string{"wwan0"}}}
	txt := pcieHostSetup(qmi)
	if !strings.Contains(txt, "/dev/mhi_QMI0") {
		t.Errorf("QMI 说明应点名 MHI 控制口：%q", txt)
	}
	if !strings.Contains(txt, "不要传 `-i`") {
		t.Errorf("QMI 说明必须写明不要传 -i（否则用户照抄会拨不上号）：%q", txt)
	}
	if strings.Contains(txt, "-i wwan0") {
		t.Errorf("不能给出 `-i wwan0` 这种 USB 口径的写法：%q", txt)
	}

	mbim := &DialTypeInfo{PCIe: &PCIeInfo{MBIMDev: "/dev/wwan0mbim0"}}
	txt = pcieHostSetup(mbim)
	if !strings.Contains(txt, "umbim") || !strings.Contains(txt, "/dev/wwan0mbim0") {
		t.Errorf("MBIM 说明应给出 umbim + wwan 控制口：%q", txt)
	}

	// 没有控制口的 PCIe 模组：说明要指向内核模块排查，而不是编一个拨号命令
	none := &DialTypeInfo{PCIe: &PCIeInfo{BDF: "0000:01:00.0"}}
	txt = pcieHostSetup(none)
	if !strings.Contains(txt, "kmod") {
		t.Errorf("没有控制口时应提示检查内核模块：%q", txt)
	}

	// 非 PCIe（或 nil）不给这段文字（走原来的 USB 口径）
	if got := pcieHostSetup(&DialTypeInfo{}); got != "" {
		t.Errorf("非 PCIe 不该返回 PCIe 说明，实际 %q", got)
	}
}

// TestPCIeDeviceAge PCIe 模组也有"刚上电要等它启动"的问题：年龄判据 = PCI 设备目录 mtime。
func TestPCIeDeviceAge(t *testing.T) {
	withPCIeFakeSysfs(t, "mhi_pci_generic", "17cb", "0b40")

	// 目录刚建出来 → 年龄很小
	if d, ok := netdev.PcieDeviceAge("/dev/mhi_DUN"); !ok {
		t.Fatalf("应能算出 PCIe 模组的年龄")
	} else if d > time.Minute {
		t.Errorf("刚建的目录年龄不该超过 1 分钟，实际 %v", d)
	}
	// 非 PCIe 口 → 查不到
	if _, ok := netdev.PcieDeviceAge("/dev/ttyUSB0"); ok {
		t.Errorf("USB 串口不该有 PCIe 年龄")
	}
}
