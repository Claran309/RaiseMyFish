package bus

import (
	"context"
	"errors"
	"testing"
	"time"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/store"
)

type fakeQueue struct {
	store.QueueRepo
	inserted  bool
	err       error
	owner     string
	recovered int
}

func (f *fakeQueue) Insert(context.Context, event.Event) (bool, error) { return f.inserted, f.err }
func (f *fakeQueue) Claim(_ context.Context, owner string, _ time.Duration, _ int) ([]event.Claim, error) {
	f.owner = owner
	return nil, f.err
}
func (f *fakeQueue) RecoverExpired(context.Context, int) (int, error) { return f.recovered, f.err }
func (f *fakeQueue) MarkClaim(context.Context, event.Claim, []event.Status, event.Status, time.Duration) error {
	return f.err
}
func (f *fakeQueue) DeferClaim(context.Context, event.Claim, time.Time) error { return f.err }
func (f *fakeQueue) RenewClaim(_ context.Context, c event.Claim, _ time.Duration) (event.Claim, error) {
	return c, f.err
}

func TestPublishNotifyMergedAndNoBackgroundWork(t *testing.T) {
	repo := &fakeQueue{inserted: true}
	b, err := New(repo, 0)
	if err != nil {
		t.Fatal(err)
	}
	var _ event.Bus = b
	ctx := context.Background()
	ev := event.Event{Dir: event.DirIn}
	for range 100 {
		if ok, err := b.Publish(ctx, ev); !ok || err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-b.Notify():
	default:
		t.Fatal("未唤醒")
	}
	select {
	case <-b.Notify():
		t.Fatal("信号应合并")
	default:
	}
	repo.inserted = false
	if _, err := b.Publish(ctx, ev); err != nil {
		t.Fatal(err)
	}
	repo.inserted = true
	ev.Dir = event.DirOut
	if _, err := b.Publish(ctx, ev); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.Notify():
		t.Fatal("重复/出事件不应通知")
	default:
	}
	repo.err = errors.New("fake")
	if _, err := b.Publish(ctx, ev); err == nil {
		t.Fatal("吞错")
	}
	repo.err = nil
	repo.recovered = 1
	if n, err := b.Recover(ctx, 10); n != 1 || err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.Notify():
	default:
		t.Fatal("恢复应通知")
	}
	if _, err := b.ClaimPending(ctx, 1); err != nil || repo.owner == "" {
		t.Fatal("缺实例持有权")
	}
	other := &fakeQueue{}
	b2, _ := New(other, time.Second)
	if _, err := b2.ClaimPending(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if repo.owner == other.owner {
		t.Fatal("实例Owner重复")
	}
}

func TestBusValidation(t *testing.T) {
	if _, err := New(nil, 0); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("接受nil repo")
	}
	for _, ttl := range []time.Duration{-1, time.Millisecond, 25 * time.Hour} {
		if _, err := New(&fakeQueue{}, ttl); !errors.Is(err, store.ErrInvalid) {
			t.Fatal("接受无效租期")
		}
	}
	b, _ := New(&fakeQueue{}, 0)
	//nolint:staticcheck // SA1012：刻意验证nil Context的防御边界。
	if _, err := b.Publish(nil, event.Event{}); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("接受nil Context")
	}
	for _, n := range []int{0, -1, 101} {
		if _, err := b.ClaimPending(context.Background(), n); !errors.Is(err, store.ErrInvalid) {
			t.Fatal("接受无界claim")
		}
		if _, err := b.Recover(context.Background(), n); !errors.Is(err, store.ErrInvalid) {
			t.Fatal("接受无界recover")
		}
	}
}

func TestNewRejectsTypedNilRepository(t *testing.T) {
	var repo *fakeQueue
	if _, err := New(repo, 0); !errors.Is(err, store.ErrInvalid) {
		t.Fatal("接口包装的nil仓储应被拒绝，不能把panic留给Publish")
	}
}

func TestBusDelegationAndNilContext(t *testing.T) {
	sentinel := errors.New("storage-failed")
	repo := &fakeQueue{err: sentinel}
	b, _ := New(repo, 0)
	for _, run := range []func(context.Context) error{
		func(ctx context.Context) error { _, err := b.Publish(ctx, event.Event{}); return err },
		func(ctx context.Context) error { _, err := b.ClaimPending(ctx, 1); return err },
		func(ctx context.Context) error { return b.Mark(ctx, event.Claim{}, nil, event.StatusDone) },
		func(ctx context.Context) error { return b.Defer(ctx, event.Claim{}, time.Now()) },
		func(ctx context.Context) error { _, err := b.Renew(ctx, event.Claim{}); return err },
		func(ctx context.Context) error { _, err := b.Recover(ctx, 1); return err },
	} {
		if err := run(context.Background()); !errors.Is(err, sentinel) {
			t.Fatal("未透传存储错误")
		}
		//nolint:staticcheck // SA1012：刻意验证nil Context拒绝，不改变生产调用规范。
		if err := run(nil); !errors.Is(err, store.ErrInvalid) {
			t.Fatal("接受nil Context")
		}
	}
	select {
	case <-b.Notify():
		t.Fatal("失败不应唤醒")
	default:
	}
}
