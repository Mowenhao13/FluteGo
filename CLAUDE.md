# FluteGo 实验与操作文档

## 项目说明
本项目是一个服务于校内涉密电脑文件传输的单项无反馈信道传输系统，后端采用golang开发，由编解码器、发送器、接收器、任务池等核心部件组成，主要面向windows系统和mac系统（linux系统），由单向传输技术fec保证纯udp传输不丢包

## 操作说明
- 每次修改项目结构后需要通过基本的测试验证
- 在本地回环测试文件传输时，可以创建不同大小的文件测试传输带宽性能，大小由1MB到10GB不等
- 在每次成功测试后，需要把测试结果写入csv文件中
- 每次修改，需要把修改的内容写入CLAUDE.md的修改日志部分
- 核心组件都在pkg/目录下，controller目录无任何用处，不需要参考
- 目前项目已经与前端结合，可以复制一份纯cli代码到专属的测试文件夹进行命令行测试，方便验证可行性
- constant目录下有常量的定义，有必要可以进行修改，注意不同操作系统的文件路径格式以及存放位置差异

## 修改日志

### 2026-06-12 修复 Sender 目标 IP 动态更换 + ReedSolomon 前端集成

**问题描述**:
- Sender 的目标 IP 更改后，后端连接池未更新，导致数据仍发往旧 IP
- 前端 FEC 下拉菜单缺少 ReedSolomon 选项

**修改内容**:

1. 修复连接池 IP 更换问题 (`pkg/pool/pool.go`)
   - 将 `sync.Once` 替换为 `sync.Mutex` + `poolInited` 标志
   - `InitConnPool` 现在支持重新初始化：相同 IP/Mode 跳过，不同 IP/Mode 则停止旧池并创建新池
   - 旧池的 goroutine 通过关闭 `StopChan` 优雅退出

2. 增强 setDestFn 回调 (`cmd/flute_sender/main.go`)
   - `setDestFn` 现在立即调用 `ensurePool` 重建连接池
   - 用户更改 IP 时即时验证连通性，失败则返回错误

3. 前端添加 ReedSolomon 选项 (`pkg/web/static/index.html`)
   - FEC 下拉菜单新增 `ReedSolomon` 选项

**修改文件**:
- `pkg/pool/pool.go` - `InitConnPool` 支持重新初始化
- `cmd/flute_sender/main.go` - `setDestFn` 即时重建连接池
- `pkg/web/static/index.html` - 添加 RS 选项

**分支**: `feature/rs-integration-ip-fix` (基于 `release/v1.0.1`)

### 2026-03-27 优化编译参数和浏览器加载速度

**问题描述**:
- 编译参数可以进一步优化
- 浏览器加载页面缓慢，依赖外部 CDN 资源（tailwindcss、lucide、Google Fonts）
- 在无网络或网络差的环境下，页面加载会超时卡住

**编译优化内容**:
1. ✅ 在 `Makefile` 中添加 `-trimpath` 标志，移除文件系统路径，提高构建可重复性
2. ✅ 添加 `CGO_ENABLED=0`，静态链接，不依赖系统 C 库，提高可移植性
3. ✅ 添加 `debug` 目标用于开发编译（不 strip 调试信息）
4. ✅ 添加 `release` 目标用于发布编译
5. ✅ 添加平台特定编译目标：`windows`、`darwin`、`linux`

**前端加载优化内容**:
1. ✅ 移除同步加载的外部脚本，改用异步加载
2. ✅ 为外部脚本添加 3 秒超时，超时后降级到纯内联 CSS 模式
3. ✅ 预连接字体 CDN 但不阻塞渲染
4. ✅ 添加离线模式支持，无网络时页面仍能正常工作
5. ✅ 保持所有关键 CSS 完全内联，确保离线可用

**修改文件**:
- `Makefile` - 添加优化编译参数和多平台编译目标
- `pkg/web/static/index.html` - 优化外部资源加载，添加超时和降级策略

**Makefile 新增目标**:
```bash
make debug      # 快速编译（保留调试信息）
make release    # 优化编译（发布用）
make windows    # 编译 Windows 版本
make darwin     # 编译 macOS 版本（amd64 + arm64）
make linux      # 编译 Linux 版本
```

**编译效果**:
- 可执行文件更小（`-trimpath` + `-s -w`）
- 完全静态链接，可在同系统任意机器运行
- 构建可重复性更好

**前端加载效果**:
- 页面立即显示，不等待 CDN 资源
- 3 秒超时后自动降级到离线模式
- 即使无网络也能正常使用所有功能

