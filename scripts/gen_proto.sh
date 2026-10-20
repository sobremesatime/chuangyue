#!/usr/bin/env bash
#
# 由 api/proto 下的契约生成 Go 代码（消息结构 + gRPC 桩）。
# 通过根目录 Makefile 的 `make proto` 调用，不直接执行。
#
# 依赖（需自行安装到 PATH）：
#   protoc                   https://github.com/protocolbuffers/protobuf/releases
#   protoc-gen-go            go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   protoc-gen-go-grpc       go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
set -euo pipefail

MODULE="github.com/sobremesatime/chuangyue"
PROTO_DIR="api/proto"
GEN_DIR="api/gen"

require() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "未找到 $1，请先安装：$2" >&2
    exit 1
  }
}

require protoc "见 scripts/gen_proto.sh 顶部注释"
require protoc-gen-go "go install google.golang.org/protobuf/cmd/protoc-gen-go@latest"
require protoc-gen-go-grpc "go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest"

# 收集全部契约文件；没有契约时直接返回，便于脚手架阶段调用。
PROTOS=$(find "$PROTO_DIR" -name '*.proto' | sort)
if [ -z "$PROTOS" ]; then
  echo "在 $PROTO_DIR 下未找到 .proto 文件，跳过生成。"
  exit 0
fi

# --go_opt=module 让产物的导入路径与 go_package 对齐，落点由 go_package 决定（即 api/gen/...）。
# shellcheck disable=SC2086
protoc \
  --proto_path="$PROTO_DIR" \
  --go_out=. --go_opt=module="$MODULE" \
  --go-grpc_out=. --go-grpc_opt=module="$MODULE" \
  $PROTOS

echo "已生成 Go 代码到 $GEN_DIR/"