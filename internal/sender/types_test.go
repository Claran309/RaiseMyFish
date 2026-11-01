package sender

import "testing"

func TestEvidenceOriginCannotBeSwapped(t *testing.T) {
	for _, v := range []Verdict{{Outcome: OutcomeAccepted, MsgID: "id", Verified: true, Evidence: "history_id", Origin: OriginPost}, {Outcome: OutcomeAccepted, MsgID: "id", Verified: true, Evidence: "receipt_id", Origin: OriginVerify}} {
		if err := v.Validate(); err == nil {
			t.Fatal("跨来源证据被接受")
		}
	}
}
