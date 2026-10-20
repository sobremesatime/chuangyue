// 窗月 · 网关读路径压测基线（k6）
//
// 目标：量出「网关读入口」在**限流保护之外**的原始承载能力，作为后续优化的对照基线。
//
// 为什么只压读路径：写路径（上传 / 互动）需要管理员身份与真实文件，变量太多、不适合做基线；
// 而读路径（首页信息流 + 视频详情）才是流量占比最高的那条链路。
// 为什么是「固定到达率」而不是「固定并发」：到达率直接对应线上 QPS 口径，且能精确复用同一压力档位，
// 便于把不同档位的结果并排对比、找到延迟拐点。
//
// 用法（k6 容器接入 compose 网络，直接用服务名访问网关，免去 host.docker.internal 的差异）：
//   docker run --rm --network chuangyue_default \
//     -v "<repo>/test/loadtest:/scripts" \
//     -e RATE=100 -e BASE_URL=http://gateway:8080 \
//     grafana/k6 run /scripts/read-path.js
//
// 参数：
//   RATE          目标到达率（请求/秒），constant-arrival-rate 精确打流
//   DURATION      该档位持续时长，默认 60s
//   DETAIL_RATIO  视频详情占比，默认 0.3（其余走首页信息流）
//   VIDEO_IDS     详情压测用的视频 ID，逗号分隔
//   BASE_URL      网关地址，默认 http://gateway:8080
//
// 前置条件（重要）：网关默认按 IP 限流（ratelimit.ip_per_second = 20）。压测流量全部来自同一个
// 源 IP，不抬高该阈值就只会量到「限流挡下 429」的曲线，量不到服务真实能力。
// 因此基线的做法是临时抬高该阈值，细节与还原方式见同目录 results/README.md。
import http from 'k6/http';
import { check } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://gateway:8080';
const RATE = Number(__ENV.RATE || 100);
const DURATION = __ENV.DURATION || '60s';
const DETAIL_RATIO = Number(__ENV.DETAIL_RATIO || 0.3);
const VIDEO_IDS = (__ENV.VIDEO_IDS || '2109676650810576896,2109508717052432384,2109501405638168578')
  .split(',')
  .map((s) => s.trim())
  .filter((s) => s !== '');

// VU 预算按「小定律」粗估：所需 VU ≈ 到达率 × 单请求耗时。
// 预分配取到达率的一半是留足冗余（压测中延迟会随档位上升，VU 不够会被计成 dropped_iterations，
// 污染结果）；上限放开到 2 倍，避免高延迟时排队丢请求。
const preAllocatedVUs = Math.max(50, Math.min(1000, Math.ceil(RATE / 2)));
const maxVUs = Math.max(500, Math.min(3000, RATE * 2));

export const options = {
  // 关掉内建的 setup/teardown 之外的打扰项：不做 sleep，由到达率执行器自己控速。
  discardResponseBodies: false,
  scenarios: {
    read_path: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs,
      maxVUs,
    },
  },
  // 阈值是「护栏」而非目标：基线要的是曲线，不是把某档位调成绿色。
  // 因此这里只拦「明显坏掉」的情况，具体达标情况在 results/README.md 里逐档位记录。
  thresholds: {
    'http_req_failed{name:feed}': ['rate<0.01'],
    'http_req_duration{name:feed}': ['p(95)<300'],
    'http_req_failed{name:video_detail}': ['rate<0.01'],
    'http_req_duration{name:video_detail}': ['p(95)<400'],
  },
  // 图表/汇总里的标签口径。
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

// 按比例决定本次请求打哪个接口，模拟「首页刷流 + 点进详情」的真实配比。
function pickEndpoint() {
  return Math.random() < DETAIL_RATIO ? 'detail' : 'feed';
}

function requestFeed() {
  const res = http.get(`${BASE_URL}/api/v1/feed?page=1&page_size=20`, {
    tags: { name: 'feed' },
  });
  check(res, {
    'feed 200': (r) => r.status === 200,
    'feed code=0': (r) => r.status === 200 && JSON.parse(r.body).code === 0,
  });
}

function requestDetail() {
  const id = VIDEO_IDS[Math.floor(Math.random() * VIDEO_IDS.length)];
  const res = http.get(`${BASE_URL}/api/v1/videos/${id}`, {
    tags: { name: 'video_detail' },
  });
  check(res, {
    'detail 200': (r) => r.status === 200,
    'detail code=0': (r) => r.status === 200 && JSON.parse(r.body).code === 0,
  });
}

export default function () {
  if (pickEndpoint() === 'detail') {
    requestDetail();
    return;
  }
  requestFeed();
}

// 只落「本轮结论要用到的字段」，避免把 k6 全量 summary（含每条子指标）塞进仓库。
function pickMetrics(data) {
  const wanted = [
    'http_reqs',
    'http_req_failed',
    'http_req_duration',
    'http_req_duration{name:feed}',
    'http_req_duration{name:video_detail}',
    'http_req_failed{name:feed}',
    'http_req_failed{name:video_detail}',
    'dropped_iterations',
    'vus_max',
    'iteration_duration',
  ];
  const out = {};
  for (const key of wanted) {
    const m = data.metrics[key];
    if (m) {
      out[key] = { values: m.values };
    }
  }
  // checks 汇总：通过 / 失败条目数。
  const checks = data.metrics.checks;
  if (checks) {
    out.checks = {
      passes: checks.values.passes,
      fails: checks.values.fails,
    };
  }
  return out;
}

export function handleSummary(data) {
  const report = {
    rate: RATE,
    duration: DURATION,
    detail_ratio: DETAIL_RATIO,
    base_url: BASE_URL,
    video_ids: VIDEO_IDS,
    metrics: pickMetrics(data),
  };
  const file = `/scripts/results/${RATE}rps.json`;
  const okFeed = report.metrics['http_req_duration{name:feed}']?.values;
  const okDetail = report.metrics['http_req_duration{name:video_detail}']?.values;
  const reqs = report.metrics['http_reqs']?.values;
  const lines = [
    `==== 到达率 ${RATE} rps / ${DURATION} ====`,
    `  总请求 ${reqs ? reqs.count : '-'}（实际 ${reqs ? (reqs.rate).toFixed(1) : '-'} rps）`,
    okFeed
      ? `  feed   avg=${okFeed.avg.toFixed(1)}ms p95=${okFeed['p(95)'].toFixed(1)}ms p99=${okFeed['p(99)'].toFixed(1)}ms max=${okFeed.max.toFixed(1)}ms`
      : '  feed   （无数据）',
    okDetail
      ? `  详情   avg=${okDetail.avg.toFixed(1)}ms p95=${okDetail['p(95)'].toFixed(1)}ms p99=${okDetail['p(99)'].toFixed(1)}ms max=${okDetail.max.toFixed(1)}ms`
      : '  详情   （无数据）',
  ];
  return {
    [file]: JSON.stringify(report, null, 2),
    stdout: lines.join('\n') + '\n',
  };
}
