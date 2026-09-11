package labstatus

import "testing"

func TestOutputSchemaRequiresClosedCandidateShape(t *testing.T) {
	t.Parallel()
	schema := OutputSchema()
	for _, field := range []string{"outcome", "records", "abstentions", "limitations", "abstention_reason"} {
		if schema.Properties[field] == nil {
			t.Errorf("OutputSchema() missing property %q", field)
		}
		if !contains(schema.Required, field) {
			t.Errorf("OutputSchema() does not require %q", field)
		}
	}
	record := schema.Properties["records"].Items
	for _, field := range []string{"epistemic_class", "status", "scope", "citation", "does_not_establish"} {
		if record.Properties[field] == nil || !contains(record.Required, field) {
			t.Errorf("record schema must define and require %q", field)
		}
	}
	if record.Properties["citation"].Properties["exact_quote"] != nil {
		t.Error("model citation schema must not let the model supply exact_quote")
	}
}
