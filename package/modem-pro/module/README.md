# module/ 结构说明（改代码前先看这里）

> 这份文档只讲**代码放哪儿、为什么这么放**。产品用法见仓库根的 `README.md`。

## 1. 分层（依赖只能向下）

```
        main.go / web.go
              │
        module/kernel        ← 引擎：Modem 类型 + 全部方法（单一 package，按领域分文件）
              │
   ┌──────────┼───────────┬─────────────┐
厂商包      功能包      方言包         公共层
quectel…   cell/sms…   modedialect…   common / platform
   └──────────┴───────────┴─────────────┘
              │
            iface            ← 契约层（谁都不许 import 上层）
```

硬约束（`wsl-tools/verify-layering.sh` 自动检查，全部必须 0 处）：

| 规则 | 含义 |
|---|---|
| A | 功能包 / 厂商包**不许** import `module/kernel` |
| B | 功能包 / 厂商包**不许**出现 `*Modem`、`mod.SendAT` 之类内核状态 |
| C | `iface/` **不许** import 任何 `module/` |
| D | `module/common` **不许** import 具体厂商包 |
| I | 功能层里「按厂商命名」的声明必须在 `vendor_*.go` / `<品牌>.go` 里 |

## 2. 厂商差异分三类，放三处

| 差异 | 例子 | 放哪 | 为什么 |
|---|---|---|---|
| ① **发什么指令** | `AT+QTEMP` / `AT+CPMUTEMP` / `AT^CHIPTEMP` | `module/<vendor>` 实现 `iface` 的可选能力接口 | 每家话术必须一家一份；新增厂商 = 新增一个包，**内核 0 改动** |
| ② **同一功能几套写法** | 锁频段：QNW / GTACT / SP* / `^LTEFREQLOCK` / `AT!BAND` | `module/lock` 等方言包，**运行期探测**选一套 | 同一品牌不同固件都可能变，不能按品牌写死 |
| ③ **回什么格式** | `+QTEMP:"cpu",42` | 通用形状 → 功能包；品牌私有 → 功能包里的 `vendor_<品牌>.go` | 通用解析共享（否则改一处要改 6 遍）；私有格式才一家一份 |

**反例（已修）**：`atcmd.go` 曾让每个厂商类型重抄 `stdCommands` 的 14 个方法。
现在靠嵌入复用，`TestStdCommandsNotRedeclared` 是护栏。

## 2.1 品牌优先：一个品牌的**所有**功能都在 `module/<品牌>/`

厂商知识不再按功能分目录，而是按品牌收拢：

```
module/quectel/     atpreset.go  command.go  lock.go  mode.go  qnwlock.go  qnwparse.go  usb.go
module/fibocom/     airplane.go  atpreset.go  command.go  imei.go  lock.go  mode.go  usb.go  usbnet.go
module/unisoc/      atparse.go  atpreset.go  atpreset_suppress.go  command.go  lock.go  mode.go
module/thales/      atparse.go  cell.go  lock.go  mode.go  usb.go
…
```

| 文件名 | 内容 |
|---|---|
| `command.go` | `iface.ModemCommand`（嵌入 `common.StdCommands`） |
| `mode.go` | 组网模式方言（返回 `*modedialect.Dialect`） |
| `usb.go` | USB 网卡模式方言（返回 `*usbdialect.USBDialect`） |
| `imei.go` | IMEI 改写方言 |
| `lock.go` | 锁频段 / 锁小区方言 |
| `cell.go` | 服务小区 / 邻区 **私有格式**解析器 |
| `atparse.go` | 温度 / AMBR 等 **私有格式**解析器 |
| `atpreset.go` | 界面「AT 指令」预设 |

「框架」包（`cell` / `atparse` / `lock` / `modedialect` / `usbdialect`）
**不再 import 任何品牌包**（否则品牌包 import 框架取类型就成环）。
私有格式用**自注册**接回去：

```go
// module/cell/vendor_hooks.go（框架侧）
func RegisterServingCells(order int, fn ServingCellsParser, tags ...string)
func RegisterNeighbourCells(fn NeighbourCellsParser, tags ...string)

// module/telit/cell.go（品牌侧）
func init() { cell.RegisterServingCells(50, servingCell, "#CAINFOEXT") }
```

