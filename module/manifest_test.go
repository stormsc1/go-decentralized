package module

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// TestManifestsMatchTheSpec checks every module.yaml in the repo against
// the spec's schema of manifests, and parses it.
func TestManifestsMatchTheSpec(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile("../spec/module.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	modules, _ := filepath.Glob("../modules/*/module.yaml")
	builtins, _ := filepath.Glob("../internal/*/*.module.yaml")
	paths := append(modules, builtins...)
	if len(paths) < 5 {
		t.Fatalf("found only %v", paths)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := yaml.Unmarshal(data, &v); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		j, _ := json.Marshal(v)
		doc, _ := jsonschema.UnmarshalJSON(bytes.NewReader(j))
		if err := schema.Validate(doc); err != nil {
			t.Errorf("%s doesn't match the spec: %v", path, err)
		}
		if _, err := ParseManifest(data); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestManifestRejectsEventsNamedLikeCapabilities(t *testing.T) {
	_, err := ParseManifest([]byte(`
name: chat
version: 1.0.0
capabilities:
  - name: message
events:
  - name: message
`))
	if err == nil {
		t.Fatal("parsed a capability and an event with the same ref")
	}
}

func TestManifestRejectsEntitiesInKVStores(t *testing.T) {
	_, err := ParseManifest([]byte(`
name: chat
version: 1.0.0
stores:
  - name: seen
    type: kv
    entities: [{name: receipt}]
`))
	if err == nil {
		t.Fatal("parsed a key-value store with entity types")
	}
}

func TestManifestRejectsNonObjectSchemas(t *testing.T) {
	_, err := ParseManifest([]byte(`
name: bad
version: 1.0.0
capabilities:
  - name: count
    output: {type: integer}
`))
	if err == nil {
		t.Fatal("parsed a capability whose result isn't an object")
	}
}
