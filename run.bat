@echo off
echo King Sniper - Select Mode:
echo ========================
echo 1. Safe Mode (with delays)
echo 2. Speed Mode (maximum speed, higher risk)
echo.
choice /C 12 /M "Enter your choice"

if errorlevel 2 goto speed
if errorlevel 1 goto safe

:safe
echo Starting King Sniper in SAFE MODE...
echo.
go run main.go
goto end

:speed
echo Starting King Sniper in SPEED MODE...
set SPEED_MODE=true
echo.
go run main.go
goto end

:end
pause