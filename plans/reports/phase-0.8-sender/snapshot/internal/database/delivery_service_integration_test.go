//go:build integration

package database

import (
	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/sender"
	sendlive "RaiseMyFish/internal/sender/live"
	"go.uber.org/zap"
	"path/filepath"
	"testing"
)

func TestDeliveryNodeServiceRealPostgres(t *testing.T) {
	for _, mode := range []string{"accepted", "unverified", "unknown", "real-adapter"} {
		t.Run(mode, func(t *testing.T) {
			db, ctx := repositoryDB(t)
			repo := sendRepo(t, db)
			opts := sendlive.DefaultNodeOptions()
			opts.Cookie = "SESSDATA=synthetic; bili_jct=syntheticcsrf; buvid3=syntheticbuvid"
			opts.ScriptPath = filepath.Join("..", "sender", "live", "testdata", "node-"+mode+".mjs")
			transport, err := sendlive.NewNodeTransport(opts)
			if err != nil {
				t.Fatal(err)
			}
			serviceOptions := sendlive.DefaultOptions()
			serviceOptions.Enabled = true
			serviceOptions.SelfID = "8"
			service, err := sendlive.New(repo, transport, serviceOptions, zap.NewNop())
			if err != nil {
				t.Fatal(err)
			}
			c := sendFixture(t, db, ctx, "live:123:node-"+mode, "123")
			result, err := service.Send(ctx, c, sendReply("123", "合成发送文本"))
			if mode == "accepted" || mode == "real-adapter" {
				if err != nil || !result.Verified {
					t.Fatal("单POST回执未结算", result, err)
				}
				sendState(t, db, c.Event.ID, event.StatusDone, event.StageVerify)
			} else {
				if !sender.Handled(err) || result.Verified {
					t.Fatal("未知结果未保守处理", result, err)
				}
				sendState(t, db, c.Event.ID, event.StatusRetry, event.StageVerify)
				if mode == "unverified" {
					resumed := sendReclaim(t, db, ctx, c.Event.ID)
					result, err = service.Resume(ctx, resumed)
					if err != nil || !result.Verified {
						t.Fatal("只读恢复核验未完成", result, err)
					}
					sendState(t, db, c.Event.ID, event.StatusDone, event.StageVerify)
				} else {
					for range 3 {
						resumed := sendReclaim(t, db, ctx, c.Event.ID)
						result, err = service.Resume(ctx, resumed)
						if !sender.Handled(err) || result.Verified {
							t.Fatal("失败核验错误", result, err)
						}
					}
					sendState(t, db, c.Event.ID, event.StatusDead, event.StageVerify)
					var out struct{ Status string }
					if err := db.Table("events").Select("status").Where("dir='out'").Take(&out).Error; err != nil || out.Status != "retry" {
						t.Fatal("未知耗尽不应伪造失败发言", err)
					}
				}
			}
			sendCounts(t, db, 1)
			var calls int64
			if err := db.Table("llm_usage").Count(&calls).Error; err != nil || calls != 0 {
				t.Fatal("发送器不应调模型", err)
			}
			var plans []struct{ Text string }
			if err := db.Table("send_plans").Select("text").Find(&plans).Error; err != nil || len(plans) != 1 || plans[0].Text != "合成发送文本" {
				t.Fatal("恢复更换了回复", err)
			}
		})
	}
}

func TestDeliveryServiceDefaultDoesNotOpenTransport(t *testing.T) {
	db, ctx := repositoryDB(t)
	repo := sendRepo(t, db)
	nodeOptions := sendlive.DefaultNodeOptions()
	nodeOptions.NodePath = "missing-runtime"
	transport, err := sendlive.NewNodeTransport(nodeOptions)
	if err != nil {
		t.Fatal(err)
	}
	opts := sendlive.DefaultOptions()
	opts.SelfID = "8"
	svc, err := sendlive.New(repo, transport, opts, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	c := sendFixture(t, db, ctx, "live:123:disabled", "123")
	if _, err := svc.Send(ctx, c, sendReply("123", "默认不发送")); err == nil {
		t.Fatal("默认开启发送")
	}
	var count int64
	if err := db.Table("send_attempts").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("关闭仍产生发送尝试", err)
	}
}
