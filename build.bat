@echo off
setlocal
cd /d "%~dp0"

echo === kdb-backup build ===

echo Downloading Go dependencies...
go mod download
if errorlevel 1 goto :fail

echo Running go vet...
go vet .
if errorlevel 1 goto :fail

echo Building kdb-backup.exe (stripped, static)...
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H=windowsgui=0" -o kdb-backup.exe .
if errorlevel 1 goto :fail

echo.
echo Done. Output: %~dp0kdb-backup.exe
echo.
echo To make reverse engineering even harder, optionally install garble:
echo   go install mvdan.cc/garble@latest
echo   garble -literals -tiny -ldflags "-s -w" build -o kdb-backup.exe .
exit /b 0

:fail
echo.
echo BUILD FAILED.
exit /b 1