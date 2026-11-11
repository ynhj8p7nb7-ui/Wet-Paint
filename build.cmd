@echo off
rem ------------------------------------------------------------------
rem  Build Wet Paint from source on Windows.
rem  Needs Go (free):   winget install GoLang.Go
rem  Usage:  build.cmd            (version 1.3.1)
rem          build.cmd 1.3.1      (stamp a different version)
rem  Output: dist\WetPaint.exe  +  its SHA-256 fingerprint
rem ------------------------------------------------------------------
setlocal
cd /d "%~dp0"
set "VERSION=%~1"
if "%VERSION%"=="" set "VERSION=1.3.1"

where go >nul 2>nul
if errorlevel 1 (
  echo.
  echo  Go isn't installed. Install it by running this in a terminal:
  echo      winget install GoLang.Go
  echo  ...or download it from https://go.dev/dl/
  echo  Then close this window, open a new one, and run build.cmd again.
  echo.
  pause
  exit /b 1
)

echo.
echo  [1/4] Checking the code...
go vet ./...
if errorlevel 1 goto :failed

echo  [2/4] Running tests...
go test ./...
if errorlevel 1 goto :failed

echo  [3/4] Embedding icon and version info...
go run ./tools/mkicon -version %VERSION% -out rsrc_windows_amd64.syso assets\icon-16.png assets\icon-24.png assets\icon-32.png assets\icon-48.png assets\icon-64.png assets\icon-128.png assets\icon-256.png
if errorlevel 1 goto :failed

echo  [4/4] Building dist\WetPaint.exe ...
if not exist dist mkdir dist
go build -trimpath -ldflags "-X main.version=%VERSION%" -o dist\WetPaint.exe .
if errorlevel 1 goto :failed

echo.
echo  Built dist\WetPaint.exe  (version %VERSION%)
certutil -hashfile dist\WetPaint.exe SHA256 | findstr /v "hash CertUtil" > dist\WetPaint.exe.sha256.txt
echo  SHA-256:
type dist\WetPaint.exe.sha256.txt
echo.
exit /b 0

:failed
echo.
echo  BUILD FAILED - see the messages above.
echo  Tip: ask Claude Code in this folder to "fix the build" and paste the error.
echo.
pause
exit /b 1
