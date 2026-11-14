package brain

// 峰谷计价：DeepSeek 官方错峰优惠——峰段（UTC 周一至周五 01:00-04:00、06:00-10:00，
// 不含中国法定假日）价格是谷段的两倍。这里只做「按当前时刻选价」，
// 不做节假日历（节假日按谷段算属保守估计，不会高估）。

import "time"

// PeakWindow 描述峰段：UTC 小时区间 + 是否只在工作日。
type PeakWindow struct {
	WeekdaysOnly bool
	// Ranges 是 UTC 小时区间 [start,end)，可跨 0 点由调用方拆开。
	Ranges [][2]int
}

// DefaultDeepSeekPeak 是 DeepSeek 官方峰段：UTC 01-04、06-10，周一至周五。
func DefaultDeepSeekPeak() PeakWindow {
	return PeakWindow{WeekdaysOnly: true, Ranges: [][2]int{{1, 4}, {6, 10}}}
}

// InPeak 判断给定时刻是否落在峰段。
func (w PeakWindow) InPeak(at time.Time) bool {
	utc := at.UTC()
	if w.WeekdaysOnly {
		switch utc.Weekday() {
		case time.Saturday, time.Sunday:
			return false
		}
	}
	hour := utc.Hour()
	for _, r := range w.Ranges {
		if hour >= r[0] && hour < r[1] {
			return true
		}
	}
	return false
}
