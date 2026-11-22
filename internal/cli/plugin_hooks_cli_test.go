package cli

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// magpie plugin says the hooks a request-hook package exports: a package
// that only gives a translation plan has no onRequest, and the list named
// one it did not have.
func TestPluginListSaysTheHooksAPackageExports(t *testing.T) {
	bun := testenv.Bun(t)
	groupsHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("MAGPIE_PLUGIN_MARKET", "off")
	t.Setenv("NO_COLOR", "1")
	t.Cleanup(plugin.Settle)
	pkg := filepath.Join(t.TempDir(), "translate")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"translate","version":"0.1.0","type":"module","magpie":{"hooks":"./translate.hooks.js"}}`
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	hook := "export async function onTranslate(ctx) {\n  return null\n}\n"
	if err := os.WriteFile(filepath.Join(pkg, "translate.hooks.js"), []byte(hook), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Add(context.Background(), pkg); err != nil {
		t.Fatal(err)
	}
	out, err := stdoutOf(t, func() error { return listPlugins(context.Background(), false) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "async gateway hook  onTranslate") {
		t.Errorf("the hooks the package exports aren't named:\n%s", out)
	}
	if strings.Contains(out, "async gateway hook  onRequest") {
		t.Errorf("the package is said to have an onRequest it does not have:\n%s", out)
	}
	// the JSON says them too, so the window can name them
	js, err := stdoutOf(t, func() error { return listPlugins(context.Background(), true) })
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`"gatewayHooks":\s*\[\s*"onTranslate"\s*\]`).MatchString(js) {
		t.Errorf("--json has no hooks:\n%s", js)
	}
}
