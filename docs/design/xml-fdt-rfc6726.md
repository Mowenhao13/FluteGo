# XML FDT 元数据传输设计方案

## 概述

将当前二进制元数据传输改为符合 RFC 6726 标准的 FDT (File Delivery Table) XML 格式,
同时统一元数据和文件数据到同一个端口,通过 TOI (Transport Object Identifier) 区分。

## 设计目标

1. **符合 RFC 6726 标准** - FDT 使用 XML 格式,支持增量更新
2. **统一端口** - 元数据和文件数据在同一端口传输,通过 TOI 区分
3. **扩展性** - 支持 RFC 6726 完整属性集
4. **向后兼容** - 数据传输保持二进制格式

## 架构变更

### 当前架构
```
Meta Port (3399): 二进制 MetaPkt → 接收端解析后启动 Receiver
File Port (3400+): 8字节头(chunkIdx+symbolID) + 二进制数据
```

### 目标架构
```
统一端口 (3400):
  TOI=0 → FDT XML (文件描述 + FEC OTI + 会话参数)
  TOI>0 → 文件数据 (二进制, 12字节头: TOI + chunkIdx + symbolID)
```

## 数据包格式

### LCT 头部 (RFC 5651)
```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  V=1| C |PSI|S| O |H|Res|A|B|   HDR_LEN     | Codepoint (CP)|
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                         CCI (32 bits)                         |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                         TSI (32 bits)                         |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                         TOI (32 bits)                         |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                    Chunk Index / SBN (32 bits)                |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                    Symbol ID / ESI (32 bits)                  |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                        Payload Data ...                       |
```

- **V**: Version (4 bits) = 1
- **C**: Congestion Control (2 bits) = 0 (不使用)
- **PSI**: Packet Sequence Identifier (2 bits) = 0
- **S**: Session Identifier (1 bit) = 1 (TSI 存在)
- **O**: Object Identifier (2 bits) = 2 (TOI 32 bits)
- **H**: Half-word (1 bit) = 0
- **A**: Close Session (1 bit) = 0/1
- **B**: Close Object (1 bit) = 0/1
- **HDR_LEN**: Header length in 32-bit words
- **CP**: Codepoint (8 bits) = FEC Encoding ID
- **CCI**: Congestion Control Information (32 bits)
- **TSI**: Transport Session Identifier (32 bits)
- **TOI**: Transport Object Identifier (32 bits)
  - TOI=0: FDT XML
  - TOI>0: 文件数据
- **Chunk Index**: 块索引 (32 bits)
- **Symbol ID**: 符号索引 (32 bits)

头部总长度: 24 字节

## FDT XML 格式

```xml
<?xml version="1.0" encoding="UTF-8"?>
<FDT-Instance
    xmlns="urn:IETF:metadata:2005:FLUTE:FDT"
    Expires="4294967295"
    Complete="true"
    FEC-OTI-FEC-Encoding-ID="1"
    FEC-OTI-FEC-Instance-ID="1"
    FEC-OTI-Encoding-Symbol-Length="1400"
    FEC-OTI-Maximum-Source-Block-Length="32768">

  <File
      Content-Location="/data/file1.dat"
      TOI="1"
      Transfer-Length="1073741824"
      Content-Length="1073741824"
      Content-Type="application/octet-stream"
      Content-Encoding="identity"
      Content-MD5="d41d8cd98f00b204e9800998ecf8427e"
      File-ETag="v1" />

  <File
      Content-Location="/data/file2.dat"
      TOI="2"
      Transfer-Length="524288000"
      Content-Type="application/octet-stream"
      Content-MD5="098f6bcd4621d373cade4e832627b4f6" />

  <!-- FluteGo 扩展: 会话级参数 -->
  <flute:session
      xmlns:flute="urn:flute:ext"
      base-port="3400"
      num-ports="1"
      max-packet-size="1408"
      redundancy-ratio="1.25"
      rate-limit-mbps="500" />
</FDT-Instance>
```

### FDT 属性说明

#### FDT-Instance 级别 (RFC 6726 Section 3.4.2.1)
| 属性 | 必需 | 说明 |
|------|------|------|
| `Expires` | REQUIRED | FDT 过期时间 (Unix 时间戳) |
| `Complete` | OPTIONAL | FDT 是否完整 |
| `FEC-OTI-*` | OPTIONAL | 会话级 FEC 参数 |

