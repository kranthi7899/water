package connectors_test

import (
	"encoding/json"
	"strings"
	"testing"

	"water/internal/connectors"
	"water/internal/connectors/fake"
)

func TestRegistryResolvesAndRejectsDuplicates(t *testing.T) {
	r, err := connectors.NewRegistry(fake.NewCalendar(), fake.NewMail(), fake.NewDocs())
	if err != nil {
		t.Fatal(err)
	}
	c, f, ok := r.Lookup("fake_mail.send_email")
	if !ok || c.Name() != "fake_mail" || f.Level != "A" {
		t.Fatalf("lookup: %v %+v %v", c, f, ok)
	}
	if _, _, ok := r.Lookup("fake_mail"); ok {
		t.Fatal("connector name resolved as a function")
	}
	if _, err := connectors.NewRegistry(fake.NewMail(), fake.NewMail()); err == nil {
		t.Fatal("duplicate connector accepted")
	}
}

func TestSchemaValidation(t *testing.T) {
	r, _ := connectors.NewRegistry(fake.NewMail())
	_, f, _ := r.Lookup("fake_mail.send_email")
	ok := map[string]any{"to": []any{"a@b.c"}, "subject": "s", "body": "b"}
	if err := f.Schema.Validate(ok); err != nil {
		t.Fatal(err)
	}
	bad := []map[string]any{
		{"to": []any{"a@b.c"}, "subject": "s"},
		{"to": "a@b.c", "subject": "s", "body": "b"},
		{"to": []any{1}, "subject": "s", "body": "b"},
		{"to": []any{"a@b.c"}, "subject": "s", "body": "b", "bcc": "x"},
	}
	for _, args := range bad {
		if err := f.Schema.Validate(args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	b, _ := json.Marshal(f.Schema)
	if !strings.Contains(string(b), `"additionalProperties":false`) || !strings.Contains(string(b), `"type":"object"`) {
		t.Fatalf("schema JSON: %s", b)
	}
}
