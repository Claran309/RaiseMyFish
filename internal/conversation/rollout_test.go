package conversation

import (
	"fmt"
	"testing"
)

// TestInRolloutIsStableAndBounded 同一键结论稳定；0/100 是确定的开关。
func TestInRolloutIsStableAndBounded(t *testing.T) {
	for _, key := range []string{"comment:1700001:555", "dm:1001:2002", "comment:1700002:600"} {
		first := InRollout(key, 10)
		for i := 0; i < 20; i++ {
			if InRollout(key, 10) != first {
				t.Fatalf("%s 灰度结论不稳定", key)
			}
		}
	}
	if InRollout("dm:1:2", 0) {
		t.Fatal("0% 仍命中")
	}
	if !InRollout("dm:1:2", 100) {
		t.Fatal("100% 未命中")
	}
	if InRollout("", 50) {
		t.Fatal("空键被灰度命中")
	}
}

// TestInRolloutDistribution 桶分布要接近百分比，不能全挤在一侧。
func TestInRolloutDistribution(t *testing.T) {
	const total = 5000
	hit := 0
	for i := 0; i < total; i++ {
		if InRollout(fmt.Sprintf("comment:1700001:%d", i), 10) {
			hit++
		}
	}
	ratio := float64(hit) / total
	if ratio < 0.07 || ratio > 0.13 {
		t.Fatalf("10%% 灰度实际命中 %.2f%%，分布不均", ratio*100)
	}
}

// TestBucketMatchesRollout 桶号与命中结论必须一致，便于按日志排查。
func TestBucketMatchesRollout(t *testing.T) {
	for i := 0; i < 200; i++ {
		key := fmt.Sprintf("dm:1001:%d", i)
		bucket := Bucket(key)
		if bucket < 0 || bucket > 99 {
			t.Fatalf("桶号越界：%d", bucket)
		}
		if InRollout(key, bucket+1) != true {
			t.Fatalf("%s 在 %d%% 下应命中（桶 %d）", key, bucket+1, bucket)
		}
		if bucket > 0 && InRollout(key, bucket) {
			t.Fatalf("%s 在 %d%% 下不应命中（桶 %d）", key, bucket, bucket)
		}
	}
	if Bucket("") != -1 {
		t.Fatal("空键桶号应为 -1")
	}
}
