@echo off
chcp 65001 >nul
setlocal enabledelayedexpansion

set SAVE_DIR=%~dp0received_files
set RECEIVER=%~dp0dist\flute_receiver_cli.exe
set LOG_DIR=%~dp0logs

if not exist "%SAVE_DIR%" mkdir "%SAVE_DIR%"
if not exist "%LOG_DIR%" mkdir "%LOG_DIR%"

echo ═══════════════════════════════════════════
echo  FluteGo 接收循环
echo  保存目录: %SAVE_DIR%
echo  日志目录: %LOG_DIR%
echo  按 Ctrl+C 停止
echo ═══════════════════════════════════════════

:loop
set TIMESTAMP=%date:~0,4%%date:~5,2%%date:~8,2%_%time:~0,2%%time:~3,2%%time:~6,2%
set TIMESTAMP=%TIMESTAMP: =0%
set LOGFILE=%LOG_DIR%\recv_%TIMESTAMP%.log

echo [%date% %time%] 启动接收端 (日志: %LOGFILE%)
echo ═══════════════════════════════════════════ >> "%LOGFILE%"

"%RECEIVER%" --cli --save-dir "%SAVE_DIR%" --timeout 0 >> "%LOGFILE%" 2>&1

set EXITCODE=%ERRORLEVEL%
echo [%date% %time%] 接收端退出，退出码: %EXITCODE% >> "%LOGFILE%"

if %EXITCODE% NEQ 0 (
    echo [%date% %time%] ⚠ 接收端异常退出 (代码: %EXITCODE%)，5秒后重启...
    timeout /t 5 /nobreak >nul
) else (
    echo [%date% %time%] 接收端正常退出，2秒后重新监听...
    timeout /t 2 /nobreak >nul
)

goto loop
