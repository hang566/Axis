@echo off
rem 兼容旧入口：run.bat 现在等同于 start.bat（启动工作台并打开浏览器）。
rem 只保留 ASCII，避免 cmd 与 chcp 65001 混排时截断。
cd /d "%~dp0"
call "%~dp0start.bat"
