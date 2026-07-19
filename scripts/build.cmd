@echo off
REM build.cmd - release build of the churn binary (Windows-native twin of build.sh).
REM Usage: scripts\build.cmd
REM Produces .\churn.exe (trimpath, symbols stripped); web/dist is embedded.
setlocal
cd /d "%~dp0.."

echo ==^> building .\churn.exe (release: -trimpath -ldflags "-s -w")
go build -trimpath -ldflags "-s -w" -o churn.exe .\cmd\churn
if errorlevel 1 exit /b 1
echo ==^> done: .\churn.exe
