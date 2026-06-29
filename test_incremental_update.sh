#!/bin/bash

# 增量更新测试脚本
# 测试场景：
# 1. 限制传输速率到很小
# 2. 写入一大段文字到txt文件
# 3. 开始本地回环传输
# 4. 修改txt文件内容
# 5. 触发增量更新

set -e

echo "=== 增量更新测试 ==="

# 清理旧文件
rm -rf /tmp/test_incremental /tmp/receiver_incremental
mkdir -p /tmp/test_incremental /tmp/receiver_incremental

# 创建初始文件
echo "创建初始测试文件..."
cat > /tmp/test_incremental/test.txt << 'EOF'
这是初始版本的测试文件。
包含多行文本内容。
用于测试FDT增量更新机制。
EOF

echo "初始文件大小: $(wc -c < /tmp/test_incremental/test.txt) 字节"

# 启动接收端（后台运行）
echo "启动接收端..."
go run cmd/flute_receiver/main.go -cli -save-dir /tmp/receiver_incremental > /tmp/receiver_incremental.log 2>&1 &
RECEIVER_PID=$!
echo "接收端 PID: $RECEIVER_PID"

# 等待接收端启动
sleep 2

# 启动发送端（低速率，限制到 1 Mbps）
echo "启动发送端（速率限制: 1 Mbps）..."
go run cmd/flute_sender/main.go -cli \
    -dest-ip 127.0.0.1 \
    -file /tmp/test_incremental/test.txt \
    -fec RaptorQ \
    -fdt-id 1 \
    -rate-limit-mbps 1 \
    -start-send-wait 1 > /tmp/sender_incremental.log 2>&1 &
SENDER_PID=$!
echo "发送端 PID: $SENDER_PID"

# 等待传输开始
sleep 3

# 修改文件内容
echo "修改文件内容..."
cat >> /tmp/test_incremental/test.txt << 'EOF'

这是新增的内容。
用于测试增量更新机制。
文件已经被修改。
EOF

echo "修改后文件大小: $(wc -c < /tmp/test_incremental/test.txt) 字节"

# 等待一段时间让传输完成
sleep 5

# 停止进程
echo "停止发送端和接收端..."
kill $SENDER_PID 2>/dev/null || true
kill $RECEIVER_PID 2>/dev/null || true

# 等待进程退出
sleep 2

# 显示日志
echo ""
echo "=== 发送端日志 ==="
tail -50 /tmp/sender_incremental.log

echo ""
echo "=== 接收端日志 ==="
tail -50 /tmp/receiver_incremental.log

echo ""
echo "=== 接收到的文件 ==="
if [ -f /tmp/receiver_incremental/test.txt ]; then
    echo "文件已接收"
    echo "文件大小: $(wc -c < /tmp/receiver_incremental/test.txt) 字节"
    echo "文件内容:"
    cat /tmp/receiver_incremental/test.txt
else
    echo "文件未接收"
fi

echo ""
echo "=== 测试完成 ==="
