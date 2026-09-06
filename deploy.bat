@echo off
setlocal EnableExtensions
cd /d "%~dp0"

REM ============================================================
REM  deploy.bat - pack jmdb-backup for the real server
REM
REM  Usage:   deploy.bat [target-folder]
REM
REM  Copies ONLY jmdb-backup.exe and config.yaml into a clean
REM  folder you can ship (zip / USB / copy). Then prints a
REM  checklist to run through on the destination machine.
REM
REM  Default target folder: .\deploy
REM  NOTE: if the target folder exists inside this project it is
REM  DELETED and recreated, so keep anything else out of it.
REM ============================================================

set "TARGET=%~1"
if "%TARGET%"=="" set "TARGET=deploy"

echo === jmdb-backup deploy pack ===

REM ---- 1. source files must exist ----
if not exist "jmdb-backup.exe" (
    echo [FAIL] jmdb-backup.exe not found. Run build.bat first.
    exit /b 1
)
if not exist "config.yaml" (
    echo [FAIL] config.yaml not found.
    echo        First create it from the template and fill in real values:
    echo          copy config.example.yaml config.yaml
    exit /b 1
)

REM ---- 2. resolve absolute paths ----
for %%I in ("%~dp0.")   do set "PROJROOT=%%~fI"
for %%I in ("%TARGET%")  do set "TGTABS=%%~fI"

REM ---- 3. prepare a CLEAN target folder ----
echo %TGTABS% | findstr /I /B /C:"%PROJROOT%" >nul
if errorlevel 1 goto :outside_project

if /I "%TGTABS%"=="%PROJROOT%" (
    echo [FAIL] Refusing to use the project root as the deploy folder.
    exit /b 1
)
if exist "%TGTABS%" rmdir /s /q "%TGTABS%"
mkdir "%TGTABS%"
goto :copy_files

:outside_project
echo [INFO] Target folder is outside this project - creating it (not wiping).
if not exist "%TGTABS%" mkdir "%TGTABS%"
goto :copy_files

:copy_files
copy /y "jmdb-backup.exe" "%TGTABS%\" >nul
if errorlevel 1 (
    echo [FAIL] Could not copy jmdb-backup.exe to "%TGTABS%"
    exit /b 1
)
copy /y "config.yaml" "%TGTABS%\" >nul
if errorlevel 1 (
    echo [FAIL] Could not copy config.yaml to "%TGTABS%"
    exit /b 1
)

echo.
echo Deploy folder ready: %TGTABS%
echo Contains exactly: jmdb-backup.exe  config.yaml
echo.

echo === Checklist - run through before trusting it on the real machine ===
echo   [ ] config.yaml:  host/port/user/password correct, databases include jmdatabase
echo   [ ] config.yaml:  cloudflareR2.endpoint has ACCOUNT_ID replaced
echo   [ ] config.yaml:  accessKeyId / secretAccessKey / bucket are set
echo   [ ] config.yaml:  schedule.times shows the times you want
echo   [ ] destination machine has MariaDB running and mysqldump available
echo       (or set database.mysqldumpPath in config.yaml)
echo   [ ] on the server run:   jmdb-backup.exe -validate -config config.yaml
echo   [ ] test one backup:      jmdb-backup.exe -once  -config config.yaml
echo   [ ] confirm the .sql.gz file appears in your R2 bucket
echo   [ ] start it for real:
echo       - resident console (double-click jmdb-backup.exe), or
echo       - Windows Task Scheduler for -once at fixed time (see README)
echo.
echo   [ ] SECURITY: config.yaml contains credentials. Protect the folder
echo       permissions and never commit config.yaml or the deploy folder.
echo.
echo Pack done.
exit /b 0
