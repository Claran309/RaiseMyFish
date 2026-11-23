package knowledgeeval

// 本文件是 B8 的离线对照实验：在关键词 Reader 之上加一层 BM25 打分与 RRF 融合，
// 用同一批 100 条用例量化「是否值得上线」。不改变生产检索路径。

import (
	"context"
	"math"
	"sort"
	"strings"
	"unicode"

	"RaiseMyFish/internal/knowledge"
)

// BM25Options 是实验参数；k1/b 取常见默认值。
type BM25Options struct {
	K1 float64
	B  float64
	// RRFK 是 RRF 平滑常数。
	RRFK float64
}

func (o BM25Options) resolve() BM25Options {
	if o.K1 <= 0 {
		o.K1 = 1.5
	}
	if o.B <= 0 {
		o.B = 0.75
	}
	if o.RRFK <= 0 {
		o.RRFK = 60
	}
	return o
}

// RRFReader 把关键词候选与 BM25 候选用 RRF 融合；只用于实验。
type RRFReader struct {
	Base    knowledge.Reader
	Options BM25Options
	// MaxItems 限制候选数；0 表示用基座返回的全部。
	MaxItems int
}

// Recall 先取基座关键词结果，再用 BM25 重排同一批候选。
func (r RRFReader) Recall(ctx context.Context, query knowledge.Query) (knowledge.Context, error) {
	base, err := r.Base.Recall(ctx, query)
	if err != nil {
		return knowledge.Context{}, err
	}
	if len(base.Items) < 2 {
		return base, nil
	}
	if r.MaxItems > 0 && len(base.Items) > r.MaxItems {
		base.Items = base.Items[:r.MaxItems]
	}
	opts := r.Options.resolve()
	terms := tokenize(query.Text)
	if len(terms) == 0 {
		return base, nil
	}
	// 用候选正文构造迷你语料，计算 IDF/长度归一。
	docs := make([][]string, len(base.Items))
	for i, item := range base.Items {
		docs[i] = tokenize(item.Text)
	}
	avgLen := 0.0
	for _, doc := range docs {
		avgLen += float64(len(doc))
	}
	avgLen /= float64(len(docs))
	df := map[string]int{}
	for _, doc := range docs {
		seen := map[string]bool{}
		for _, token := range doc {
			if !seen[token] {
				seen[token] = true
				df[token]++
			}
		}
	}
	scores := make([]float64, len(base.Items))
	for i, doc := range docs {
		freq := map[string]int{}
		for _, token := range doc {
			freq[token]++
		}
		score := 0.0
		for _, term := range terms {
			tf := float64(freq[term])
			if tf == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(docs))-float64(df[term])+0.5)/(float64(df[term])+0.5))
			denom := tf + opts.K1*(1-opts.B+opts.B*float64(len(doc))/math.Max(avgLen, 1))
			score += idf * (tf * (opts.K1 + 1)) / denom
		}
		scores[i] = score
	}
	// RRF：关键词名次 + BM25 名次。
	keywordRank := make(map[int]int, len(base.Items))
	for i := range base.Items {
		keywordRank[i] = i + 1
	}
	order := make([]int, len(base.Items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return scores[order[i]] > scores[order[j]] })
	bm25Rank := make(map[int]int, len(order))
	for rank, idx := range order {
		bm25Rank[idx] = rank + 1
	}
	fused := make([]struct {
		idx   int
		score float64
	}, len(base.Items))
	for i := range base.Items {
		fused[i] = struct {
			idx   int
			score float64
		}{i, 1/(opts.RRFK+float64(keywordRank[i])) + 1/(opts.RRFK+float64(bm25Rank[i]))}
	}
	sort.SliceStable(fused, func(i, j int) bool {
		if fused[i].score == fused[j].score {
			return fused[i].idx < fused[j].idx
		}
		return fused[i].score > fused[j].score
	})
	items := make([]knowledge.Item, 0, len(fused))
	for _, entry := range fused {
		items = append(items, base.Items[entry.idx])
	}
	base.Items = items
	return base, nil
}

// tokenize 做最小分词：ASCII 词 + 中文二元组，够离线实验用。
func tokenize(text string) []string {
	var tokens []string
	var word strings.Builder
	var cjk []rune
	flushWord := func() {
		if word.Len() > 0 {
			tokens = append(tokens, strings.ToLower(word.String()))
			word.Reset()
		}
	}
	flushCJK := func() {
		for i := 0; i+1 < len(cjk); i++ {
			tokens = append(tokens, string(cjk[i:i+2]))
		}
		if len(cjk) == 1 {
			tokens = append(tokens, string(cjk))
		}
		cjk = cjk[:0]
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			word.WriteRune(r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return tokens
}
