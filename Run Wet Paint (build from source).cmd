@echo off
rem  Builds Wet Paint from this folder's source code, then starts it.
rem  An exe you build yourself isn't marked as "downloaded from the internet",
rem  so Windows SmartScreen won't stop it the way it stops a downloaded exe.
setlocal
cd /d "%~dp0"
call build.cmd
if errorlevel 1 exit /b 1
start "Wet Paint" "%~dp0dist\WetPaint.exe"
