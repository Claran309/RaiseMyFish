// 集成测试替身：stderr 有噪声也必须被父进程丢弃。
process.stderr.write('credential-private-canary')
const args = process.argv.slice(2)
const value = name => args[args.indexOf(name) + 1]
process.stdout.write(JSON.stringify({
  protocol: 1,
  type: 'sent',
  gift_id: value('--gift-id'),
  num: Number(value('--num')),
}) + '\n')
