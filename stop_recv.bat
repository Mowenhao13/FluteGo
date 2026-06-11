@echo off
chcp 65001 >nul
echo 停止所有 FluteGo 接收进程...
taskkill /F /IM flute_receiver_cli.exe 2>nul
echo 已清理
