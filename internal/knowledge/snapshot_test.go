package knowledge

import "testing"

func TestSnapshotInfoRejectsNilReader(t *testing.T) {
	var reader *FileReader
	if _, err := reader.SnapshotInfo(); err == nil {
		t.Fatal("nil Reader伪装空快照")
	}
}
