# scripts · 开发辅助脚本

放开发期用到的工具脚本，例如：

- `gen_proto.sh` —— 由 `api/proto/` 生成 Go 代码
- `gen_proto.ps1` —— `gen_proto.sh` 的 Windows 等价入口，没有 make 的机器上可直接执行
- `gen_openapi.sh` —— 由 OpenAPI 契约生成前端 TS 类型
- `mock_gen.sh` —— 生成接口 mock

统一通过根目录 `Makefile` 调用，不直接执行脚本；Windows 上没有 make 时，
出码可直接跑 `powershell -ExecutionPolicy Bypass -File scripts/gen_proto.ps1`。
