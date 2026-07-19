@echo off
REM export.cmd - export a workspace event log as canonical JSONL (twin of export.sh).
REM Usage: scripts\export.cmd ^<data-dir^> [out.jsonl]
REM Default out: churn-export-YYYYMMDD-HHMMSS.jsonl. Safe while a server runs.
setlocal
cd /d "%~dp0.."

if "%~1"=="" (
  echo usage: scripts\export.cmd ^<data-dir^> [out.jsonl]
  exit /b 1
)
set "DATA=%~1"
set "OUT=%~2"
if "%OUT%"=="" (
  for /f %%t in ('powershell -NoProfile -Command "Get-Date -Format yyyyMMdd-HHmmss"') do set "TS=%%t"
  call set "OUT=churn-export-%%TS%%.jsonl"
)

if not exist churn.exe (
  echo ==^> no .\churn.exe yet: building
  go build -o churn.exe .\cmd\churn
  if errorlevel 1 exit /b 1
)

echo ==^> exporting %DATA% -^> %OUT%
.\churn.exe export-log --data "%DATA%" --out "%OUT%"
if errorlevel 1 exit /b 1
