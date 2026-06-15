## 添加静态 ARP 说明

### MacOS

#### 设置静态 ARP
> Tips: MacOS会定期刷新 ARP 表

```zsh
# 发送端A：找出连接TX光纤收发器的接口
ip addr show
# 查看哪个接口有 Sender IP 

# 假设A的IP在en5上
sudo arp -s <Receiver IP> <Receiver MAC> -i en5

# 验证
netstat -rn -f inet | grep en5
# 示例输出
192.168.0.10  10:7c:61:10:a5:47  UHLS     en5
<ReceiverIP>  <Receiver MAC>              <Sender Interface>
```

#### 发送数据包验证

1. 发送端发送udp数据包
```zsh
go run scripts/send_udp_loop.go -ip <Receiver IP> -port 3400
```

2. 接收端监听
```zsh
nc -ul 3400
```

### Windows

#### 设置静态 ARP
