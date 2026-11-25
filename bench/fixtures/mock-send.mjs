#!/usr/bin/env node
// mock-send.mjs 是压测用的「发送脚本替身」：只解析参数并返回稳定回执，
// 不访问任何平台、不读取 cookie。用于验证 sender 的进程协议与回执解析。
//
// 用法示例：
//   node bench/fixtures/mock-send.mjs --room 10000001 --text "压测" --msg-id bench-1
// 输出：{"code":0,"data":{"msg_id":"bench-1"}}

function parseArgs(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i += 1) {
    const key = argv[i];
    if (!key.startsWith("--")) continue;
    const name = key.slice(2);
    const value = argv[i + 1];
    if (value === undefined || value.startsWith("--")) {
      out[name] = true;
      continue;
    }
    out[name] = value;
    i += 1;
  }
  return out;
}

const args = parseArgs(process.argv.slice(2));
if (args.help) {
  process.stdout.write("mock-send.mjs --room <id> --text <text> [--msg-id <id>] [--fail]\n");
  process.exit(0);
}
if (args.fail) {
  process.stdout.write(JSON.stringify({ code: -500, message: "mock injected send failure" }) + "\n");
  process.exit(1);
}
const room = String(args.room || "").trim();
const text = String(args.text || "").trim();
if (!room || !text) {
  process.stdout.write(JSON.stringify({ code: -400, message: "room/text required" }) + "\n");
  process.exit(2);
}
const msgID = String(args["msg-id"] || `mock-${room}-${text.length}`);
process.stdout.write(JSON.stringify({ code: 0, data: { msg_id: msgID } }) + "\n");
