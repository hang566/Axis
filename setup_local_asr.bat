@echo off
rem 安装本地语音引擎依赖：64 位 Python + sherpa-onnx。
rem ASCII-only：避免 cmd 代码页问题。
setlocal
chcp 65001 >nul
title 安装本地语音引擎
cd /d "%~dp0"

echo.
echo   ============================================
echo    安装本地语音引擎（Python + sherpa-onnx）
echo   ============================================
echo.

set "PY="
where python >nul 2>nul && set "PY=python"
if not defined PY (
    where py >nul 2>nul && set "PY=py -3"
)
if not defined PY (
    echo [错误] 未找到 Python。
    echo        请安装 64 位 Python 3.10 或更高版本：https://www.python.org/downloads/
    echo        安装时务必勾选 "Add Python to PATH"。
    echo.
    pause
    exit /b 1
)

echo [1/2] 检查 Python 位数...
%PY% -c "import struct,sys; print(sys.version); sys.exit(0 if struct.calcsize('P')==8 else 1)"
if errorlevel 1 (
    echo [错误] 当前 Python 是 32 位，sherpa-onnx 需要 64 位。
    pause
    exit /b 1
)

echo [2/2] 安装 sherpa-onnx（首次约 100MB，请耐心等待）...
%PY% -m pip install --upgrade pip
%PY% -m pip install -U sherpa-onnx

if errorlevel 1 (
    echo.
    echo [错误] 安装失败。国内网络可先换镜像：
    echo        %PY% -m pip install -U sherpa-onnx -i https://pypi.tuna.tsinghua.edu.cn/simple
    echo.
    pause
    exit /b 1
)

echo.
echo   安装完成。回到工作台「模型中心」下载一个语音模型即可开始转写。
echo.
pause
