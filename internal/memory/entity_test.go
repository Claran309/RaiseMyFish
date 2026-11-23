package memory

import "testing"

func TestExtractRelationsOnlyExplicitPatterns(t *testing.T) {
	relations := ExtractRelations("小明是阿猫的室友", "live:1:1")
	if len(relations) != 1 || relations[0].Predicate != "roommate" || len(relations[0].SourceEventIDs) != 1 {
		t.Fatalf("明确句式未抽取：%+v", relations)
	}
	if relations[0].FromID == relations[0].ToID {
		t.Fatal("自环关系不应生成")
	}
	for _, text := range []string{"小明和阿猫一起玩", "他们是朋友吧", "小明是阿猫"} {
		if got := ExtractRelations(text, "live:1:2"); len(got) != 0 {
			t.Fatalf("模糊语义不应抽取：%q → %+v", text, got)
		}
	}
}

func TestEntityIDStableAndScoped(t *testing.T) {
	if EntityID("person", "阿猫") != EntityID("person", " 阿猫 ") {
		t.Fatal("同实体应稳定归一")
	}
	if EntityID("person", "阿猫") == EntityID("game", "阿猫") {
		t.Fatal("不同类型不应共享 ID")
	}
	if PersonEntity("1001", "阿猫").ID != EntityID("person", "阿猫") {
		t.Fatal("PersonEntity 与 EntityID 不一致")
	}
}
