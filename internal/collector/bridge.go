package collector

import (
	"RaiseMyFish/internal/event"
	"context"
	"errors"
	"go.uber.org/zap"
	"reflect"
	"time"
)

type bridgeError struct {
	message string
	cause   error
}

func (e *bridgeError) Error() string { return e.message }
func (e *bridgeError) Unwrap() error { return e.cause }
func nilValue(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		return v.IsNil()
	}
	return false
}

// RunToBus在Publish成功前施加背压；重复插入仍为成功，落库失败则停采集并保留原因链。
func RunToBus(ctx context.Context, c Collector, bus Publisher, timeout time.Duration, log *zap.Logger) error {
	if ctx == nil || nilValue(c) || nilValue(bus) || timeout <= 0 || log == nil {
		return errors.New("采集转发配置无效")
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make(chan event.Event)
	errc := make(chan error, 16)
	finished := make(chan error, 1)
	go func() {
		var result error
		defer func() {
			if recover() != nil {
				result = errors.New("采集器发生panic")
			}
			finished <- result
		}()
		result = c.Start(child, out, errc)
	}()
	stop := func() error { cancel(); return <-finished }
	for {
		select {
		case <-ctx.Done():
			if err := stop(); err != nil && !errors.Is(err, context.Canceled) {
				log.Warn("采集取消收尾时报告异常", zap.String("action", "collector_stop_error"))
			}
			return nil
		case err := <-finished:
			cancel()
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				return &bridgeError{"采集器已暂停", err}
			}
			return nil
		case _, ok := <-errc:
			if !ok {
				errc = nil
				continue
			}
			log.Warn("采集器报告暂时异常", zap.String("action", "collector_error"))
		case ev, ok := <-out:
			if !ok {
				return &bridgeError{"采集器输出提前关闭", stop()}
			}
			publish, stopPublish := context.WithTimeout(child, timeout)
			_, err := bus.Publish(publish, ev)
			stopPublish()
			if err != nil {
				stopErr := stop()
				if ctx.Err() != nil {
					if stopErr != nil && !errors.Is(stopErr, context.Canceled) {
						log.Warn("采集取消收尾时报告异常", zap.String("action", "collector_stop_error"))
					}
					return nil
				}
				return &bridgeError{"采集事件持久化失败，已停止采集", errors.Join(err, stopErr)}
			}
		}
	}
}
