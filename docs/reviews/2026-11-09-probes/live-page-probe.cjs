// 只读离线探针：执行受审 live.html 的真实内联脚本；不启动服务、不访问网络。
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const root = path.resolve(__dirname, '..', '..');
const source = path.join(root, 'internal', 'web', 'static', 'live.html');
const html = fs.readFileSync(source, 'utf8');
const scriptMatch = html.match(/<script>([\s\S]*?)<\/script>/);
if (!scriptMatch) throw new Error('live.html 缺少内联脚本');

const cases = [
  { name: '直播采集运行', state: { live: { available: true, running: true }, watch: { available: true, running: false } } },
  { name: '直播采集停止', state: { live: { available: true, running: false, enabled: false }, watch: { available: true, running: false } } },
  { name: '直播能力未装配', state: { live: { available: false }, watch: { available: false } } },
];

const results = cases.map(({ name, state }) => {
  const elements = new Map();
  let blockedFetchCalls = 0;
  const context = {
    document: {
      getElementById(id) {
        if (!elements.has(id)) elements.set(id, { value: '', textContent: '', className: '', disabled: false, innerHTML: '' });
        return elements.get(id);
      },
      querySelector() { return { content: 'offline-probe-token' }; },
    },
    // 页面初始化 load() 被挂起；只有 render() 由探针显式调用，绝无真实网络。
    fetch() { blockedFetchCalls += 1; return new Promise(() => {}); },
    setInterval() { return 0; },
    clearInterval() {},
    Date,
    JSON,
  };
  vm.createContext(context);
  try {
    vm.runInContext(scriptMatch[1], context, { filename: source, timeout: 1000 });
    context.probeState = state;
    vm.runInContext('state = probeState; render();', context, { filename: 'offline-render-probe', timeout: 1000 });
    return { name, passed: true, blockedFetchCalls };
  } catch (error) {
    return { name, passed: false, error: { name: error.name, message: error.message }, blockedFetchCalls };
  }
});

console.log(JSON.stringify({
  source: path.relative(root, source).replaceAll('\\', '/'),
  mode: 'offline-node-vm-minimal-dom',
  realNetworkCalls: 0,
  cases: results,
  passed: results.every(result => result.passed),
}, null, 2));
process.exitCode = results.every(result => result.passed) ? 0 : 1;
