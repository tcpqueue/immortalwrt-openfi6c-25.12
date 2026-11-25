# modem-pro 的 OpenWrt 打包说明

这个目录现在**既是项目源码，也是一个 OpenWrt 包目录**：根目录的 `Makefile` 就是包定义。
放进 `lede/package/daocaoren/` 后，`make menuconfig` 里就能选，选中就直接打包，
**不需要改 lede 的任何原有文件**。

## 一个 Makefile 出两个包

| 包名 | 内容 | 依赖 |
| --- | --- | --- |
| `modemd-pro` | Go 主程序 + **four 家厂商 C 拨号器**（third_party 源码本包自己编）+ init.d / netifd 协议 / 默认配置 | `kmod-usb-serial-option`、`kmod-usb-wdm`、`adb-enablemodem（已从依赖中去掉：它要求编 adb，需从 google 源拉源码，OpenWrt 自带的不参与本次构建）`、`comgt`(+`-directip`/`-ncm`)、
  `modemband`、`umbim`、`uqmi` |
| `luci-app-modemd-pro` | LuCI 控制器 / JS 视图 / 菜单 / ACL | `modemd-pro`、`luci-base` |

界面单独成包（OpenWrt 里 LuCI 应用都是这么分的），选它会把 `modemd-pro` 一起带上；
但你不能只要界面不要拨号器 —— 拨号器属于 `modemd-pro`，这也是刻意的：

> **third_party 里的 C 代码不拆成独立 OpenWrt 包。**
> 它们由 `modemd-pro` 用**目标的交叉工具链**直接编成静态 musl 可执行文件，
> 和守护进程打进同一个 ipk。不依赖 zig（zig 那套是给 Windows 构建机准备的，
> 这里工具链是现成的），也不产生 `kmod-*` 之类的一堆子包。

## 装到哪儿

```
/usr/bin/modemd                                  Go 主程序
/usr/lib/modemd/quectel-CM                       移远（files/third_party/quectel-cm）
/usr/lib/modemd/simcom-cm                        芯讯通（files/third_party/simcom-cm/src）
/usr/lib/modemd/fibocom-dial                     广和通（files/third_party/fibocom-dial/src）
/usr/lib/modemd/meig-cm                          美格（files/third_party/meig-cm/src）
/etc/init.d/modemd                               procd 启动脚本（files/packaging/openwrt/files）
/lib/netifd/proto/quectel.sh                     netifd 的 proto=quectel
/etc/modemd/config.toml                          默认配置（conffile，升级不覆盖用户改动）
```

LuCI 侧（`luci-app-modemd-pro`）：`/usr/lib/lua/luci/...`、`/usr/share/ucode/luci/controller/...`、
`/usr/share/luci/menu.d/luci-app-modemd.json`、`/usr/share/rpcd/acl.d/luci-app-modemd.json`、
`/www/luci-static/resources/modemd/...`。安装脚本会按固件的 LuCI 类型自动只留 Lua 或 JS 一套。

## 设备中心访问

默认地址为 `http://<路由器地址>:9527/`。HTTP 模式仍需要 LuCI 会话，登录凭据与会话通过明文传输。

如需 HTTPS，在 `/etc/modemd/config.toml` 的 `[server]` 设置 `tls_enabled = true`，随后执行 `/etc/init.d/modemd restart`。
未指定证书及私钥路径时，服务在首次启动时生成自签证书和私钥，将它们保存在权限为 `0600` 的 `/etc/modemd/modemd-tls.pem` 并在后续启动复用。
本机主机名及已配置的 IP 自动加入证书。需要通过其他主机名访问时，可在首次生成前设置 `tls_names`，例如 `["openfi.lan", "192.168.1.1"]`。
若已有证书，可同时指定 `tls_cert_file` 和 `tls_key_file`。旧配置未写 `tls_enabled` 且已配置证书文件时仍使用 HTTPS。

自签证书需要在浏览器中建立信任或确认例外。启用 HTTPS 后请使用 `https://<路由器地址>:9527/`。

## 编译方法

```sh
cd ~/lede

# 首次需要 golang feed（提供 golang/host 和 golang-package.mk）
./scripts/feeds install -a -p golang

make menuconfig
#   Network -> modemd-pro                                    ← 守护进程 + 四家拨号器
#   LuCI -> 3. Applications -> luci-app-modemd-pro               ← 网页面板
# 在 modem-pro 的选项菜单里可以分别关掉不需要的厂商拨号器

# host go 是第一次编（约几分钟，编完就缓存住）
make package/lang/golang/golang/host/compile V=s -j$(nproc)

# 编本包
make package/daocaoren/modem-pro/compile V=s -j$(nproc)
```

产物：`bin/packages/<arch>/daocaoren/modem-pro_<版本>_<arch>.ipk` 与 `luci-app-modemd_...ipk`。

## 细节

- **版本号**只在 `files/packaging/openwrt/VERSION`（形如 `0.2.300-1`）里写一次，
  Makefile 读它生成 `PKG_VERSION` / `PKG_RELEASE`；改版本别改 Makefile。
