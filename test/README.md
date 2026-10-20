# test · 集成测试与压测

| 内容 | 说明 |
| --- | --- |
| 集成测试 | 跨服务链路验证，需要依赖已启动（`deploy/compose/`） |
| 压测脚本 | 针对核心接口的压测，如首页信息流、视频详情 |

## 集成测试

- 单元测试与源码同目录（`*_test.go`），只有跨服务测试放这里。
- 集成测试通过环境变量判断依赖是否可用，不可用则跳过。

## 压测（`loadtest/`）

`loadtest/read-path.js` 是首页信息流 + 视频详情的读路径压测脚本（k6），
基线数据与方法、结论见 [`loadtest/results/README.md`](./loadtest/results/README.md)。

k6 用容器跑、接入 compose 网络访问网关，宿主机无需安装：

```bash
docker run --rm --network chuangyue_default \
  -v "<repo>/test/loadtest:/scripts" \
  -e RATE=10 -e DURATION=60s -e BASE_URL=http://gateway:8080 \
  grafana/k6 run /scripts/read-path.js
```

> 压测流量全部来自同一个源 IP，会被网关的按 IP 限流（默认 20 rps）挡住 ——
> 想量「限流之外」的服务容量，需另行处理源 IP 或限流阈值，详见上述 results 文档。

