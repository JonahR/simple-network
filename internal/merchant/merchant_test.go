package merchant

import (
	"regexp"
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
)

func TestCatalogIsValid(t *testing.T) {
	if len(Catalog) != 13 {
		t.Errorf("catalog has %d merchants, want 13", len(Catalog))
	}
	mids, tids := map[string]bool{}, map[string]bool{}
	for _, m := range Catalog {
		if !regexp.MustCompile(`^\d{15}$`).MatchString(m.ID) {
			t.Errorf("%s: merchant ID %q is not 15 digits (DE42)", m.Name, m.ID)
		}
		if mids[m.ID] {
			t.Errorf("%s: duplicate merchant ID %s", m.Name, m.ID)
		}
		mids[m.ID] = true
		if len(m.Terminals) == 0 {
			t.Errorf("%s: no terminals", m.Name)
		}
		for _, id := range m.Terminals {
			if len(id) != 8 {
				t.Errorf("%s: terminal ID %q is not 8 characters (DE41)", m.Name, id)
			}
			if tids[id] {
				t.Errorf("%s: terminal %s is also another merchant's", m.Name, id)
			}
			tids[id] = true
		}
		if _, ok := iso8583.MCCs[m.MCC]; !ok {
			t.Errorf("%s: MCC %s has no name in iso8583.MCCs", m.Name, m.MCC)
		}
		if _, err := time.LoadLocation(m.TimeZone); err != nil {
			t.Errorf("%s: time zone %q: %v", m.Name, m.TimeZone, err)
		}
		if m.DiscountBPS <= 0 || m.DiscountBPS > 1_000 {
			t.Errorf("%s: discount rate %d bps is out of range", m.Name, m.DiscountBPS)
		}
	}
}

func TestFromEnvOverridesOnlyTheDefault(t *testing.T) {
	env := map[string]string{"MERCHANT_NAME": "Test Cafe", "TERMINAL_ID": "TEST0001", "MERCHANT_DISCOUNT_BPS": "199"}
	got := FromEnv(func(k string) string { return env[k] })
	if got[0].Name != "Test Cafe" || got[0].Terminals[0] != "TEST0001" || got[0].DiscountBPS != 199 {
		t.Errorf("default merchant = %+v", got[0])
	}
	if got[0].ID != Catalog[0].ID || got[0].MCC != Catalog[0].MCC {
		t.Error("unset variables must keep the catalog's values")
	}
	if Catalog[0].Name != "Simple Coffee Co" || Catalog[0].Terminals[0] != "TERM0001" {
		t.Error("FromEnv modified the shared catalog")
	}
	if len(got) != len(Catalog) || got[1].Name != Catalog[1].Name {
		t.Error("the other merchants must be unchanged")
	}
}
