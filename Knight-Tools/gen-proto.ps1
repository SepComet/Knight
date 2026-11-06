# Knight 协议代码生成脚本
# 用法：在仓库根目录执行  ./Knight-Tools/gen-proto.ps1
#
# 前置依赖：
#   - protoc 33.0 在 PATH 中（https://github.com/protocolbuffers/protobuf/releases/tag/v33.0）
#   - protoc-gen-go 1.36.10 在 PATH 中（go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.10）
#
# 运行时依赖（生成后两端编译需要）：
#   - C#：Google.Protobuf 包（Unity 侧引入方式见 Docs/protocol.md §12）
#   - Go：google.golang.org/protobuf
#
# 产物（全量重生成，先清理旧文件防漂移）：
#   - Knight-Client/Assets/Scripts/Proto/Generated/Knight.cs
#   - Knight-Server/protogen/knight.pb.go

#Requires -Version 5.1
$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$protoDir = Join-Path $repoRoot "Knight-Proto"
$protoFile = Join-Path $protoDir "knight.proto"
$csharpOut = Join-Path $repoRoot "Knight-Client\Assets\Scripts\Proto\Generated"
$goOut = Join-Path $repoRoot "Knight-Server\protogen"

if (-not (Test-Path $protoFile)) {
    throw "proto 文件不存在: $protoFile"
}

New-Item -ItemType Directory -Force -Path $csharpOut, $goOut | Out-Null

# 全量重生成：先清理旧产物，防止消息改名后残留旧文件造成两端映射漂移
Remove-Item (Join-Path $csharpOut "*.cs") -ErrorAction SilentlyContinue
Remove-Item (Join-Path $goOut "*.pb.go") -ErrorAction SilentlyContinue

protoc -I $protoDir --csharp_out=$csharpOut $protoFile
if ($LASTEXITCODE -ne 0) { throw "C# 生成失败" }

protoc -I $protoDir --go_out=$goOut --go_opt=paths=source_relative $protoFile
if ($LASTEXITCODE -ne 0) { throw "Go 生成失败" }

Write-Host "生成完成:"
Write-Host "  C#: $csharpOut"
Write-Host "  Go: $goOut"
