// 集成测试替身：不可重试失败，退出码2。
process.stdout.write('{"protocol":1,"type":"error","kind":"fatal","message":"礼物发送被拒绝（code=-400）"}\n')
process.exitCode = 2
