@echo off
setlocal EnableDelayedExpansion
set "now_dir=%~dp0"

::cd "%now_dir%webssh"
cd "%now_dir%gossh"

set CGO_ENABLED=0
set GOOS=linux
set GOARCH=arm64

go env -w GOPROXY=https://goproxy.cn,direct
go mod tidy

set build_time=%date:~0,4%%date:~5,2%%date:~8,2%_%time:~0,2%%time:~3,2%%time:~6,2%
set build_time=%build_time: =0%

go build -trimpath -ldflags="-s -w" -o webssh_%build_time%

::upx --best --lzma webssh
%now_dir%upx.exe --best --lzma webssh_%build_time%

pause