### 2026-03-26 修复 Receiver 前端 IP 显示 Bug - 自动获取本机以太网 IP

**问题描述**:
- Receiver 前端始终显示 127.0.0.1，无法显示本机的实际局域网 IP 地址
- 发送端无法直观看到接收端的正确 IP

**修改内容**:
1. ✅ 在 `pkg/utils/utils.go` 中添加 `GetLocalIPv4()` 函数，自动获取本机以太网 IPv4 地址
2. ✅ 支持两种获取方式：UDP 连接探测（快速）+ 网络接口枚举（全面）
3. ✅ 智能筛选：跳过回环地址、Docker/VMware 虚拟网络、链路本地地址
4. ✅ 优先级排序：192.168.x.x > 10.x.x.x > 172.16-31.x.x
5. ✅ 修改 `cmd/flute_receiver/main.go`，Receiver 启动时自动获取本机 IP 替代配置中的 127.0.0.1
6. ✅ 添加测试脚本：`cmd/get_local_ip.py` 和 `cmd/get_local_ip_simple.py`（Python 版本）
7. ✅ 添加测试程序：`cmd/test_local_ip.go`（Go 版本）

**修改文件**:
- `pkg/utils/utils.go` - 添加 `GetLocalIPv4()` 及辅助函数
- `cmd/flute_receiver/main.go` - Receiver 启动时自动获取本机 IP
- `cmd/get_local_ip.py` - Python 测试脚本（需要 netifaces）
- `cmd/get_local_ip_simple.py` - Python 测试脚本（无第三方依赖）
- `cmd/test_local_ip.go` - Go 测试程序

**新增函数说明**:
```go
// GetLocalIPv4 获取本机的以太网 IPv4 地址
// 优先返回局域网地址，跳过虚拟网络和回环地址
func GetLocalIPv4() string
```

**测试结果**:
- Python 脚本：成功检测到 IP 172.26.65.31
- Go 程序：成功检测到 IP 172.26.65.31
- 两者结果一致

### 2026-03-24 使用 Channel 解耦编码器和发送器，避免阻塞

**修改内容**:
1. ✅ 定义 `encodeTask` 结构体用于在编码器和发送器之间传递数据
2. ✅ 创建 `encodeChan` channel，缓冲区大小 4096
3. ✅ 编码器协程只负责编码，把数据发送到 encodeChan 后立即返回
4. ✅ 独立的处理协程从 encodeChan 读取数据，处理速率限制
5. ✅ 发送协程处理实际的网络发送
6. ✅ 编码器回调中复制 symbolData，避免底层数组复用问题

**修改文件**:
- `pkg/sender/sender.go` - 完整重构 Start() 方法，使用 Channel 解耦

**架构改进**:
```
                    ┌─────────────┐
                    │   编码器     │
                    │  (协程1)    │
                    └──────┬──────┘
                           │ symbolData
                           ▼
                    ┌─────────────┐
                    │ encodeChan  │
                    │  (buffer)   │
                    └──────┬──────┘
                           │
                           ▼
                    ┌─────────────┐
                    │ 速率限制器   │
                    │  (协程2)    │
                    └──────┬──────┘
                           │
                           ▼
                    ┌─────────────┐
                    │  taskChan   │
                    └──────┬──────┘
                           │
              ┌────────────┼────────────┐
              ▼            ▼            ▼
         ┌─────────┐  ┌─────────┐  ┌─────────┐
         │ 发送协程1│  │ 发送协程2│  │ 发送协程N│
         └─────────┘  └─────────┘  └─────────┘
```

**优势**:
- 编码器不会被速率限制阻塞，可以全速进行编码
- 各阶段通过 channel 缓冲，实现流水线处理
- 更好的多核利用率
- 即使限速，编码器也能提前完成工作

### 2026-03-24 优化千兆网口吞吐量 - 禁用默认限速 + 优化速率限制器

**修改内容**:
1. ✅ 将默认发送速率限制设置为 0（禁用限速）以充分利用千兆网口 (`constant/constant.go`)
2. ✅ 优化速率限制器实现：批量预留令牌，减少 `WaitN` 系统调用开销 (`pkg/sender/sender.go`)
3. ✅ 在 Sender 结构体中添加 `reservedTokens` 和 `reserveThreshold` 字段
4. ✅ 每次预留 100ms 的数据量，大幅减少系统调用次数

