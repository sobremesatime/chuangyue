﻿# 由 api/proto 下的契约生成 Go 代码（消息结构 + gRPC 桩）。
#
# Windows 侧入口，与 scripts/gen_proto.sh 等价；Linux / macOS 仍走 `make proto`。
# 本机若禁用了脚本执行，请用：powershell -ExecutionPolicy Bypass -File scripts/gen_proto.ps1
#
# 依赖（缺哪补哪）：
#   protoc                    https://github.com/protocolbuffers/protobuf/releases
#                             没有原生 protoc 时，可用 Python 的 grpcio-tools 顶替
#   protoc-gen-go             go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   protoc-gen-go-grpc        go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
#
# 官方源在国内可能不可达，可先设置：
#   go env -w GOPROXY=https://goproxy.cn,direct
[CmdletBinding()]
param()

# 这里用 Continue 而不是 Stop：部分机器的 Python 启动时会往 stderr 打环境噪音
# （.pth 处理失败的提示），而 PowerShell 会把原生命令的 stderr 记成错误，
# 配合 Stop 会误杀脚本。因此改为逐条检查 $LASTEXITCODE，把失败判断握在自己手里。
$ErrorActionPreference = 'Continue'

$Module   = 'github.com/sobremesatime/chuangyue'
$ProtoDir = 'api/proto'
$GenDir   = 'api/gen'

if (-not (Test-Path $ProtoDir)) {
    throw "未找到 $ProtoDir，请在仓库根目录执行本脚本。"
}
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "未找到 go 命令，请先安装 Go 并加入 PATH。"
}

# 选 protoc：优先 PATH 上的原生二进制，其次退回 grpcio-tools 自带的实现。
# 后者的命令行参数与 protoc 兼容，可在没有原生 protoc 的机器上直接出码。
if (Get-Command protoc -ErrorAction SilentlyContinue) {
    $protocExe = 'protoc'
    $protocPre = @()
} else {
    if (-not (Get-Command python -ErrorAction SilentlyContinue)) {
        throw "未找到 protoc：请安装 protoc，或安装 Python 后执行 pip install grpcio-tools。"
    }
    & python -c "import grpc_tools" *> $null
    if ($LASTEXITCODE -ne 0) {
        throw "未找到 protoc：请安装 protoc，或执行 pip install grpcio-tools。"
    }
    $protocExe = 'python'
    $protocPre = @('-m', 'grpc_tools.protoc')
}

# 定位插件：PATH 上没有时回落到 GOPATH/bin，并显式告知 protoc 去哪里找。
$pluginArgs = @()
$gobin = "$(go env GOBIN)".Trim()
if (-not $gobin) { $gobin = Join-Path "$(go env GOPATH)".Trim() 'bin' }
foreach ($name in @('protoc-gen-go', 'protoc-gen-go-grpc')) {
    if (Get-Command $name -ErrorAction SilentlyContinue) { continue }
    $exe = Join-Path $gobin "$name.exe"
    if (-not (Test-Path $exe)) {
        throw "未找到 $name，请执行：go install google.golang.org/${name}/...@latest"
    }
    $pluginArgs += "--plugin=$name=$exe"
}

# 收集全部契约文件；脚手架阶段可能还没有契约，直接跳过。
$root = (Get-Location).Path
$protos = Get-ChildItem -Path $ProtoDir -Recurse -Filter *.proto -File |
    Sort-Object FullName |
    ForEach-Object { $_.FullName.Substring($root.Length + 1).Replace('\', '/') }
if (-not $protos) {
    Write-Host "在 $ProtoDir 下未找到 .proto 文件，跳过生成。"
    exit 0
}

# --go_opt=module 让产物的导入路径与 go_package 对齐，落点由 go_package 决定（即 api/gen/...）。
& $protocExe @protocPre `
    "--proto_path=$ProtoDir" `
    @pluginArgs `
    '--go_out=.' "--go_opt=module=$Module" `
    '--go-grpc_out=.' "--go-grpc_opt=module=$Module" `
    @protos
if ($LASTEXITCODE -ne 0) {
    throw "protoc 生成失败，退出码 $LASTEXITCODE"
}

Write-Host "已生成 Go 代码到 $GenDir/"