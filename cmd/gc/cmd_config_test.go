package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestDoConfigShowMissingRemoteImportSuggestsInstall(t *testing.T) {
	clearGCEnv(t)
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GC_CITY_PATH", dir)
	if err := os.MkdirAll(".gc", 0o755); err != nil {
		t.Fatalf("MkdirAll(.gc): %v", err)
	}
	writeCityToml(t, dir, "[workspace]\nname = \"demo\"\n")
	writePackToml(t, dir, `[pack]
name = "demo"
schema = 1

[imports.tools]
source = "https://example.com/tools.git"
version = "^1.4"
`)

	var stdout, stderr bytes.Buffer
	code := doConfigShow(false, false, false, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected failure for missing remote import")
	}
	if got := stderr.String(); !bytes.Contains([]byte(got), []byte(`run "gc import install"`)) {
		t.Fatalf("stderr = %q, want install remediation", got)
	}
}

func TestConfigShowJSON(t *testing.T) {
	clearGCEnv(t)
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GC_CITY_PATH", dir)
	if err := os.MkdirAll(".gc", 0o755); err != nil {
		t.Fatalf("MkdirAll(.gc): %v", err)
	}
	writeCityToml(t, dir, "[workspace]\nname = \"demo\"\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"config", "show", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run(config show --json) = %d, stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var payload struct {
		SchemaVersion string `json:"schema_version"`
		CityPath      string `json:"city_path"`
		Warnings      []string
		Config        struct {
			Workspace struct {
				Name string
			}
		}
		Validation struct {
			OK       bool `json:"ok"`
			Warnings []string
			Errors   []string
		} `json:"validation"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if payload.SchemaVersion != "1" || payload.CityPath != dir || payload.Config.Workspace.Name != "demo" || !payload.Validation.OK {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Warnings == nil || payload.Validation.Warnings == nil || payload.Validation.Errors == nil {
		t.Fatalf("warnings/errors must be JSON arrays, got %+v", payload)
	}
	validateConfigShowJSONSchema(t, stdout.Bytes())
}

func TestConfigShowValidateJSONReturnsNonzeroForInvalidConfig(t *testing.T) {
	clearGCEnv(t)
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GC_CITY_PATH", dir)
	if err := os.MkdirAll(".gc", 0o755); err != nil {
		t.Fatalf("MkdirAll(.gc): %v", err)
	}
	writeCityToml(t, dir, "[workspace]\nname = \"demo\"\n\n[[rigs]]\nname = \"broken\"\n")

	var stdout, stderr bytes.Buffer
	code := run([]string{"config", "show", "--validate", "--json"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("run(config show --validate --json) = 0, want nonzero; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	var payload struct {
		Validation struct {
			OK     bool     `json:"ok"`
			Errors []string `json:"errors"`
		} `json:"validation"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if payload.Validation.OK || len(payload.Validation.Errors) == 0 {
		t.Fatalf("validation payload = %+v, want ok=false with errors", payload.Validation)
	}
	validateConfigShowJSONSchema(t, stdout.Bytes())
}

func validateConfigShowJSONSchema(t *testing.T, data []byte) {
	t.Helper()

	rawSchema, err := readBuiltinSchema([]string{"config", "show"}, jsonSchemaResultRole)
	if err != nil {
		t.Fatalf("read config show schema: %v", err)
	}
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawSchema))
	if err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("config-show.schema.json", schemaDoc); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	schema, err := compiler.Compile("config-show.schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if err := schema.Validate(doc); err != nil {
		t.Fatalf("payload does not match config show schema: %v\n%s", err, string(data))
	}
}

// TestConfigShowEmitsBindingName pins ch-sam5: BindingName is runtime-only
// (toml:"-" json:"-") so city.toml cannot set it, and gc config show adds
// it to its output itself, in the JSON per agent and named session and as
// a comment block in the TOML.
func TestConfigShowEmitsBindingName(t *testing.T) {
	cfg := &config.City{
		Workspace: config.Workspace{Name: "demo"},
		Agents: []config.Agent{
			{Name: "mayor", BindingName: "gastown"},
			{Name: "local"},
		},
		NamedSessions: []config.NamedSession{
			{Template: "mayor", BindingName: "gastown"},
			{Template: "local"},
		},
	}

	data, err := json.Marshal(configJSONForDisplay(cfg))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Workspace struct{ Name string }
		Agents    []struct {
			Name        string
			BindingName *string
		}
		NamedSessions []struct {
			Template    string
			BindingName *string
		}
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, data)
	}
	if got.Workspace.Name != "demo" {
		t.Fatalf("workspace name = %q, want demo (embedded city fields must survive)\n%s", got.Workspace.Name, data)
	}
	if len(got.Agents) != 2 || got.Agents[0].Name != "mayor" || got.Agents[0].BindingName == nil || *got.Agents[0].BindingName != "gastown" {
		t.Fatalf("agents = %s, want mayor with BindingName gastown", data)
	}
	if got.Agents[1].BindingName != nil {
		t.Fatalf("unbound agent carries BindingName %q, want it omitted", *got.Agents[1].BindingName)
	}
	if len(got.NamedSessions) != 2 || got.NamedSessions[0].BindingName == nil || *got.NamedSessions[0].BindingName != "gastown" {
		t.Fatalf("named sessions = %s, want mayor with BindingName gastown", data)
	}
	if got.NamedSessions[1].BindingName != nil {
		t.Fatalf("unbound named session carries BindingName %q, want it omitted", *got.NamedSessions[1].BindingName)
	}
	// The plain config marshal (the TOML path and every other caller) is
	// untouched: still no BindingName.
	if plain, err := json.Marshal(configForDisplay(cfg)); err != nil || strings.Contains(string(plain), "BindingName") {
		t.Fatalf("configForDisplay JSON gained BindingName (err=%v): %s", err, plain)
	}

	notes := renderBindingAnnotations(cfg)
	if !strings.Contains(notes, "#   agent         gastown.mayor") || !strings.Contains(notes, "binding gastown") ||
		!strings.Contains(notes, "#   named_session gastown.mayor") {
		t.Fatalf("binding annotations = %q", notes)
	}
	for _, line := range strings.Split(strings.TrimSuffix(notes, "\n"), "\n") {
		if !strings.HasPrefix(line, "#") {
			t.Fatalf("binding annotation line %q is not a TOML comment", line)
		}
	}
	if strings.Contains(notes, "local") {
		t.Fatalf("binding annotations name an unbound entry: %q", notes)
	}
	if renderBindingAnnotations(&config.City{Agents: []config.Agent{{Name: "local"}}}) != "" {
		t.Fatal("binding annotations for a city with no bindings must be empty")
	}

	// Config load still refuses to take a binding from city.toml.
	parsed, err := config.Parse([]byte("[workspace]\nname = \"demo\"\n\n[[agent]]\nname = \"a\"\nbinding_name = \"forged\"\nBindingName = \"forged\"\n"))
	if err == nil {
		for _, a := range parsed.Agents {
			if a.BindingName != "" {
				t.Fatalf("city.toml set BindingName = %q; it must stay runtime-only", a.BindingName)
			}
		}
	}
}