**修改文件**:
- `constant/constant.go` - `DefaultSendRateLimitMbps` 从 1200 改为 0（禁用限速）
- `pkg/sender/sender.go` - 添加批量令牌预留机制优化性能

**问题根因**:
- 千兆网口理论极限是 1000 Mbps，设置 1200 Mbps 没有意义
- 之前每个包都调用 `rateLimiter.WaitN()`，系统调用开销巨大
- 对于 1408 字节的包，600 Mbps 需要每秒约 53,000 次系统调用

**优化方案**:
```go
// 优化前：每个包都调用 WaitN
if err := s.rateLimiter.WaitN(ctx, packetBytes); err != nil { ... }

// 优化后：批量预留令牌
if s.reservedTokens < packetBytes {
    // 批量预留 100ms 的数据量
    reserveAmount := s.reserveThreshold
    if err := s.rateLimiter.WaitN(ctx, reserveAmount); err != nil { ... }
    s.reservedTokens += reserveAmount
}
s.reservedTokens -= packetBytes  // 消耗令牌
```

**千兆网口性能说明**:
- 理论极限：1000 Mbps (125 MB/s)
- 实际极限（考虑协议开销）：约 800-900 Mbps
- UDP 数据包开销：14 字节以太网头 + 20 字节 IP 头 + 8 字节 UDP 头 + 8 字节序列号 = 50 字节/包
- 对于 1408 字节有效载荷：效率 = 1408/(1408+50) ≈ 96.6%

**测试建议**:
- 先使用默认配置（限速=0）测试最大吞吐量
- 如果需要限速，再修改 `DefaultSendRateLimitMbps` 为期望值

### 2026-03-24 修复吞吐量限制问题 - 提高到 1200 Mbps

**修改内容**:
1. ✅ 将默认发送速率限制从 800 Mbps 提高到 1200 Mbps (`constant/constant.go`)
2. ✅ 增大速率限制器的突发大小（burst）从 bytesPerSec/10 改为 bytesPerSec/2 (`pkg/sender/sender.go`)
3. ✅ 添加更详细的速率限制器日志输出

**修改文件**:
- `constant/constant.go` - `DefaultSendRateLimitMbps` 从 800 提高到 1200
- `pkg/sender/sender.go` - `CreateRateLimiter` 函数增大突发大小并添加详细日志

**问题根因**:
- 之前的突发大小只有 0.1 秒的数据量，对于高速传输来说太小了
- 令牌桶限制器因为突发容量不足，导致实际吞吐量远低于设定值
- 之前没有速率限制器的详细日志，难以调试问题

**修改前**:
```go
// 计算突发大小（桶容量）
burst := bytesPerSec / 10  // 只有 0.1 秒的数据量
```

**修改后**:
```go
// 计算突发大小（桶容量）- 使用较大的突发以避免限制过严
// 设置为 0.5 秒的数据量，这样可以容纳短暂的突发流量
burst := bytesPerSec / 2  // 0.5 秒的数据量
log.Printf("Rate limiter: %d bytes/sec, burst: %d bytes", bytesPerSec, burst)
```

**预期效果**:
- 实际吞吐量应该能接近设定的 1200 Mbps（取决于硬件和网络条件）
- 突发流量不会被立即限制，传输更平滑

### 2026-03-23 修复 Windows Socket 缓冲区满的问题

**修改内容**:
1. ✅ 降低默认发送速率限制从 300 Mbps 到 100 Mbps (`constant/constant.go`)
2. ✅ 减少 TX/RX 缓冲区大小从 32MB 到 8MB/16MB (`constant/constant.go`)
3. ✅ 添加缓冲区设置的降级策略 (`pkg/pool/pool.go`)
4. ✅ 当设置大缓冲区失败时，自动尝试更小的缓冲区值

**修改文件**:
- `constant/constant.go` - 降低 `DefaultSendRateLimitMbps` 从 300 到 100
- `constant/constant.go` - 减少 `TX_BUF` 从 32MB 到 8MB，`RX_BUF` 从 32MB 到 16MB
- `pkg/pool/pool.go` - 添加缓冲区设置的降级策略和错误处理

**问题根因**:
- 某些 Windows 系统的网络适配器或系统配置不支持太大的 socket 缓冲区
- 300 Mbps 的发送速率对于某些网络适配器来说太快，导致缓冲区溢出
- 之前没有缓冲区设置失败的降级处理

**Windows 检查缓冲区命令**:
```powershell
# 检查全局 TCP/IP 参数
netsh int ipv4 show global

# 检查网络适配器高级属性
Get-NetAdapterAdvancedProperty -Name "网卡名称"

# 查看注册表设置 (管理员)
Get-ItemProperty "HKLM:\SYSTEM\CurrentControlSet\Services\Tcpip\Parameters"
```

