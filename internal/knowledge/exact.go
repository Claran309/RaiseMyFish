package knowledge

// 本文件提供「精确词表」：知识库里所有可作为规范的词（title / keywords / tags）。
//
// 为什么需要它：梗白学习判断「这个梗是不是已经收录过」时，
// 不能用 Recall（相关性检索）——语义相近的**不同**梗也会被召回，
// 于是新梗被误判成重复而永久丢失（M09）。
// 精确去重必须只看规范词是否字面相等，与相关性检索彻底解耦。
//
// 边界：
//   - 只读快照，不加锁、不改状态；
//   - 返回原始文本（未规范化），由调用方决定比较口径；
//   - 不包含 summary/usage：它们是解释性正文，不是「规范词」。

import (
	"context"
	"errors"
)

// ExactTermSource 是精确去重能力（FileReader 与 HybridReader 均实现）。
type ExactTermSource interface {
	ExactTerms(ctx context.Context) ([]string, error)
}

// ExactTerms 返回知识库快照里所有规范词（title、keywords）。
//
// 不含 tags：tags 是分类标签（「弹幕梗」「直播间」这类通用词），
// 拿它做精确去重会把与标签同名的候选误判为「已收录」。
// 同一词可能出现多次（不同条目共享 keyword），调用方用集合去重即可。
func (r *FileReader) ExactTerms(ctx context.Context) ([]string, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("知识Reader不能为空")
	}
	st := r.current()
	terms := make([]string, 0, len(st.entries)*2)
	for _, e := range st.entries {
		terms = append(terms, e.title)
		terms = append(terms, e.keywords...)
	}
	return terms, nil
}

// ExactTerms 转发到底层 FileReader：混合召回只多一层语义，不改变精确词表。
func (h *HybridReader) ExactTerms(ctx context.Context) ([]string, error) {
	if h == nil || h.base == nil {
		return nil, errors.New("混合召回底层Reader不能为空")
	}
	return h.base.ExactTerms(ctx)
}
