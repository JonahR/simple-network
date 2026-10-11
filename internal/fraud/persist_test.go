package fraud

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// A rule built in the page, a block and its case survive a save and restore
// through JSON, and the restored block still declines.
func TestStateSurvivesRestart(t *testing.T) {
	c := newClock()
	e := NewEngine([]Rule{DefaultRules()[0]}, c.now)
	custom := DefaultRules()[0]
	custom.Name = "Built in the page"
	added, err := e.AddRule(custom)
	if err != nil {
		t.Fatal(err)
	}
	for range 6 {
		decline(e, req("4242424242424242", "RISK0001"), iso8583.RCInvalidCard)
	}
	if !decline(e, req("4000056655665556", "RISK0001"), iso8583.RCApproved) {
		t.Fatal("terminal not blocked")
	}

	data, err := json.Marshal(e.SaveState())
	if err != nil {
		t.Fatal(err)
	}
	var st EngineState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	restored := NewEngine(DefaultRules(), c.now) // A fresh start, as after a redeploy
	restored.RestoreState(st)

	before, after := e.Snapshot(), restored.Snapshot()
	if len(after.Rules) != 2 || after.Rules[1].ID != added.ID || after.Screened != before.Screened || after.Declined != before.Declined {
		t.Fatalf("rules or totals not restored: %+v", after)
	}
	if len(after.Blocks) != 1 || len(after.Cases) != len(before.Cases) || after.Cases[0].ID != before.Cases[0].ID {
		t.Fatalf("blocks %d, cases %+v", len(after.Blocks), after.Cases)
	}
	if !decline(restored, req("5555555555554444", "RISK0001"), iso8583.RCApproved) {
		t.Error("restored block did not decline")
	}
	// A new case continues the numbering instead of reusing an ID.
	restored.SetMode(added.ID, "off")
	if n := restored.SaveState().Seq; n < st.Seq {
		t.Errorf("case sequence went backward: %d < %d", n, st.Seq)
	}

	// An expired block is dropped on restore.
	c.advance(10 * time.Minute)
	late := NewEngine(DefaultRules(), c.now)
	late.RestoreState(st)
	if len(late.Snapshot().Blocks) != 0 {
		t.Error("expired block restored")
	}
}