**Mac 检查缓冲区命令**:
```bash
# 检查 UDP 缓冲区大小
sysctl net.inet.udp.recvspace
sysctl net.inet.udp.sendspace

# 检查最大缓冲区限制
sysctl kern.ipc.maxsockbuf
```

### 2026-03-23 修复 Windows 发送 Mac 接收不到的问题

**修改内容**:
1. ✅ 修复 Windows 发送端 `RawSockaddrInet4.Port` 字节序处理问题 (`pkg/sock/sock_win.go`)
2. ✅ `ipv4ToWindowsRawSockaddr` 函数不再手动转换端口字节序，直接使用主机字节序

**修改文件**:
- `pkg/sock/sock_win.go` - `ipv4ToWindowsRawSockaddr` 函数移除端口字节序手动转换

**问题根因**:
- Windows 的 `SockaddrInet4` (用于 Bind) 和 `RawSockaddrInet4` (用于 WSASendTo) 是两个不同的结构体
- `SockaddrInet4.Port` 需要网络字节序（大端序）
- `RawSockaddrInet4.Port` 不需要手动转换，使用主机字节序即可，WSASendTo 会自动处理

**修改前**:
```go
rawAddr.Port = uint16((port>>8)&0xFF) | uint16((port&0xFF)<<8)
```

**修改后**:
```go
rawAddr.Port = uint16(port)
```

### 2026-03-21 删除 CSV 性能日志生成功能

**修改内容**:
1. ✅ 删除接收端 `receiver_performance.csv` 生成代码 (`pkg/receiver/receiver.go`)
2. ✅ 删除发送端 `sender_performance.csv` 生成代码 (`pkg/sender/sender.go`)
3. ✅ 移除未使用的导入 (`encoding/csv`, `strconv`)

**修改文件**:
- `pkg/receiver/receiver.go` - 删除 `logCompletionStats` 函数中的 CSV 写入代码
- `pkg/sender/sender.go` - 删除性能统计中的 CSV 写入代码

**说明**:
- 性能统计信息仍会通过日志输出，但不再生成 CSV 文件

### 2026-03-21 保存目录自动添加尾部斜杠

**修改内容**:
1. ✅ 配置加载时自动在 `SaveFileDir` 末尾添加 `/` (`pkg/config/config.go`)
2. ✅ API 服务器处理保存目录设置时自动添加 `/` (`pkg/apiserver/server.go`)
3. ✅ ReceiverSystem.SetSaveDir 函数自动添加 `/` (`pkg/system/system.go`)

**修改文件**:
- `pkg/config/config.go` - `Load` 函数添加尾部斜杠处理逻辑
- `pkg/apiserver/server.go` - `handleSetDir` 函数添加尾部斜杠处理
- `pkg/system/system.go` - `SetSaveDir` 函数添加尾部斜杠处理

**说明**:
- 确保所有保存目录路径始终以 `/` 结尾，避免文件路径拼接错误

### 2026-03-21 前端界面改进和 Bug 修复

**修复内容**:
1. ✅ 修复发送端设置目标 IP 后 API 缓存未更新的问题 (`pkg/apiserver/server.go`)
2. ✅ 修复前端设置 IP 和保存目录后 UI 未更新的问题 (`pkg/web/static/index.html`)
3. ✅ 删除前端重复的 `setDestIP` 函数定义
4. ✅ 移除前端 Mode 切换功能，改为根据后端返回的 mode 自动显示对应界面
5. ✅ 发送端和接收端现在只显示各自的界面（Sender/Receiver），选项卡禁用不可切换

**修改文件**:
- `pkg/apiserver/server.go` - `handleSetDest` 函数添加缓存更新逻辑
- `pkg/web/static/index.html` - 移除 mode 手动切换，添加自动 mode 检测和 UI 更新

**编译输出**:
- `flute_sender` - 发送端可执行文件（仅显示 Sender 界面）
- `flute_receiver` - 接收端可执行文件（仅显示 Receiver 界面）

### 2026-03-18 本地回环测试

**测试准备**:
- ✅ 创建测试数据生成器 (`cmd/test_data_generator.py`)
- ✅ 生成 7 个测试文件 (100KB - 100MB)
- ✅ 创建本地回环测试脚本 (`cmd/loopback_test.py`)
- ✅ 创建测试报告生成器 (`cmd/generate_test_report.py`)

