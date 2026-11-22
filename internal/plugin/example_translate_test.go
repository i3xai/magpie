package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exampleDir: the example package the translation's reference points at.
// It is a real package, not a snippet in a document: magpie installs it as
// it stands, and its hook is asked for a plan.
func exampleDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "examples", "prompt-translation"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the example package is not there: %v", err)
	}
	return dir
}

// The example gives a plan the gateway can translate with: a model to ask,
// two languages and a prompt. A plan that says less is no plan, and the
// user would see no reason why nothing was translated.
func TestTheTranslationExampleGivesAPlan(t *testing.T) {
	sandbox(t)
	dir := exampleDir(t)
	if _, err := Add(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	run := GatewayBegin(t.Context(), []byte(`{"note":"original"}`), GatewayContext{Protocol: "chat", Model: "claude-opus-4-5"})
	if run == nil {
		t.Fatal("no run")
	}
	defer run.Close()
	if run.Translate == nil {
		t.Fatal("the example gave no plan, so a request is not translated at all")
	}
	p := run.Translate
	if p.Model == "" || p.From == "" || p.To == "" || p.System == "" {
		t.Fatalf("a plan the gateway cannot use: %+v", p)
	}
	if p.From != "zh" || p.To != "en" {
		t.Fatalf("the languages it translates between: %+v", p)
	}
	if p.FromScript != "cjk" || p.ToScript != "latin" {
		t.Fatalf("the scripts it knows: %+v", p)
	}
	if !strings.Contains(p.System, "{from}") || !strings.Contains(p.System, "{to}") {
		t.Fatalf("the prompt does not name the languages: %q", p.System)
	}
}

// The example is shipped whole: a manifest that points at a hooks file
// that is there, a hook that exports what the gateway asks for, and a
// readme that says how it goes in.
func TestTheTranslationExampleIsShippedWhole(t *testing.T) {
	dir := exampleDir(t)
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Name   string `json:"name"`
		Magpie struct {
			Hooks   string         `json:"hooks"`
			Options map[string]any `json:"options"`
		} `json:"magpie"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	if pkg.Name == "" {
		t.Fatal("the package has no name")
	}
	if pkg.Magpie.Hooks == "" {
		t.Fatal("the package declares no hooks file")
	}
	hooks := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(pkg.Magpie.Hooks, "./")))
	body, err := os.ReadFile(hooks)
	if err != nil {
		t.Fatalf("the hooks file the manifest names is not there: %v", err)
	}
	if !strings.Contains(string(body), "onTranslate") {
		t.Fatal("the hooks file exports no onTranslate")
	}
	// The model's to ask comes from the package's own options, so a user
	// names the one they have.
	for _, k := range []string{"model", "from", "to", "system"} {
		if _, ok := pkg.Magpie.Options[k]; !ok {
			t.Errorf("the package has no %q option", k)
		}
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("the example has no readme: %v", err)
	}
	for _, want := range []string{pkg.Name, "magpie plugin add", "onTranslate"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("the readme does not say %q", want)
		}
	}
}
