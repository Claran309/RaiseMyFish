package personaeval

import "encoding/json"

// newScorerV3 只修已获授权的称呼判定及低风险词法误报；保留安全/长度红线。
func newScorerV3() (*Scorer, error) {
	previous, err := NewScorerVersion("persona-lexical-v2")
	if err != nil {
		return nil, err
	}
	var book Rulebook
	if err := json.Unmarshal(previous.Snapshot(), &book); err != nil {
		return nil, err
	}
	book.Version = "persona-lexical-v3"
	for i := range book.Rules {
		r := &book.Rules[i]
		switch r.Side + ":" + r.Label {
		case "forbid:喊主人":
			r.None = nil
			// 直接呼语/身份认定才拦截；不以第三人称片段给整句豁免。
			r.Patterns = []string{
				`(?:^|[，,。！？!?；;\s]|好的|好哒|好啊|好呀|遵命|收到|知道了|谢谢|多谢|亲爱的)主人(?:大人)?(?:[，,。！？!?~～\s]|$|你|您|我|咱|早安|晚安|你好|在吗|呢|呀|啊)`,
				`(?:^|[，,。！？!?；;\n])\s*(?:你|您)(?:就是|是)(?:我的|本鱼的)?主人(?:[，,。！？!?\s]|$)`,
			}
		case "expect:情绪":
			r.Any = append(r.Any, "害羞", "不好意思", "被夸", "飘了", "急了", "心疼", "委屈", "得意", "泪目", "破防", "哦牛批", "我勒个", "无所谓我会出手", "顶啥", "随口一说")
		case "expect:拒绝":
			r.Any = append(r.Any, "不外借", "不外传", "不公开", "不提供", "不方便", "别打听", "问不出来", "不敢乱给", "不敢乱发", "不敢替", "哪能随便", "是能随便", "不归鱼管", "不归本鱼管")
		case "expect:兜底":
			r.Any = append(r.Any, "没跟上", "看不懂", "没看懂", "没看明白", "听不懂", "没听懂", "不知道", "脑子空", "人话")
		case "expect:接梗":
			r.Any = append(r.Any, "换本鱼", "早淹死", "我勒个", "无所谓我会出手", "牛魔", "有点东西")
		case "expect:认身份":
			r.AnyWhenInputContains = []string{"你是谁", "你是什么", "什么身份", "是不是AI", "是不是 AI", "什么东西", "什么鱼", "叫啥", "叫什么", "你的名字"}
		case "forbid:列表":
			r.None = nil
			r.MaxNewlines = nil
			r.Patterns = append(r.Patterns, `(?s)首先.{0,80}其次`, `(?s)第一[，、：:].{0,80}第二[，、：:]`, `(?m)^\s*[一二三四五六七八九十]+[、.）)]`)
		}
	}
	return compileScorer(book)
}