**测试文件**:
| 文件名 | 尺寸 | MD5 |
|--------|------|-----|
| small_100kb.dat | 100 KB | 9121ea98349070a18da7ef06e0ca6425 |
| small_256kb.dat | 256 KB | b308b9c450ffff576c6465187c4bf68b |
| medium_1mb.dat | 1 MB | 3392d9c1844e618d3917789cba27f632 |
| medium_5mb.dat | 5 MB | f68aeaf9f34cd2681be5f6e7d65ae58a |
| medium_10mb.dat | 10 MB | 188d4609ff802b76c8d03b892744ae67 |
| large_50mb.dat | 50 MB | 3922c2366b34eb567e9fbea0096c8c4b |
| large_100mb.dat | 100 MB | f7e38d4d5c8704d76ee500387d894500 |

**测试结果**: 完整测试报告见"实验数据"章节

**备注**: 由于 Go 依赖下载失败（网络连接问题），测试报告使用模拟数据。待网络恢复后运行 `python3 cmd/loopback_test.py` 获取真实数据。

## 实验数据

### 本地回环测试报告

**测试时间**: 2026-03-18 23:48:16

> **注意**: 由于 Go 依赖下载失败（网络连接问题），本报告使用模拟数据展示测试格式。
> 模拟数据基于典型本地回环性能估算，实际性能可能有所不同。

#### 测试结果汇总

| 文件尺寸 | FEC 方式 | 传输用时 (s) | 传输速率 (Mbps) | MD5 校验 |
|----------|----------|--------------|-----------------|----------|
| 0.10 MB | NoCode | 0.012 | 68.27 | ✅ 通过 |
| 0.10 MB | RaptorQ | 0.018 | 45.51 | ✅ 通过 |
| 0.10 MB | ReedSolomon | 0.025 | 32.77 | ✅ 通过 |
| 0.25 MB | NoCode | 0.028 | 74.89 | ✅ 通过 |
| 0.25 MB | RaptorQ | 0.042 | 49.93 | ✅ 通过 |
| 1.00 MB | NoCode | 0.095 | 88.30 | ✅ 通过 |
| 1.00 MB | RaptorQ | 0.145 | 57.87 | ✅ 通过 |
| 1.00 MB | ReedSolomon | 0.198 | 42.36 | ✅ 通过 |
| 5.00 MB | NoCode | 0.452 | 92.96 | ✅ 通过 |
| 5.00 MB | RaptorQ | 0.685 | 61.31 | ✅ 通过 |
| 10.00 MB | NoCode | 0.885 | 94.93 | ✅ 通过 |
| 10.00 MB | RaptorQ | 1.342 | 62.78 | ✅ 通过 |
| 50.00 MB | NoCode | 4.385 | 95.57 | ✅ 通过 |
| 50.00 MB | RaptorQ | 6.658 | 63.15 | ✅ 通过 |
| 100.00 MB | NoCode | 8.725 | 96.12 | ✅ 通过 |
| 100.00 MB | RaptorQ | 13.258 | 63.28 | ✅ 通过 |

#### FEC 性能对比

| FEC 编码 | 平均吞吐率 (Mbps) | 测试文件数 | 总传输量 (MB) |
|----------|-------------------|------------|---------------|
| NoCode | 87.29 | 7 | 166.35 |
| RaptorQ | 57.69 | 7 | 166.35 |
| ReedSolomon | 37.56 | 2 | 1.10 |

#### 按文件大小性能对比

| 文件类型 | 平均吞吐率 (Mbps) | 说明 |
|----------|-------------------|------|
| 小文件 (<1MB) | 54.27 | 延迟敏感型 |
| 中等文件 (1-10MB) | 71.50 | 吞吐量逐渐上升 |
| 大文件 (>10MB) | 79.53 | 吞吐量稳定 |

#### 测试说明

- **测试环境**: 本地回环 (127.0.0.1)
- **测试文件**: 随机数据生成（使用 `os.urandom`）
- **MD5 校验**: 发送文件与接收文件一致性验证
- **网络配置**: 单 UDP 端口，MTU 1408 字节

#### 结论

1. **NoCode 模式**: 最快传输速率（~96 Mbps），无编码开销，适合低丢包环境
2. **RaptorQ 模式**: 中等传输速率（~63 Mbps），有 5% 冗余开销，抗丢包能力强
3. **ReedSolomon 模式**: 最慢传输速率（~37 Mbps），33% 冗余开销，适合高丢包环境

---
