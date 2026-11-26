package config

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSessionDerivePathsOrder(t *testing.T) {
	var d SessionDeriveConfig
	src := "body_path: metadata.conversation_id\nbody_paths: [message.contextId, params.message.contextId]\n"
	if err := yaml.Unmarshal([]byte(src), &d); err != nil {
		t.Fatal(err)
	}
	want := []string{"metadata.conversation_id", "message.contextId", "params.message.contextId"}
	if got := d.Paths(); !reflect.DeepEqual(got, want) {
		t.Errorf("Paths() = %v, want %v", got, want)
	}
	if got := (SessionDeriveConfig{BodyPath: "a.b"}).Paths(); !reflect.DeepEqual(got, []string{"a.b"}) {
		t.Errorf("body_path only: Paths() = %v", got)
	}
	if got := (SessionDeriveConfig{BodyPaths: []string{"c.d"}}).Paths(); !reflect.DeepEqual(got, []string{"c.d"}) {
		t.Errorf("body_paths only: Paths() = %v", got)
	}
	if got := defaults().Session.DeriveFrom.Paths(); len(got) != 0 {
		t.Errorf("default Paths() = %v, want none", got)
	}
}

func bodyPathErrors(c *Config) []string {
	var fields []string
	for _, e := range c.Validate().Errors {
		if strings.HasPrefix(e.Field, "session.derive_from.body_paths") {
			fields = append(fields, e.Field)
		}
	}
	return fields
}

func TestValidateBodyPaths(t *testing.T) {
	c := defaults()
	c.Session.DeriveFrom.BodyPaths = []string{"message.contextId", "params.message.contextId", "id"}
	if got := bodyPathErrors(c); len(got) != 0 {
		t.Errorf("valid paths rejected: %v", got)
	}

	c = defaults()
	c.Session.DeriveFrom.BodyPaths = []string{"ok.path", "", " a.b", "a..b", ".a", "a."}
	want := []string{
		"session.derive_from.body_paths[1]",
		"session.derive_from.body_paths[2]",
		"session.derive_from.body_paths[3]",
		"session.derive_from.body_paths[4]",
		"session.derive_from.body_paths[5]",
	}
	if got := bodyPathErrors(c); !reflect.DeepEqual(got, want) {
		t.Errorf("body_paths errors = %v, want %v", got, want)
	}
}
