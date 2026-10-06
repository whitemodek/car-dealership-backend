package leads

import "testing"

func TestLeadTransitions(t *testing.T) {
	for _, pair := range [][2]string{{"new", "qualified"}, {"closed", "new"}, {"qualified", "contacted"}, {"new", "invalid"}} {
		if AllowedTransition(pair[0], pair[1]) {
			t.Fatalf("invalid transition %v", pair)
		}
	}
	for _, pair := range [][2]string{{"new", "contacted"}, {"contacted", "qualified"}, {"qualified", "closed"}, {"new", "closed"}} {
		if !AllowedTransition(pair[0], pair[1]) {
			t.Fatalf("rejected transition %v", pair)
		}
	}
}
func TestContactRequiresConsentAndPhone(t *testing.T) {
	for _, c := range []Contact{{Name: "Customer", Phone: "+79990000000"}, {Name: "Customer", Phone: "-----", Consent: true}, {Name: "Customer", Phone: "+79990000000", Email: "invalid", Consent: true}} {
		if c.Validate() == nil {
			t.Fatal("invalid contact accepted")
		}
	}
}
