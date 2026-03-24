# Windows Socket 缓冲区检查脚本
# 以管理员身份运行 PowerShell

Write-Host "=== Windows Socket 缓冲区诊断工具 ===" -ForegroundColor Green
Write-Host ""

# 1. 检查管理员权限
Write-Host "[1/5] 检查管理员权限..." -ForegroundColor Yellow
$currentPrincipal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
$isAdmin = $currentPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

if (-not $isAdmin) {
    Write-Host "⚠️  警告: 未以管理员身份运行，某些检查可能受限" -ForegroundColor Red
    Write-Host "   请右键点击 PowerShell，选择'以管理员身份运行'" -ForegroundColor Yellow
} else {
    Write-Host "✓ 已获得管理员权限" -ForegroundColor Green
}
Write-Host ""

# 2. 检查 TCP/IP 全局设置
Write-Host "[2/5] 检查 TCP/IP 全局设置..." -ForegroundColor Yellow
try {
    netsh int ipv4 show global
    Write-Host ""
} catch {
    Write-Host "✗ 获取 TCP/IP 设置失败: $_" -ForegroundColor Red
}

# 3. 列出网络适配器
Write-Host "[3/5] 列出网络适配器..." -ForegroundColor Yellow
try {
    $adapters = Get-NetAdapter | Where-Object { $_.Status -eq "Up" }
    Write-Host "发现 $($adapters.Count) 个活动网络适配器:" -ForegroundColor Cyan
    $adapters | Format-Table Name, InterfaceDescription, LinkSpeed, Status -AutoSize
    Write-Host ""
} catch {
    Write-Host "✗ 获取网络适配器列表失败: $_" -ForegroundColor Red
}

# 4. 检查网络适配器高级属性
Write-Host "[4/5] 检查网络适配器缓冲区设置..." -ForegroundColor Yellow
if ($adapters) {
    foreach ($adapter in $adapters) {
        Write-Host "`n适配器: $($adapter.Name)" -ForegroundColor Cyan
        try {
            $props = Get-NetAdapterAdvancedProperty -Name $adapter.Name -ErrorAction SilentlyContinue
            if ($props) {
                $bufferProps = $props | Where-Object { $_.DisplayName -like "*Buffer*" -or $_.DisplayName -like "*缓冲区*" }
                if ($bufferProps) {
                    $bufferProps | Format-Table DisplayName, DisplayValue -AutoSize
                } else {
                    Write-Host "  未发现缓冲区相关设置" -ForegroundColor Gray
                }
            }
        } catch {
            Write-Host "  获取高级属性失败: $_" -ForegroundColor Red
        }
    }
}
Write-Host ""

# 5. 检查注册表设置
Write-Host "[5/5] 检查注册表 TCP/IP 设置..." -ForegroundColor Yellow
$regPath = "HKLM:\SYSTEM\CurrentControlSet\Services\Tcpip\Parameters"
if (Test-Path $regPath) {
    Write-Host "检查注册表路径: $regPath" -ForegroundColor Cyan
    try {
        $params = Get-ItemProperty $regPath -ErrorAction SilentlyContinue
        $interestingParams = @("DefaultSendWindow", "DefaultReceiveWindow", "TcpWindowSize", "GlobalMaxTcpWindowSize")
        $found = $false
        foreach ($param in $interestingParams) {
            if ($params.$param -ne $null) {
                Write-Host "  $param = $($params.$param)" -ForegroundColor Green
                $found = $true
            }
        }
        if (-not $found) {
            Write-Host "  未发现自定义缓冲区设置（使用系统默认值）" -ForegroundColor Gray
        }
    } catch {
        Write-Host "  读取注册表失败: $_" -ForegroundColor Red
    }
} else {
    Write-Host "  注册表路径不存在: $regPath" -ForegroundColor Red
}
Write-Host ""

# 总结和建议
Write-Host "=== 诊断完成 ===" -ForegroundColor Green
Write-Host ""
Write-Host "建议操作:" -ForegroundColor Yellow
Write-Host "1. 如果使用 FluteGo 出现缓冲区满错误:" -ForegroundColor White
Write-Host "   - 降低发送速率 (在 constant.go 中修改 DefaultSendRateLimitMbps)" -ForegroundColor Gray
Write-Host "   - 减少 TX/RX 缓冲区大小 (在 constant.go 中修改 TX_BUF/RX_BUF)" -ForegroundColor Gray
Write-Host ""
Write-Host "2. 如需永久增加 Windows 缓冲区大小 (需要管理员权限):" -ForegroundColor White
Write-Host "   - 打开注册表编辑器 (regedit)" -ForegroundColor Gray
Write-Host "   - 导航到: HKLM\SYSTEM\CurrentControlSet\Services\Tcpip\Parameters" -ForegroundColor Gray
Write-Host "   - 创建 DWORD 值: DefaultSendWindow, DefaultReceiveWindow" -ForegroundColor Gray
Write-Host "   - 设置为十进制值 (例如: 16777216 = 16MB)" -ForegroundColor Gray
Write-Host "   - 重启计算机使设置生效" -ForegroundColor Gray
Write-Host ""
Write-Host "3. 更新网络适配器驱动程序" -ForegroundColor White
Write-Host "   - 访问网卡厂商官网下载最新驱动" -ForegroundColor Gray
Write-Host ""
