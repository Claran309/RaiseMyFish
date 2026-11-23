package conversation

// 本文件实现按会话键的确定性灰度（C2 评论 thread / DM 分桶）。
//
// 为什么不用随机数：同一会话必须在整个灰度期内稳定落在同一侧，
// 否则「这条 thread 有上下文、下一条没有」会让评测数字和线上体感都不可信。
// 这里用 FNV-1a 对会话键取模，键相同 → 桶相同 → 结论相同。

// InRollout 判断会话键是否落在灰度百分比内。
//
// percent<=0 返回 false（关闭）；percent>=100 返回 true（全量）。
// 空键返回 false：没有会话键就没有可灰度的对象。
func InRollout(key string, percent int) bool {
	if percent <= 0 || key == "" {
		return false
	}
	if percent >= 100 {
		return true
	}
	// 取 32 位哈希的低位再对 100 取模，桶分布均匀且实现简单。
	return int(fnv32(key)%100) < percent
}

// Bucket 返回会话键所在的灰度桶（0-99），用于日志与排查「为什么这条没生效」。
// 只暴露桶号，不含 UID/正文。
func Bucket(key string) int {
	if key == "" {
		return -1
	}
	return int(fnv32(key) % 100)
}
