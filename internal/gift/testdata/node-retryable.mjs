// 集成测试替身：预检可重试失败，退出码1。
process.stdout.write('{"protocol":1,"type":"error","kind":"retryable","message":"网络请求失败"}\n')
process.exitCode = 1
