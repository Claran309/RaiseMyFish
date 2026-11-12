package watch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const playInfoFixture = `{"code":0,"data":{"playurl_info":{"playurl":{"stream":[{"format":[{"codec":[{"base_url":"/live/stream.flv","url_info":[{"host":"https://live.example.test","extra":"?token=secret"}]}]}]}]}}}}`

func newTestRecorder(t *testing.T, handler http.HandlerFunc) (*StreamRecorder, *fakeCommander) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	commander := &fakeCommander{run: func(name string, args ...string) ([]byte, error) {
		if name != "ffmpeg" {
			return nil, errors.New("unexpected command")
		}
		return nil, createLastArg(t, args, []byte("video-bytes"))
	}}
	recorder, err := NewStreamRecorder(StreamRecorderOptions{
		RoomID:     "30308220",
		Cookie:     "SESSDATA=test",
		Quality:    "origin",
		FFmpegPath: "ffmpeg",
		TempDir:    t.TempDir(),
		Command:    commander,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder.playInfo = server.URL
	// 取流失败时还会查开播状态；测试必须打到同一个 httptest，不能访问真实接口。
	recorder.roomInfo = server.URL + "/room_info"
	return recorder, commander
}

func TestStreamRecorderFetchesPlayURLAndRecords(t *testing.T) {
	var gotCookie, gotQuery string
	recorder, commander := newTestRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		gotQuery = r.URL.RawQuery
		fmt.Fprint(w, playInfoFixture)
	})
	path, err := recorder.Record(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if !fileExists(path) {
		t.Fatal("录制文件未生成")
	}
	if gotCookie != "SESSDATA=test" || !strings.Contains(gotQuery, "room_id=30308220") || !strings.Contains(gotQuery, "qn=10000") {
		t.Fatalf("接口请求不符合预期：cookie=%q query=%q", gotCookie, gotQuery)
	}
	if commander.count("ffmpeg") != 1 {
		t.Fatal("未调用ffmpeg录制")
	}
	args := strings.Join(commander.calls[0].Args, " ")
	if !strings.Contains(args, "-t 2") || !strings.Contains(args, "-c copy") || !strings.Contains(args, "https://live.example.test/live/stream.flv?token=secret") {
		t.Fatalf("ffmpeg参数缺少录制要素：%s", args)
	}
	if !strings.Contains(args, "Cookie: SESSDATA=test") {
		t.Fatal("ffmpeg未带凭据")
	}
}

func TestStreamRecorderSnapshot(t *testing.T) {
	recorder, commander := newTestRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, playInfoFixture)
	})
	path, err := recorder.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(path) })
	if !fileExists(path) {
		t.Fatal("截帧文件未生成")
	}
	if commander.count("ffmpeg") != 1 {
		t.Fatal("未调用ffmpeg截帧")
	}
	args := strings.Join(commander.calls[0].Args, " ")
	if !strings.Contains(args, "-frames:v 1") || !strings.Contains(args, "scale=960:-2") {
		t.Fatalf("ffmpeg参数缺少截帧要素：%s", args)
	}
	if !strings.Contains(args, "Cookie: SESSDATA=test") {
		t.Fatal("截帧未带凭据")
	}
}

func TestStreamRecorderRejectsUnavailableStream(t *testing.T) {
	cases := map[string]string{
		"接口错误码":   `{"code":701,"data":{}}`,
		"未开播":     `{"code":0,"data":{"playurl_info":null}}`,
		"无可用地址":   `{"code":0,"data":{"playurl_info":{"playurl":{"stream":[{"format":[{"codec":[{"base_url":"","url_info":[]}]}]}]}}}}`,
		"响应非JSON": `not-json`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			recorder, commander := newTestRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, body)
			})
			_, err := recorder.Record(context.Background(), 2)
			if err == nil || commander.count("ffmpeg") != 0 {
				t.Fatalf("未开播仍录制或未报错：%v", err)
			}
			if strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "secret") {
				t.Fatal("错误泄露了流地址凭据")
			}
		})
	}
}

// TestStreamRecorderDistinguishesOfflineAndNoStream 取流失败要能区分：
// 没开播（live_status=0）与开播但拿不到流（live_status=1/2）是两种故障。
func TestStreamRecorderDistinguishesOfflineAndNoStream(t *testing.T) {
	cases := []struct {
		name     string
		roomBody string
		roomCode int
		want     string
	}{
		{"未开播", `{"code":0,"data":{"live_status":0}}`, http.StatusOK, "直播未开播（live_status=0）"},
		{"直播中但无流", `{"code":0,"data":{"live_status":1}}`, http.StatusOK, "直播进行中但没有可用流（live_status=1）"},
		{"轮播中但无流", `{"code":0,"data":{"live_status":2}}`, http.StatusOK, "直播进行中但没有可用流（live_status=2）"},
		{"状态接口异常", `{"code":-500,"data":{}}`, http.StatusOK, "直播未开播或没有可用流"},
		{"状态接口HTTP错误", ``, http.StatusInternalServerError, "直播未开播或没有可用流"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder, commander := newTestRecorder(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "room_info") {
					w.WriteHeader(tc.roomCode)
					fmt.Fprint(w, tc.roomBody)
					return
				}
				fmt.Fprint(w, `{"code":0,"data":{"playurl_info":null}}`)
			})
			_, err := recorder.Record(context.Background(), 2)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误=%v，期望包含 %q", err, tc.want)
			}
			if commander.count("ffmpeg") != 0 {
				t.Fatal("取流失败仍调用了ffmpeg")
			}
			if strings.Contains(err.Error(), "SESSDATA=test") {
				t.Fatal("错误泄露了凭据")
			}
		})
	}
}

func TestStreamRecorderHTTPFailureAndBadRequests(t *testing.T) {
	recorder, commander := newTestRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	if _, err := recorder.Record(context.Background(), 2); err == nil || commander.count("ffmpeg") != 0 {
		t.Fatal("HTTP失败仍录制")
	}
	if _, err := recorder.Record(context.Background(), 0); err == nil {
		t.Fatal("接受非法秒数")
	}
	if _, err := NewStreamRecorder(StreamRecorderOptions{RoomID: "1", Quality: "origin", FFmpegPath: "ffmpeg"}); err == nil {
		t.Fatal("接受空凭据")
	}
	if _, err := NewStreamRecorder(StreamRecorderOptions{RoomID: "1", Cookie: "a", Quality: "unknown", FFmpegPath: "ffmpeg"}); err == nil {
		t.Fatal("接受未知清晰度")
	}
}

func TestStreamRecorderFailsWhenFFmpegFails(t *testing.T) {
	recorder, commander := newTestRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, playInfoFixture)
	})
	commander.run = func(_ string, _ ...string) ([]byte, error) {
		return nil, errors.New("ffmpeg 执行失败（[url]）：exit status 1")
	}
	if _, err := recorder.Record(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "录制直播流失败") {
		t.Fatalf("ffmpeg失败未显式报错：%v", err)
	}
}

func TestParseQuality(t *testing.T) {
	for name, want := range map[string]int{"origin": 10000, "bluray": 400, "ultra": 250, "high": 150, "smooth": 80} {
		got, err := ParseQuality(name)
		if err != nil || got != want {
			t.Fatalf("%s=%d err=%v", name, got, err)
		}
	}
	if _, err := ParseQuality("4k"); err == nil {
		t.Fatal("接受未知清晰度")
	}
}