- **Go 依赖拉取**走 `GOPROXY=https://goproxy.cn,direct`（`proxy.golang.org` 在国内不通）。
  想换：`make package/daocaoren/modem-pro/compile GOPROXY=https://你的代理,direct`。
- **postinst / prerm / postrm** 直接引用 `files/packaging/openwrt/control/` 和
  `files/packaging/openwrt/luci/control/` 里那几份脚本（与原 Windows 侧脚本用的是同一份（脚本已删），
  不会两处漂移）。这要求 GNU Make ≥ 4.2（Ubuntu 20.04 / Debian 11 起都满足）。
- ⚠️ LuCI 那份 postinst 里的 `__PKGVER__` **只替换带引号的那一处**（`__ver="__PKGVER__"`）。
  同一个文件里还有两处不带引号的：注释里的一处，以及紧跟的兜底
  `case "$__ver" in *__PKGVER__*) __ver="" ;; esac`。**全局替换会把那个 case 的模式
  也改成版本号** ⇒ 它必然命中自己 ⇒ 版本被清空、最后退成 `dev`（Windows 侧
  `原 Windows 打包脚本（已删）` 用的 `$piText.Replace('__PKGVER__', $Version)` 也是全局，同样会中招）。
  所以 Makefile 里用的是 `$(subst "__PKGVER__","<版本>",…)`，只动带引号的那一处。
- **拨号器编译开关**见下方清单（取自原 Windows 打包脚本，脚本已删）：源文件清单逐条对应上游 Makefile 的
  release 目标；fibocom 额外带 `-DGHT_FEATURE_PCIE_AUTO`，fibocom / meig 带
  `-std=gnu89 -Wno-error=return-type`（这批 gcc 时代的代码在严格模式下编不过）。
- **静态链接**：工具链里有 `libc.a` 就加 `-static`（换板子/换 libc 也能跑，等价于 zig 产物）；
  万一没有就自动退回动态链接，不会因此编不过。
- 想临时关掉某个厂商拨号器：`make menuconfig` 里 `modemd-pro` 选项菜单内取消勾选。

## 没有包含的东西

`files/third_party/kmods/` 里的厂商内核模块（`qmi_wwan_q`、`pcie_mhi`、`gobinet`、`sprd_pcie` …）
**没有**打进这两个包 —— Windows 侧原打包脚本出包时也不编它们（脚本已删）（它只编 Go 主程序 + 四家拨号器），
这里保持一致。那些是 `KernelPackage`，真要随固件发，按 OpenWrt 的规矩它们是独立的
`kmod-*` 包（那样才能被 `AUTOLOAD`/依赖关系正确管理）。如果你希望它们也塞进 `modemd-pro`
（不进独立包），说一声，加一段 `$(KERNEL_MAKE) M=... modules` 就行。

## ⚠️ 为什么 `third_party` 里两个厂商 Makefile 被改了名

`files/third_party/meig-cm/Makefile`、`files/third_party/fibocom-dial/Makefile` 是**厂商自带的独立 OpenWrt
包定义**（结尾是 `$(eval $(call BuildPackage,...))`）。lede 发现包的规则是（`include/scan.mk`）：

```sh
find package -maxdepth 5 -name Makefile | xargs grep -aHE 'call (BuildPackage|KernelPackage)'
```

这两个文件正好落在**深度 5 之内、内容也命中** ⇒ 只要它们还叫 `Makefile`，`make menuconfig` 里
就会多出 `meig-cm` / `fibocom-dial` 两个**独立的包**（正是"不要"的那种形态；选中还会把拨号器
装到 `/usr/bin` 去，和 `modemd-pro` 的 `/usr/lib/modemd/` 重复）。所以改成：

```sh
files/third_party/meig-cm/Makefile       ->  Makefile.vendor
files/third_party/fibocom-dial/Makefile  ->  Makefile.vendor
```

厂商内容**一个字没改**，只在文件开头加了几行说明注释。想单独出包时
`cp Makefile.vendor Makefile`，并放到 lede 树外去用。

这个深度是硬约束，顺便记一下当前各文件的处境：

| 文件 | 层级（相对 `package/`） | 会被扫到吗 |
| --- | --- | --- |
| `modem-pro/Makefile` | 2 | ✅ 本该如此 |
| `files/third_party/quectel-cm/Makefile` | 5 | ⚠️ 在窗口内，但内容是普通 Makefile，不命中 grep ⇒ 安全 |
| `files/third_party/{meig-cm,fibocom-dial}/Makefile` | 5 | ❌ 会命中 ⇒ **已改名** |
| `files/third_party/kmods/*/Makefile` | 6 | ✅ 超出 `maxdepth 5` ⇒ 扫不到 |
| `files/tools/script/Makefile` | 4 | ⚠️ 在窗口内，但不命中 grep ⇒ 安全 |

**如果哪天把 `modemd-pro` 整体上移一层**（例如放到 `package/modem-pro/`），
`files/third_party/kmods/*/Makefile` 就会进入扫描窗口 —— 那时同样要把它们改名（或保持现在这个层级）。
往 `files/third_party/` 里再丢厂商目录时也留意同一件事。
