package polling

import (
	"math/rand"
	"testing"
	"time"
)

func TestPolicyActiveIdleAndBackoff(t *testing.T) {
	p := Policy{Min: 20 * time.Second, Max: 60 * time.Second, Jitter: 0, BackoffFactor: 2}
	if got := p.Next(true, false, 0, nil); got != 20*time.Second {
		t.Fatalf("活跃间隔=%v", got)
	}
	if got := p.Next(false, false, 0, nil); got != 60*time.Second {
		t.Fatalf("空闲间隔=%v", got)
	}
	if got := p.Next(true, true, 1, nil); got != 40*time.Second {
		t.Fatalf("一次限流退避=%v", got)
	}
	// 指数退避有上限，不能无限放大。
	if got := p.Next(true, true, 10, nil); got > 60*time.Second*4 {
		t.Fatalf("退避未封顶：%v", got)
	}
}

func TestPolicyJitterWithinBounds(t *testing.T) {
	p := Policy{Min: 20 * time.Second, Max: 60 * time.Second, Jitter: 0.2, BackoffFactor: 2}
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 100; i++ {
		got := p.Next(true, false, 0, rnd)
		if got < 16*time.Second || got > 24*time.Second {
			t.Fatalf("抖动越界：%v", got)
		}
	}
}

func TestPolicyResolveDefaults(t *testing.T) {
	p := Policy{}.Resolve()
	if p.Min != 20*time.Second || p.Max < p.Min || p.BackoffFactor < 1 {
		t.Fatalf("默认值错误：%+v", p)
	}
	if bad := (Policy{Min: time.Second, Max: time.Second, Jitter: 2}).Resolve(); bad.Jitter != 0.5 {
		t.Fatalf("jitter 未夹紧：%v", bad.Jitter)
	}
}
