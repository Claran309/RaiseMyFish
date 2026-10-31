package knowledge

import "errors"

// SnapshotInfo 只含构造时不可变快照的元数据，不枚举条目/正文/路径。
type SnapshotInfo struct {
	Revision string
	Entries  int
}

// SnapshotInfo 无I/O；源文件后续变动不会改变已经构造的Reader信息。
func (r *FileReader) SnapshotInfo() (SnapshotInfo, error) {
	if r == nil {
		return SnapshotInfo{}, errors.New("知识Reader不能为空")
	}
	return SnapshotInfo{Revision: r.revision, Entries: len(r.entries)}, nil
}
