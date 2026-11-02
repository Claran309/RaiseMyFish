// 集成测试替身：回显请求参数，不访问网络。
const args = process.argv.slice(2)
const value = name => args[args.indexOf(name) + 1]
process.stdout.write(JSON.stringify({
  protocol: 1,
  type: 'sent',
  gift_id: value('--gift-id'),
  num: Number(value('--num')),
}) + '\n')
