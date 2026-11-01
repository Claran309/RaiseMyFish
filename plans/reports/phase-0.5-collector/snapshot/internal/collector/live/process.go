package live

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"RaiseMyFish/internal/event"
	"go.uber.org/zap"
)

// New校验静态设置，不读取凭据文件、执行脚本或连接平台。
func New(o Options, log *zap.Logger) (*Collector, error) {
	room, roomOK := canonicalID(o.RoomID)
	self, selfOK := canonicalID(o.SelfID)
	if !roomOK || !selfOK || log == nil || strings.TrimSpace(o.NodePath) == "" || strings.TrimSpace(o.ScriptPath) == "" || (o.Cookie == "" && o.CookieFile == "") || strings.ContainsAny(o.Cookie+o.CookieFile, "\r\n\x00") || o.MaxFailures < 1 || o.MaxFailures > 32 || o.HTTPTimeout < time.Millisecond || o.ConnectTimeout < time.Millisecond || o.Heartbeat < time.Millisecond || o.IdleTimeout <= o.Heartbeat || o.StartupTimeout < time.Millisecond || o.BackoffMin < time.Millisecond || o.BackoffMax < o.BackoffMin || o.BackoffMax > time.Hour || o.StableAfter < time.Millisecond {
		return nil, &Error{Kind: KindInput}
	}
	o.RoomID = room
	o.SelfID = self
	c := &Collector{options: o, log: log}
	c.command = c.buildCommand
	c.pause = func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	return c, nil
}
func (c *Collector) buildCommand(ctx context.Context) *exec.Cmd {
	o := c.options
	// #nosec G204 -- Node与脚本路径来自可信部署配置；逐参数传递且不经shell，平台数据仅允许数字ID。
	cmd := exec.CommandContext(ctx, o.NodePath, o.ScriptPath, "--room", o.RoomID, "--self-id", o.SelfID, "--http-timeout-ms", strconv.FormatInt(o.HTTPTimeout.Milliseconds(), 10), "--connect-timeout-ms", strconv.FormatInt(o.ConnectTimeout.Milliseconds(), 10), "--heartbeat-ms", strconv.FormatInt(o.Heartbeat.Milliseconds(), 10), "--idle-ms", strconv.FormatInt(o.IdleTimeout.Milliseconds(), 10), "--host-index", strconv.Itoa(c.Health().Reconnects))
	cmd.Env = []string{}
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "PATHEXT", "TEMP", "TMP"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	if o.Cookie != "" {
		cmd.Env = append(cmd.Env, "FISH_BILI_COOKIE="+o.Cookie)
	} else {
		cmd.Env = append(cmd.Env, "FISH_BILI_COOKIE_FILE="+o.CookieFile)
	}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

// Start指数退避重连；连续短连接失败有上限，稳定连接后才重置。
func (c *Collector) Start(ctx context.Context, out chan<- event.Event, errc chan<- error) error {
	if ctx == nil || out == nil || errc == nil || !c.started.CompareAndSwap(false, true) {
		return &Error{Kind: KindInput}
	}
	defer c.setOK(false)
	failures := 0
	delay := c.options.BackoffMin
	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		if attempt > 0 {
			c.mu.Lock()
			c.health.Reconnects++
			c.mu.Unlock()
		}
		attempt++
		stable, err := c.once(ctx, out, errc)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			err = &Error{Kind: KindNetwork}
		}
		c.report(errc, err)
		if err.Kind == KindAuth || err.Kind == KindRuntime || err.Kind == KindInput {
			return err
		}
		if stable {
			failures = 0
			delay = c.options.BackoffMin
		}
		failures++
		if failures >= c.options.MaxFailures {
			return &Error{Kind: KindExhausted, Cause: err}
		}
		c.log.Warn("直播连接中断，准备退避重连", zap.String("action", "collector_reconnect"), zap.Int("attempt", failures), zap.Duration("delay", delay))
		if err := c.pause(ctx, delay); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return &Error{Kind: KindNetwork, Cause: err}
		}
		delay = min(delay*2, c.options.BackoffMax)
	}
}

func frameKind(value string) Kind {
	switch value {
	case "auth", "credentials":
		return KindAuth
	case "runtime":
		return KindRuntime
	case "network":
		return KindNetwork
	case "timeout":
		return KindTimeout
	case "rate_limit":
		return KindRate
	default:
		return KindProtocol
	}
}

func (c *Collector) once(parent context.Context, out chan<- event.Event, errc chan<- error) (bool, *Error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	cmd := c.command(ctx)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, &Error{Kind: KindRuntime, Cause: err}
	}
	if err := cmd.Start(); err != nil {
		return false, &Error{Kind: KindRuntime, Cause: err}
	}
	var timedout atomic.Bool
	expire := func() { timedout.Store(true); cancel() }
	timer := time.AfterFunc(c.options.StartupTimeout, expire)
	defer func() { timer.Stop(); c.setOK(false) }()
	reset := func() { timer.Stop(); timer = time.AfterFunc(c.options.IdleTimeout, expire) }
	room := ""
	readyAt := time.Time{}
	var failure *Error
	scanner := bufio.NewScanner(stdout)
	// 与Node的1MiB输出上限一致，额外1字节留给Scanner分隔符检查。
	scanner.Buffer(make([]byte, 4096), 1024*1024+1)
	for scanner.Scan() {
		var f wireFrame
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&f); err != nil {
			c.report(errc, &Error{Kind: KindProtocol, Cause: err})
			continue
		}
		if err := decoder.Decode(new(any)); err != io.EOF || f.Protocol != 1 {
			c.report(errc, &Error{Kind: KindProtocol})
			continue
		}
		switch f.Type {
		case "ready":
			canonical, ok := canonicalID(f.RoomID)
			if !ok || f.SelfID != c.options.SelfID || room != "" {
				failure = &Error{Kind: KindAuth}
				break
			}
			room = canonical
			readyAt = time.Now()
			reset()
			c.setOK(true)
		case "event":
			if room == "" {
				c.report(errc, &Error{Kind: KindProtocol})
				continue
			}
			ev, keep, err := normalize(f, room, c.options.SelfID)
			if err != nil {
				var ce *Error
				if errors.As(err, &ce) {
					c.report(errc, ce)
				}
				continue
			}
			reset()
			if !keep {
				continue
			}
			select {
			case out <- ev:
				c.mu.Lock()
				c.health.LastEventAt = time.Now().UTC()
				c.mu.Unlock()
			case <-ctx.Done():
			}
		case "heartbeat":
			if room != "" {
				reset()
			}
		case "error":
			e := &Error{Kind: frameKind(f.Kind)}
			if f.Fatal || f.Retryable || e.Kind == KindAuth || e.Kind == KindRuntime {
				failure = e
			} else {
				c.report(errc, e)
			}
		default:
			c.report(errc, &Error{Kind: KindProtocol})
		}
		if failure != nil || ctx.Err() != nil {
			break
		}
	}
	scanErr := scanner.Err()
	cancel()
	waitErr := cmd.Wait()
	stable := !readyAt.IsZero() && time.Since(readyAt) >= c.options.StableAfter
	if failure != nil {
		return stable, failure
	}
	if timedout.Load() {
		return stable, &Error{Kind: KindTimeout, Cause: context.DeadlineExceeded}
	}
	return stable, &Error{Kind: KindNetwork, Cause: errors.Join(scanErr, waitErr)}
}