`atparse` 的温度解析同理：`atparse.RegisterTempParser(order, fn)`。
好处：**新增一家厂商 = 只加一个 `module/<品牌>/*.go`，框架包一 行都不用改**。

通用解析助手统一住在 `module/common`（`atkv.go` / `atcsv.go`）：
`SplitCSVFields` / `AtNumbers` / `NumInLine` / `ParseLabeledKV` / `FirstNumber` /
`NormalizePLMN` / `ValidPLMN` …（`lock` / `cell` / `modedialect` / `imeidialect`
里留同名别名，老代码不用改）。

## 3. 命名约定

| 包的性质 | 厂商文件命名 | 例子 |
|---|---|---|
| **品牌目录**（首选） | 按功能命名 | `module/telit/cell.go`、`module/fibocom/lock.go` |
| 功能包里的厂商补充 | `vendor_<品牌>.go` | `module/cell/vendor_entry.go`（剩下的统一入口） |
| 公共/框架 | 按作用命名 | `module/lock/bandlist.go`、`module/common/atkv.go` |

内核（`module/kernel`）按**领域**分文件：`api_*.go` / `sms_*.go` / `netprov_*.go` /
`quota_*.go` / `push_*.go` / `glue_*.go` / `wechat_*.go` / `lock_*.go` …
**改哪个接口就去对应文件**，单文件都在 1000 行以内。

## 4. 加一个新厂商，需要动哪些文件

以「新品牌 `foo`，高通平台，支持锁频段 + 温度查询」为例：

1. `module/foo/command.go` — 实现 `iface.ModemCommand`（嵌入 `common.StdCommands`），
   需要的话再实现 `iface.TempQuerier` / `iface.NetLocker` 等**可选能力**接口。
2. `module/catalog/` — 如果 VID:PID 能识别，登记型号（`pcie_vendor.go` / 设备表）。
3. `module/foo/mode.go` / `module/foo/usb.go` — 组网模式 / USB 模式的方言。
4. `module/foo/lock.go` — 锁频段方言（若与已有族相同就复用 `lock.NewBandListLocker`，不要抄）。
5. `module/foo/cell.go` — 只在**它有私有服务小区格式**时才加（记得 `init()` 里注册）。
6. `module/foo/atparse.go` — 私有温度 / AMBR 格式（记得 `init()` 里注册）。
7. `module/foo/imei.go` / `atpreset.go` — IMEI 改写方言 / AT 指令预设。
8. `module/registry/names.go` — 登记指令类型与方言名（界面显示用）。
9. `module/registry/iface_asserts.go` — 加一行编译期断言：
   `_ iface.ModemCommand = (*foo.FooCommand)(nil)`。
10. 需要被 `variant` 派发时，在 `module/variant/dispatch.go` 的 switch 里加一行。

**内核（`module/kernel`）一行都不用改** —— 这是这套分层成立与否的判据。

## 5. 工具（`wsl-tools/`，仅重构期使用，不参与构建）

| 工具 | 作用 |
|---|---|
| `check.sh [quick\|full]` | build + vet + test，结果写 `dump/check.txt` |
| `verify-layering.sh` | 上述 A/B/C/D/I 分层自检 + 规模统计 |
| `crossbuild.sh` | 5 个平台交叉编译（**必跑**：`*_windows.go` 在 linux 构建里不参与编译） |
| `verify-pkg-prepare.sh` | 按 `Makefile` 的 `Build/Prepare` 拷贝清单模拟 ipk 构建 |
| `split-file.py` / `distribute.py` | 按锚点切分大文件 / 按声明名分发到目标文件 |
| `split-pkg.py` / `split-symbols.py` | 跨包搬迁（带自动导出、引用展开、失败回滚） |
| `trim-imports.py` | 按实际使用裁剪 import |

## 6. 基线（改动后必须对齐）

```
build rc=0 / vet rc=0
PASS 1051 / FAIL 2 / SKIP 4      ← 2 个 FAIL 是历史遗留（TestHostDialKickDue / TestPCIeCtrlDeviceForModem）
交叉编译：linux amd64 / arm64 / mips / mipsle + windows amd64 全 rc=0
gofmt -l  → 0
```