#### File 元素 (RFC 6726 Section 3.4.2.2)
| 属性 | 必需 | 说明 |
|------|------|------|
| `Content-Location` | REQUIRED | 文件路径 |
| `TOI` | REQUIRED | 文件标识符 |
| `Transfer-Length` | REQUIRED | 传输对象长度 |
| `Content-Length` | OPTIONAL | 原始内容长度 |
| `Content-Type` | OPTIONAL | MIME 类型 |
| `Content-Encoding` | OPTIONAL | 内容编码 |
| `Content-MD5` | OPTIONAL | MD5 校验和 |
| `File-ETag` | OPTIONAL | 文件实体标签 |

#### FluteGo 扩展
| 属性 | 说明 |
|------|------|
| `base-port` | 基础端口 |
| `num-ports` | 端口数量 |
| `max-packet-size` | 最大包大小 |
| `redundancy-ratio` | 冗余比率 |
| `rate-limit-mbps` | 限速 (Mbps) |

## 增量更新机制

### 发送端
1. 每次 `publish()` 生成新 FDT Instance,`fdt-id` 递增 (20-bit, 0~0xFFFFF)
2. FDT 发布模式:
   - `FullFDT` - 包含所有文件
   - `ObjectsBeingTransferred` - 仅包含正在传输的文件
3. FDT 有过期时间 (`Expires`),过期前需发布新版本
4. 通过 TOI=0 发送 FDT XML

### 接收端
1. 收到 FDT (TOI=0)
2. 解析 XML → FdtInstance
3. 检查 fdt-id:
   - 新 fdt-id > 当前 fdt-id → 更新 FDT
   - fdt-id <= 当前 fdt-id → 忽略 (重复)
4. 遍历 File 列表:
   - 新 TOI → 创建 Receiver, 开始接收
   - 已知 TOI → 忽略 (已在接收)
5. 检查 Expires, 设置过期定时器

### 增量更新示例
```
T1: publish FDT (fdt-id=1)
    → File TOI=1 (file1.dat)
    → Complete=true
    → 开始传输 file1.dat (TOI=1)

T2: 新增 file2.dat
    → publish FDT (fdt-id=2)
    → File TOI=1 (file1.dat) + File TOI=2 (file2.dat)
    → Complete=true
    → 开始传输 file2.dat (TOI=2)

T3: file1.dat 传输完成
    → publish FDT (fdt-id=3)
    → File TOI=2 (file2.dat)  [file1 已移除]
    → Complete=true

T4: 所有文件传输完成
    → Close Session 标志 (A=1)
```

## 修改文件清单

| 文件 | 修改内容 |
|------|---------|
| `pkg/meta/meta.go` | 移除二进制序列化, 新增 FDT XML 序列化/反序列化 |
| `pkg/meta/fdtinstance.go` | **新增** FdtInstance / File 结构体定义 |
| `pkg/meta/lct.go` | **新增** LCT 头部编解码 |
| `pkg/filedesc/filedesc.go` | 扩展字段 (TOI, Content-Encoding, File-ETag 等) |
| `pkg/oti/oti.go` | 新增 `get_attributes()` 方法, 映射到 XML 属性 |
| `pkg/sender/sender.go` | 适配 LCT 头部, TOI 路由, FDT 发布 |
| `pkg/sender/fdt.go` | **新增** FDT 管理 (发布、增量更新、过期) |
| `pkg/receiver/receiver.go` | 适配 LCT 头部, TOI 路由, FDT 解析 |
| `pkg/receiver/fdtreceiver.go` | **新增** FDT 接收、版本管理 |
| `cmd/flute_sender/main.go` | 移除 meta port, 统一端口 |
| `cmd/flute_receiver/main.go` | 移除 meta port, 统一端口 |

## 兼容性

- 二进制和 XML 格式不兼容,需要同时更新发送端和接收端
- 数据传输格式保持二进制,仅元数据改为 XML
- 统一端口后,移除 meta port 概念

## 参考

- RFC 6726: File Delivery over Unidirectional Transport (FLUTE)
- RFC 5651: LCT (Layered Coding Transport) Header Format
- RFC 5052: Forward Error Correction (FEC) Building Block
