@echo off
REM backup.cmd - online snapshot of a workspace database (twin of backup.sh).
REM Usage: scripts\backup.cmd ^<data-dir^> [dest.db]
REM Default dest: churn-backup-YYYYMMDD-HHMMSS.db. Safe while a server runs.
setlocal
cd /d "%~dp0.."

if "%~1"=="" (
  echo usage: scripts\backup.cmd ^<data-dir^> [dest.db]
  exit /b 1
)
set "DATA=%~1"
set "DEST=%~2"
if "%DEST%"=="" (
  for /f %%t in ('powershell -NoProfile -Command "Get-Date -Format yyyyMMdd-HHmmss"') do set "TS=%%t"
  call set "DEST=churn-backup-%%TS%%.db"
)

if not exist churn.exe (
  echo ==^> no .\churn.exe yet: building
  go build -o churn.exe .\cmd\churn
  if errorlevel 1 exit /b 1
)

echo ==^> backing up %DATA% -^> %DEST%
.\churn.exe backup --data "%DATA%" "%DEST%"
if errorlevel 1 exit /b 1
