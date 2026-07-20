@echo off
REM dev.cmd - build and run a local dev server (Windows-native twin of dev.sh).
REM Usage: scripts\dev.cmd [extra serve flags...]
REM Builds .\churn.exe, seeds .\workspace with demo data IF the directory does
REM not exist yet, then serves on 127.0.0.1:8080. Extra args go to `serve`.
setlocal
cd /d "%~dp0.."

echo ==^> building .\churn.exe
go build -o churn.exe .\cmd\churn
if errorlevel 1 exit /b 1

if not exist workspace (
  echo ==^> no .\workspace yet: seeding the demo workspace
  .\churn.exe seed-demo --data workspace
  if errorlevel 1 exit /b 1
) else (
  echo ==^> reusing existing .\workspace
)

REM --no-open: the dev workflow manages its own browser tab; drop --no-open to
REM have serve launch one. Pinned to :8080 for a stable dev URL.
echo ==^> serving http://127.0.0.1:8080 (Ctrl-C to stop)
.\churn.exe serve --data workspace --listen 127.0.0.1:8080 --no-open %*
exit /b %errorlevel%
