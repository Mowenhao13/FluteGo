#!/bin/bash

# Mac/Linux Socket 缓冲区检查脚本

echo "=== Mac/Linux Socket 缓冲区诊断工具 ==="
echo ""

# 1. 检查操作系统
echo "[1/5] 检查操作系统..."
if [[ "$OSTYPE" == "darwin"* ]]; then
    echo "✓ 检测到 macOS"
    IS_MAC=1
elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
    echo "✓ 检测到 Linux"
    IS_LINUX=1
else
    echo "⚠️  未知操作系统: $OSTYPE"
fi
echo ""

# 2. 检查 Socket 缓冲区设置
echo "[2/5] 检查 Socket 缓冲区设置..."
if [ -n "$IS_MAC" ]; then
    echo "macOS Socket 缓冲区设置:"
    echo "----------------------------"
    sysctl net.inet.udp.recvspace 2>/dev/null || echo "net.inet.udp.recvspace: (未设置)"
    sysctl net.inet.udp.sendspace 2>/dev/null || echo "net.inet.udp.sendspace: (未设置)"
    sysctl net.inet.tcp.recvspace 2>/dev/null || echo "net.inet.tcp.recvspace: (未设置)"
    sysctl net.inet.tcp.sendspace 2>/dev/null || echo "net.inet.tcp.sendspace: (未设置)"
    sysctl kern.ipc.maxsockbuf 2>/dev/null || echo "kern.ipc.maxsockbuf: (未设置)"
    sysctl net.inet.tcp.sendbuf_max 2>/dev/null || echo "net.inet.tcp.sendbuf_max: (未设置)"
    sysctl net.inet.tcp.recvbuf_max 2>/dev/null || echo "net.inet.tcp.recvbuf_max: (未设置)"
elif [ -n "$IS_LINUX" ]; then
    echo "Linux Socket 缓冲区设置:"
    echo "---------------------------"
    sysctl net.core.rmem_default 2>/dev/null || echo "net.core.rmem_default: (未设置)"
    sysctl net.core.wmem_default 2>/dev/null || echo "net.core.wmem_default: (未设置)"
    sysctl net.core.rmem_max 2>/dev/null || echo "net.core.rmem_max: (未设置)"
    sysctl net.core.wmem_max 2>/dev/null || echo "net.core.wmem_max: (未设置)"
    sysctl net.ipv4.udp_mem 2>/dev/null || echo "net.ipv4.udp_mem: (未设置)"
    sysctl net.ipv4.tcp_mem 2>/dev/null || echo "net.ipv4.tcp_mem: (未设置)"
fi
echo ""

# 3. 检查网络接口
echo "[3/5] 检查网络接口..."
if command -v ifconfig &> /dev/null; then
    echo "网络接口列表:"
    echo "-------------"
    ifconfig -a 2>/dev/null | grep -E "^[a-zA-Z0-9]+:" | head -10
elif command -v ip &> /dev/null; then
    echo "网络接口列表:"
    echo "-------------"
    ip link show 2>/dev/null | grep -E "^[0-9]+" | head -10
else
    echo "无法获取网络接口列表"
fi
echo ""

# 4. 检查网络统计
echo "[4/5] 检查网络统计..."
if command -v netstat &> /dev/null; then
    echo "网络接口统计:"
    echo "-------------"
    netstat -i 2>/dev/null | head -15
fi
echo ""

# 5. 检查内存信息
echo "[5/5] 检查系统内存信息..."
if [ -n "$IS_MAC" ]; then
    echo "macOS 内存信息:"
    echo "----------------"
    vm_stat 2>/dev/null | head -10
elif [ -n "$IS_LINUX" ]; then
    echo "Linux 内存信息:"
    echo "---------------"
    free -h 2>/dev/null
fi
echo ""

# 总结和建议
echo "=== 诊断完成 ==="
echo ""
echo "建议操作:"
echo "---------"
echo "1. 如需临时增加缓冲区大小 (重启后失效):"
if [ -n "$IS_MAC" ]; then
    echo "   sudo sysctl -w net.inet.udp.sendspace=4194304"
    echo "   sudo sysctl -w net.inet.udp.recvspace=4194304"
    echo "   sudo sysctl -w kern.ipc.maxsockbuf=8388608"
elif [ -n "$IS_LINUX" ]; then
    echo "   sudo sysctl -w net.core.wmem_default=4194304"
    echo "   sudo sysctl -w net.core.rmem_default=4194304"
    echo "   sudo sysctl -w net.core.wmem_max=16777216"
    echo "   sudo sysctl -w net.core.rmem_max=16777216"
fi
echo ""
echo "2. 如需永久修改 (需要重启):"
if [ -n "$IS_MAC" ]; then
    echo "   编辑 /etc/sysctl.conf 或 /Library/Preferences/SystemConfiguration/com.apple.sysctl.plist"
    echo "   添加或修改:"
    echo "     net.inet.udp.sendspace=4194304"
    echo "     net.inet.udp.recvspace=4194304"
    echo "     kern.ipc.maxsockbuf=8388608"
elif [ -n "$IS_LINUX" ]; then
    echo "   编辑 /etc/sysctl.conf"
    echo "   添加或修改:"
    echo "     net.core.wmem_default=4194304"
    echo "     net.core.rmem_default=4194304"
    echo "     net.core.wmem_max=16777216"
    echo "     net.core.rmem_max=16777216"
    echo "   然后执行: sudo sysctl -p"
fi
echo ""
echo "3. 如果使用 FluteGo 仍有问题:"
echo "   - 降低发送速率 (修改 constant.go 中的 DefaultSendRateLimitMbps)"
echo "   - 减少 TX/RX 缓冲区大小 (修改 constant.go 中的 TX_BUF/RX_BUF)"
echo ""
