// 生成或脱敏压测事件 JSONL：go run ./bench/fixtures --generate 10000 --out bench/raw/events.jsonl
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("fixtures", flag.ContinueOnError)
	in := fs.String("in", "", "输入 JSONL（脱敏模式）")
	out := fs.String("out", "", "输出 JSONL")
	generate := fs.Int("generate", 0, "生成合成事件数量（与 --in 二选一）")
	salt := fs.String("salt", "raisemyfish-bench", "UID 哈希盐")
	maxRunes := fs.Int("max-runes", 80, "正文最大 rune 数")
	livePct := fs.Int("live-pct", 70, "直播弹幕占比")
	commentPct := fs.Int("comment-pct", 15, "评论占比")
	sameUser := fs.Bool("same-user", false, "所有事件使用同一个用户（刷屏场景）")
	spacing := fs.Duration("spacing", 4*time.Minute, "相邻事件的时间间隔")
	sessionShards := fs.Int("session-shards", 1, "直播会话分片数（多 worker 竞争场景）")
	seed := fs.Int64("seed", 20261124, "随机种子")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*out) == "" {
		return errors.New("必须指定 --out")
	}
	if (*in == "") == (*generate == 0) {
		return errors.New("--in 与 --generate 必须二选一")
	}
	if *livePct < 0 || *commentPct < 0 || *livePct+*commentPct > 100 {
		return errors.New("通道占比非法")
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return fmt.Errorf("创建输出目录：%w", err)
	}
	f, err := os.Create(*out)
	if err != nil {
		return fmt.Errorf("创建输出文件：%w", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	opts := DefaultSanitizeOptions()
	opts.Salt, opts.MaxRunes = *salt, *maxRunes
	if *in != "" {
		count, err := sanitizeFile(*in, w, opts)
		if err != nil {
			return err
		}
		fmt.Printf("已脱敏 %d 条 → %s\n", count, *out)
		return nil
	}
	rnd := rand.New(rand.NewSource(*seed))
	now := time.Now().UTC()
	dmPct := 100 - *livePct - *commentPct
	for i := 0; i < *generate; i++ {
		record := generateRecord(i, rnd, now, *livePct, *commentPct, dmPct, *sameUser, *spacing, *sessionShards)
		if err := validateRecord(record); err != nil {
			return fmt.Errorf("第 %d 条样本非法：%w", i+1, err)
		}
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if _, err := w.WriteString(string(data) + "\n"); err != nil {
			return err
		}
	}
	fmt.Printf("已生成 %d 条 → %s\n", *generate, *out)
	return nil
}

func sanitizeFile(path string, w *bufio.Writer, opts SanitizeOptions) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("打开输入：%w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	count := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return count, fmt.Errorf("第 %d 行不是合法 JSON：%w", count+1, err)
		}
		if err := validateRecord(record); err != nil {
			return count, fmt.Errorf("第 %d 行非法：%w", count+1, err)
		}
		clean := RecordFromEvent(SanitizeEvent(record.Event(), opts))
		data, err := json.Marshal(clean)
		if err != nil {
			return count, err
		}
		if _, err := w.WriteString(string(data) + "\n"); err != nil {
			return count, err
		}
		count++
	}
	return count, scanner.Err()
}

func generateRecord(index int, rnd *rand.Rand, now time.Time, livePct, commentPct, dmPct int, sameUser bool, spacing time.Duration, sessionShards int) Record {
	pick := rnd.Intn(100)
	if spacing <= 0 {
		spacing = 4 * time.Minute
	}
	if sessionShards < 1 {
		sessionShards = 1
	}
	// 从 now 往前铺一条严格升序的时间线；staging 用 1s 间隔避免历史样本被 stale 丢弃。
	at := now.Add(-time.Duration(10000) * spacing).Add(time.Duration(index) * spacing)
	fromID := fmt.Sprintf("9%012d", 1+rnd.Intn(999999999999))
	if sameUser {
		fromID = "911111111111"
	}
	text := fmt.Sprintf("压测弹幕 %d：今天鱼在吗？这是用于 staging 的脱敏样本。", index+1)
	switch {
	case pick < livePct:
		room := fmt.Sprintf("100%05d", 1+index%sessionShards)
		return Record{
			ID: fmt.Sprintf("live:%s:%d", room, index+1), Channel: "live", Dir: "in",
			Session: room, FromID: fromID, FromName: "观众" + fromID[len(fromID)-4:],
			Text: text, At: at, CollectMode: "feed",
			Meta: json.RawMessage(fmt.Sprintf(`{"uid":"%s","platform":"bili"}`, fromID)),
		}
	case pick < livePct+commentPct:
		oid := "880000000000000001"
		return Record{
			ID: fmt.Sprintf("comment:%s:%d", oid, index+1), Channel: "comment", Dir: "in",
			Session: oid, FromID: fromID, FromName: "评论观众" + fromID[len(fromID)-4:],
			Text: text, At: at, CollectMode: "poll",
			Meta: json.RawMessage(fmt.Sprintf(`{"rpid":"%d","uid":"%s"}`, 700000+index, fromID)),
		}
	default:
		actor, peer := "1000", fmt.Sprintf("8%011d", 1+rnd.Intn(99999999999))
		if sameUser {
			peer = "822222222222"
		}
		return Record{
			ID: fmt.Sprintf("dm:%s:%s:%d", actor, peer, index+1), Channel: "dm", Dir: "in",
			Session: actor + ":" + peer, FromID: peer, FromName: "私信观众",
			Text: text, Private: true, At: at, CollectMode: "poll",
			Meta: json.RawMessage(fmt.Sprintf(`{"recv_actor":"%s","msg_id":%d}`, actor, 900000+index)),
		}
	}
}
