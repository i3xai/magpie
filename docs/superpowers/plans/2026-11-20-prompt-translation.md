# Prompt translation between models — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Chinese prompt reaches a model in English, translated by a model A the user names, and the model's reply reaches the client in Chinese, translated by the same model A.

**Architecture:** The core provides two primitives and a plugin provides the policy. The request-side primitive translates the request IR's text parts before the provider body is built, so every client protocol and every fallback attempt is covered by one change. The response-side primitive is an encoder that holds the reply's text, translates it, and releases it with the events that came after it, in the order they came. The plugin's `onTranslate` hook gives a plan — the model A, the two languages and the prompt — and the gateway asks model A itself, in process, so the translation is counted in the usage rows and cannot recurse into itself.

**Tech Stack:** Go 1.26, the gateway's request IR (`internal/gateway/ir.go`), the plugin host's Bun runtime (`internal/plugin/host.js`), `httptest` for the fake model in the tests.

## Global Constraints

- The design this plan implements: `docs/superpowers/specs/2026-11-20-prompt-translation-design.md`.
- House rules for this repo: `docs/code-standards.md`. A commit names the area it changes and what the user now sees, and its body says how it was verified and which test fails without the change.
- Subsystem references are maintained in the same change: `docs/subsystems/README.md`.
- Go: write `any`, not `interface{}`. A comment says what the code is for, in a sentence or two, and names functions rather than line numbers.
- Tests never reach the network. The model A is a fake provider on `httptest`; the plugin is a hooks file in a temporary HOME.
- The gateway's tests run under the package's own `TestMain`, which isolates HOME and the caches.
- Every failure leaves the request as it was: an untranslated text is better than a half-translated conversation.
- The reply's events reach the client in the order they came. The text is translated before the events that followed it are released.
- Only `Part.Kind == Text` is translated. Not `System`, not `ToolCall`'s arguments, not `ToolResult`, not `Thinking`, not `Search`, not `Advice`.
- A new usage kind is registered in `internal/usage/purpose.go` and the GUI's `purposes.js` is regenerated with `go generate ./internal/usage` from the repo root.
- Every GUI string goes through `t()` and gets its translations in `internal/gui/assets/i18n.js`, in Chinese, English, Traditional Chinese, Japanese and German.

---

### Task 1: The setting a translation is given

**Files:**
- Modify: `internal/settings/settings.go` (the field beside `ClassifyTimeout`; the constants and `TranslateWait` beside `ClassifyWait`; the check in `saveOwn`)
- Test: `internal/settings/translate_timeout_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `settings.Settings.TranslateTimeout int`, `settings.TranslateTimeoutDefault`, `settings.TranslateTimeoutMost`, `func (s Settings) TranslateWait() time.Duration`.

- [ ] **Step 1: Write the failing test**

Create `internal/settings/translate_timeout_test.go`:

```go
package settings

import (
	"testing"
	"time"
)

// A translation is given from 1 to 300 seconds, and 0, which is what a
// settings.json without it reads as, is the default: 30, room for a long
// reply to be translated.
func TestTranslateTimeoutKeptAndBounded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if w := Load().TranslateWait(); w != 30*time.Second {
		t.Fatalf("unset: %v", w)
	}
	for _, n := range []int{1, 45, 300} {
		if err := Save(Settings{TranslateTimeout: n}); err != nil {
			t.Fatalf("%d: %v", n, err)
		}
		if s := Load(); s.TranslateTimeout != n || s.TranslateWait() != time.Duration(n)*time.Second {
			t.Fatalf("%d read back as %d, %v", n, s.TranslateTimeout, s.TranslateWait())
		}
	}
	for _, n := range []int{-1, 301, 600} {
		if Save(Settings{TranslateTimeout: n}) == nil {
			t.Fatalf("%d seconds accepted", n)
		}
	}
	if Load().TranslateTimeout != 300 {
		t.Fatal("a refused value overwrote the saved one")
	}
	// a settings.json written by hand past the bounds still waits within them
	if w := (Settings{TranslateTimeout: 600}).TranslateWait(); w != 300*time.Second {
		t.Fatalf("600 waits %v", w)
	}
	if w := (Settings{TranslateTimeout: -3}).TranslateWait(); w != 30*time.Second {
		t.Fatalf("-3 waits %v", w)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags nogui ./internal/settings -run TestTranslateTimeoutKeptAndBounded -count=1`
Expected: FAIL to compile — `Settings.TranslateWait undefined`.

- [ ] **Step 3: Add the field**

In `internal/settings/settings.go`, after the `ClassifyTimeout` field:

```go
	// TranslateTimeout is how long, in seconds, a translation is given to
	// answer: from 1 to TranslateTimeoutMost, 0 for TranslateTimeoutDefault.
	// TranslateWait reads it.
	TranslateTimeout int `json:"translateTimeout,omitempty"`
```

- [ ] **Step 4: Add the constants and the reader**

In `internal/settings/settings.go`, after `ClassifyWait`:

```go
// TranslateTimeoutDefault is the seconds a translation is given when
// TranslateTimeout is 0, and TranslateTimeoutMost the most it may be set to:
// a whole reply is longer than a classifier's answer, and a long one takes
// its model a while.
const (
	TranslateTimeoutDefault = 30
	TranslateTimeoutMost    = 300
)

// TranslateWait is how long a translation is given to answer:
// TranslateTimeout's seconds, TranslateTimeoutDefault's when it is 0 (or
// below), and no more than TranslateTimeoutMost's when settings.json was
// written by hand past it.
func (s Settings) TranslateWait() time.Duration {
	n := s.TranslateTimeout
	if n < 1 {
		n = TranslateTimeoutDefault
	}
	return time.Duration(min(n, TranslateTimeoutMost)) * time.Second
}
```

- [ ] **Step 5: Refuse a value out of bounds**

In `saveOwn` in `internal/settings/settings.go`, after the `ClassifyTimeout` check:

```go
	if s.TranslateTimeout < 0 || s.TranslateTimeout > TranslateTimeoutMost {
		return fmt.Errorf("a translation is given from 1 to %d seconds, or 0 for %d, not %d", TranslateTimeoutMost, TranslateTimeoutDefault, s.TranslateTimeout)
	}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test -tags nogui ./internal/settings -run TestTranslateTimeoutKeptAndBounded -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/settings/settings.go internal/settings/translate_timeout_test.go
git commit -m "settings: a translation is given from 1 to 300 seconds, 30 by default

A translation of a whole reply is slower than a classifier's answer, so it
gets its own wait rather than the classifier's 15 seconds.

Verified: go test -tags nogui ./internal/settings -run
TestTranslateTimeoutKeptAndBounded. The test fails without the change:
Settings has no TranslateWait to read."
```

---

### Task 2: The plugin's plan

The plugin gives the gateway the plan it translates with: the model, the two languages, the scripts and the prompt. The gateway asks the model itself, so the plan is a policy and not a call.

**Files:**
- Modify: `internal/plugin/gateway.go` (`TranslatePlan`, `GatewayRun.Translate`, `GatewayBegin`'s answer)
- Modify: `internal/plugin/host.js` (the hook is exported and called; a hook with only `onTranslate` is allowed)
- Test: `internal/plugin/gateway_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `plugin.TranslatePlan` with `Model`, `From`, `To`, `FromName`, `ToName`, `System`, `FromScript`, `ToScript`; `func (p *TranslatePlan) Usable() bool`; `GatewayRun.Translate *TranslatePlan`.

- [ ] **Step 1: Write the failing test**

Add to `internal/plugin/gateway_test.go`:

```go
// A request hook's onTranslate gives the gateway the plan it translates
// with: the model, the languages, the scripts and the prompt.
func TestGatewayHookTranslatePlan(t *testing.T) {
	sandbox(t)
	gatewayFixture(t, `export async function onTranslate(ctx) { return { model: "gemini-3-flash", from: "zh", to: "en", fromName: "Chinese", toName: "English", fromScript: "cjk", toScript: "latin", system: "Translate {from} into {to}." } }`)
	run := GatewayBegin(t.Context(), []byte(`{"note":"original"}`), GatewayContext{Protocol: "chat", Model: "claude-opus-4-5"})
	if run == nil {
		t.Fatal("no run")
	}
	defer run.Close()
	if run.Translate == nil {
		t.Fatal("no translate plan")
	}
	if run.Translate.Model != "gemini-3-flash" || run.Translate.From != "zh" || run.Translate.To != "en" {
		t.Fatalf("plan: %+v", run.Translate)
	}
	if run.Translate.FromScript != "cjk" || run.Translate.ToScript != "latin" {
		t.Fatalf("scripts: %+v", run.Translate)
	}
	if run.Translate.System != "Translate {from} into {to}." {
		t.Fatalf("system: %q", run.Translate.System)
	}
}

// A hook that gives no plan leaves the gateway with none, and a plan that
// says too little to translate with is no plan.
func TestGatewayHookTranslatePlanRefused(t *testing.T) {
	sandbox(t)
	gatewayFixture(t, `export async function onTranslate() { return null }`)
	run := GatewayBegin(t.Context(), []byte(`{"note":"original"}`), GatewayContext{Protocol: "chat"})
	if run == nil {
		t.Fatal("no run")
	}
	defer run.Close()
	if run.Translate != nil {
		t.Fatalf("a plan that was never given: %+v", run.Translate)
	}
	gatewayFixture(t, `export async function onTranslate() { return { model: "gemini-3-flash" } }`)
	run = GatewayBegin(t.Context(), []byte(`{"note":"original"}`), GatewayContext{Protocol: "chat"})
	if run == nil {
		t.Fatal("no run")
	}
	defer run.Close()
	if run.Translate != nil {
		t.Fatalf("a plan without a language or a prompt: %+v", run.Translate)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags nogui ./internal/plugin -run 'TestGatewayHookTranslatePlan' -count=1`
Expected: FAIL to compile — `GatewayRun.Translate undefined`.

- [ ] **Step 3: Add the plan type**

In `internal/plugin/gateway.go`, after `GatewayContext`:

```go
// TranslatePlan is what a request hook's onTranslate gives the gateway: the
// model to translate with, the two languages, and the prompt the texts go
// through. The gateway asks the model itself, so a plan is a policy and not
// a call: a hook that called back into magpie would have its own request
// offered to onTranslate again.
type TranslatePlan struct {
	// Model is the model to ask, as magpie names it.
	Model string `json:"model"`
	// From and To are the two languages, as the plugin names them.
	From string `json:"from"`
	To   string `json:"to"`
	// FromName and ToName are the languages as the prompt names them; they
	// fill the prompt's {from} and {to}.
	FromName string `json:"fromName"`
	ToName   string `json:"toName"`
	// FromScript and ToScript say how a language is written, so the gateway
	// knows a text that is already in the language wanted: "cjk" for one
	// written with Han ideographs and CJK marks, "latin" for one written
	// with the Latin alphabet. An empty one is a language the gateway does
	// not know, and a text in it is translated always.
	FromScript string `json:"fromScript"`
	ToScript   string `json:"toScript"`
	// System is the prompt the texts go through. Its {from} and {to} are
	// filled with the language names.
	System string `json:"system"`
}

// Usable tells whether the plan says enough to translate with: a model to
// ask, two languages and a prompt. A plan that does not is no plan.
func (p *TranslatePlan) Usable() bool {
	return p != nil && p.Model != "" && p.From != "" && p.To != "" && p.System != ""
}
```

- [ ] **Step 4: Carry the plan on the run**

In `internal/plugin/gateway.go`, in `GatewayRun`:

```go
type GatewayRun struct {
	Body json.RawMessage
	// Translate is the plan the hook's onTranslate gave, nil when the hook
	// gave none.
	Translate *TranslatePlan
```

In `GatewayBegin`, the answer:

```go
	var out struct {
		ID        string          `json:"id"`
		Token     string          `json:"token"`
		Body      json.RawMessage `json:"body"`
		Translate *TranslatePlan  `json:"translate"`
	}
```

and the run it returns:

```go
	if !out.Translate.Usable() {
		out.Translate = nil
	}
	return &GatewayRun{Body: out.Body, Translate: out.Translate, h: h, id: out.ID, token: out.Token}
```

- [ ] **Step 5: Ask the hook in the host**

In `internal/plugin/host.js`, where the hooks are collected (the line that pushes `onRequest`, `onResponse` and `onSignIn`):

```js
    if (r.gateway) gatewayHooks.push({ spec: p.spec, options: p.options ?? {}, onRequest: r.gateway.onRequest,
      onResponse: r.gateway.onResponse, onSignIn: r.gateway.onSignIn, onTranslate: r.gateway.onTranslate,
```

and the check that a hook exports something (the line that throws "magpie.hooks exports no onRequest"):

```js
      if (typeof gateway.onRequest !== "function" && typeof gateway.onSignIn !== "function" && typeof gateway.onTranslate !== "function") throw new Error("magpie.hooks exports no onRequest")
```

In `gatewayBegin`, after the `onRequest` chain and before the run is returned:

```js
  // The translate plan: the first hook that gives one wins. The gateway
  // asks the model itself, so the plan is a policy and not a call.
  let translate = null
  for (const h of gatewayHooks) {
    if (total.aborted) break
    if (typeof h.onTranslate !== "function") continue
    const ctl = new AbortController()
    const timer = setTimeout(() => ctl.abort(), gatewayTimeout(h))
    const ownSignal = AbortSignal.any([total, ctl.signal])
    try {
      const ctx = { ...p.context, options: structuredClone(h.options), state: {}, signal: ownSignal, report: () => {} }
      const plan = await bounded(Promise.resolve().then(() => h.onTranslate(ctx)), ownSignal)
      if (plan && typeof plan === "object" && !Array.isArray(plan)) {
        translate = {
          model: typeof plan.model === "string" ? plan.model : "",
          from: typeof plan.from === "string" ? plan.from : "",
          to: typeof plan.to === "string" ? plan.to : "",
          fromName: typeof plan.fromName === "string" ? plan.fromName : "",
          toName: typeof plan.toName === "string" ? plan.toName : "",
          fromScript: typeof plan.fromScript === "string" ? plan.fromScript : "",
          toScript: typeof plan.toScript === "string" ? plan.toScript : "",
          system: typeof plan.system === "string" ? plan.system : "",
        }
        break
      }
    } catch {
      if (!signal.aborted) send({ event: "log", level: "warn", message: `${h.spec}: gateway translate hook failed; keeping request` })
    } finally {
      clearTimeout(timer)
      ctl.abort()
    }
  }
```

and the return:

```js
  return { id: run.id, token: run.token, body, translate }
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test -tags nogui ./internal/plugin -run 'TestGatewayHookTranslatePlan' -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/plugin/gateway.go internal/plugin/host.js internal/plugin/gateway_test.go
git commit -m "plugin: a request hook can give the gateway a translation plan

A hook's onTranslate names the model to translate with, the two languages
and the prompt. The gateway asks the model itself, so the plan is a policy
and not a call: a hook that called back into magpie would have its own
request offered to onTranslate again.

Verified: go test -tags nogui ./internal/plugin -run
'TestGatewayHookTranslatePlan'. The test fails without the change: a
GatewayRun has no Translate to read."
```

---

### Task 3: The translator

The translator asks a model for the translations of texts it is given, and keeps what it translated so a text is translated once.

**Files:**
- Create: `internal/gateway/translate.go`
- Modify: `internal/gateway/classify.go` (`askChat` gains a context and an agent of its own)
- Test: `internal/gateway/translate_test.go`

**Interfaces:**
- Consumes: `plugin.TranslatePlan` (Task 2), `settings.Settings.TranslateWait()` (Task 1).
- Produces: `func (s *Server) translatorFor(r *http.Request) *translator`; `func withTranslatePlan(ctx context.Context, plan *plugin.TranslatePlan) context.Context`; `func (e *translator) translate(ctx context.Context, texts []string, wantTo bool) []string`; `func (e *translator) translateRequestText(ctx context.Context, req *Request)`; `const TranslateAgent`; `func (s *Server) askChatCtx(ctx context.Context, wait time.Duration, held access.Identity, model, agent string, body []byte) (string, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/gateway/translate_test.go`:

```go
package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
)

// translateModel is a model that answers with the texts it was given, in
// the other script, so a test can see what was asked of it and what came
// back.
type translateModel struct {
	mu    sync.Mutex
	asked [][]string
}

func (m *translateModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &req)
	var texts []string
	for _, msg := range req.Messages {
		if msg.Role == "user" {
			_ = json.Unmarshal([]byte(msg.Content), &texts)
		}
	}
	m.mu.Lock()
	m.asked = append(m.asked, texts)
	m.mu.Unlock()
	// the answer is in the other script, as a translation's is
	answer := "translated"
	if len(texts) > 0 && !holdsCJK(texts[0]) {
		answer = "翻译"
	}
	arr, _ := json.Marshal([]string{answer})
	content, _ := json.Marshal(string(arr))
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + string(content) + `}}]}`))
}

// calls is how many times the model was asked.
func (m *translateModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.asked)
}

// aPlan is a plan a plugin would give: Chinese to English, with the model
// named as the model to ask. Each test names its own model, so the
// translations one test made are not another's.
func aPlan(model string) *plugin.TranslatePlan {
	return &plugin.TranslatePlan{
		Model: model, From: "zh", To: "en",
		FromName: "Chinese", ToName: "English",
		FromScript: "cjk", ToScript: "latin",
		System: "Translate {from} into {to}.",
	}
}

// aTranslator is the translator a plan gives a request, with the model A
// served by a fake provider.
func aTranslator(t *testing.T, s *Server, plan *plugin.TranslatePlan) *translator {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
	r = r.WithContext(withTranslatePlan(r.Context(), plan))
	tr := s.translatorFor(r)
	if tr == nil {
		t.Fatal("no translator")
	}
	return tr
}

// A plan's texts are asked of the model the plan names, and the texts come
// back translated.
func TestTranslatorAsksTheModelsPlan(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a1", "ka", []string{"m"}, m)
	s := New()
	tr := aTranslator(t, s, aPlan("a1/m"))
	out := tr.translate(context.Background(), []string{"你好"}, true)
	if len(out) != 1 || out[0] != "translated" {
		t.Fatalf("translated: %q", out)
	}
	if m.calls() != 1 {
		t.Fatalf("calls: %d", m.calls())
	}
	if len(m.asked[0]) != 1 || m.asked[0][0] != "你好" {
		t.Fatalf("asked for: %q", m.asked[0])
	}
}

// A text that is already in the language wanted is left as it is, and no
// call is made for it.
func TestTranslatorLeavesTheLanguageWantedAlone(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a2", "ka", []string{"m"}, m)
	s := New()
	tr := aTranslator(t, s, aPlan("a2/m"))
	out := tr.translate(context.Background(), []string{"hello"}, true)
	if out[0] != "hello" {
		t.Fatalf("translated: %q", out)
	}
	if m.calls() != 0 {
		t.Fatalf("a call was made: %d", m.calls())
	}
}

// A text that was translated before is translated no more, in either
// direction: the reply's Chinese finds the English it was made from.
func TestTranslatorKeepsBothEnds(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a3", "ka", []string{"m"}, m)
	s := New()
	tr := aTranslator(t, s, aPlan("a3/m"))
	// the reply goes from English to Chinese
	tr.translate(context.Background(), []string{"hello"}, false)
	// and the Chinese it produced finds the English it was made from
	out := tr.translate(context.Background(), []string{"翻译"}, true)
	if out[0] != "hello" {
		t.Fatalf("the reverse lookup gave: %q", out)
	}
	if m.calls() != 1 {
		t.Fatalf("calls: %d", m.calls())
	}
}

// An answer that is not the texts asked for leaves every text of the call as
// it was: half a translation is worse than none.
func TestTranslatorRefusesAWrongAnswer(t *testing.T) {
	fresh(t)
	serveOn(t, "a4", "ka", []string{"m"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"[\"one\"]"}}]}`))
	}))
	s := New()
	tr := aTranslator(t, s, aPlan("a4/m"))
	out := tr.translate(context.Background(), []string{"你好", "世界"}, true)
	if out[0] != "你好" || out[1] != "世界" {
		t.Fatalf("translated: %q", out)
	}
}

// A model's failure leaves the texts as they were.
func TestTranslatorKeepsTheTextsWhenTheModelFails(t *testing.T) {
	fresh(t)
	serveOn(t, "a5", "ka", []string{"m"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	s := New()
	tr := aTranslator(t, s, aPlan("a5/m"))
	out := tr.translate(context.Background(), []string{"你好"}, true)
	if out[0] != "你好" {
		t.Fatalf("translated: %q", out)
	}
}

// The cache lives for the gateway's life and every request reaches it, so
// two translations at once may not write over each other. Run this under
// -race.
func TestTranslatorKeepsTheCacheWholeWhenTwoTranslateAtOnce(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a6", "ka", []string{"m"}, m)
	s := New()
	tr := aTranslator(t, s, aPlan("a6/m"))
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 10 {
				tr.translate(context.Background(), []string{fmt.Sprintf("你好%d-%d", i, j)}, true)
			}
		}(i)
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags nogui ./internal/gateway -run 'TestTranslator' -count=1`
Expected: FAIL to compile — `translator` undefined.

- [ ] **Step 3: Write the translator**

Create `internal/gateway/translate.go`:

```go
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// TranslateAgent is the User-Agent of the translations magpie makes: the
// gateway's own call to a model, told apart in the usage rows from the
// turn's own call.
const TranslateAgent = "magpie-translate/1"

// translator translates the texts of one request's exchange, with the plan a
// plugin's onTranslate gave.
type translator struct {
	s    *Server
	plan *plugin.TranslatePlan
	held access.Identity
	// ctx is the request's own context: the reply's half of the exchange
	// reads it (the encoder that carries the reply's texts takes the
	// translator's context as its own), so it is kept here for it.
	ctx context.Context
}

// translatorFor is the translator of a request, from the plan its plugin
// gave, or nil when there is none: nothing is translated then.
func (s *Server) translatorFor(r *http.Request) *translator {
	if r == nil {
		return nil
	}
	plan := translatePlanOf(r.Context())
	if !plan.Usable() {
		return nil
	}
	return &translator{s: s, plan: plan, held: access.Caller(r.Context()), ctx: r.Context()}
}

type translateKey struct{}

// withTranslatePlan keeps the plan a plugin gave on the request's context, so
// the reply's half reads the same plan the request's half used.
func withTranslatePlan(ctx context.Context, plan *plugin.TranslatePlan) context.Context {
	if !plan.Usable() {
		return ctx
	}
	return context.WithValue(ctx, translateKey{}, plan)
}

// translatePlanOf is the plan a plugin gave for a request, nil when it gave
// none.
func translatePlanOf(ctx context.Context) *plugin.TranslatePlan {
	plan, _ := ctx.Value(translateKey{}).(*plugin.TranslatePlan)
	return plan
}

// translateRequestText translates the texts of a request's messages that are
// in another language than the one wanted. The system prompt, the tools, the
// tool calls, the tool results and the reasoning are left as they are: a
// tool's arguments are code, and a result is what a command said.
func (e *translator) translateRequestText(ctx context.Context, req *Request) {
	var parts []*Part
	var texts []string
	for i := range req.Messages {
		m := &req.Messages[i]
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		for j := range m.Parts {
			p := &m.Parts[j]
			if p.Kind != Text {
				continue
			}
			parts = append(parts, p)
			texts = append(texts, p.Text)
		}
	}
	if len(texts) == 0 {
		return
	}
	out := e.translate(ctx, texts, true)
	for i, p := range parts {
		p.Text = out[i]
	}
}

// translate translates the texts into the language wanted, and returns them
// in the order they were given. wantTo asks for the plan's "to" language,
// which is the request's texts; otherwise for its "from" language, which is
// the reply's.
//
// A failure leaves every text of the call as it was: a half-translated
// conversation is worse than an untranslated one.
func (e *translator) translate(ctx context.Context, texts []string, wantTo bool) []string {
	if len(texts) == 0 {
		return texts
	}
	script := e.plan.ToScript
	if !wantTo {
		script = e.plan.FromScript
	}
	pair := translateCache.pair(e.plan)
	out := make([]string, len(texts))
	var ask []int
	for i, t := range texts {
		out[i] = t
		if t == "" {
			continue
		}
		// the cache first: a text it knows is a text that was translated
		// before, and what it was made from is known exactly. The script
		// below is a guess, and the cache is not.
		if v, ok := pair.lookup(t, wantTo); ok {
			out[i] = v
			continue
		}
		if !needsTranslation(t, script) {
			continue
		}
		ask = append(ask, i)
	}
	if len(ask) == 0 {
		return out
	}
	wanted := make([]string, len(ask))
	for i, at := range ask {
		wanted[i] = out[at]
	}
	translated, ok := e.ask(ctx, wanted, wantTo)
	if !ok {
		return out
	}
	for i, at := range ask {
		out[at] = translated[i]
		if wantTo {
			pair.remember(wanted[i], translated[i])
		} else {
			pair.remember(translated[i], wanted[i])
		}
	}
	return out
}

// ask asks the model the plan names for the translations of the texts, and
// tells whether it answered with them.
func (e *translator) ask(ctx context.Context, texts []string, wantTo bool) ([]string, bool) {
	body := translateBody(e.plan, texts, wantTo)
	answer, err := e.s.askChatCtx(ctx, settings.Shared().TranslateWait(), e.held, e.plan.Model, TranslateAgent, body)
	if err != nil {
		return nil, false
	}
	return translateAnswer(answer, len(texts))
}

// translateBody is the Chat request asking a model for the translations of
// texts: the prompt the plugin gave, with its placeholders filled, and the
// texts as a JSON array.
func translateBody(plan *plugin.TranslatePlan, texts []string, wantTo bool) []byte {
	from, to := plan.FromName, plan.ToName
	if !wantTo {
		from, to = to, from
	}
	system := strings.ReplaceAll(plan.System, "{from}", from)
	system = strings.ReplaceAll(system, "{to}", to)
	list, _ := json.Marshal(texts)
	req := map[string]any{
		"model": plan.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": string(list)},
		},
		"stream": false,
	}
	b, _ := json.Marshal(req)
	return b
}

// translateAnswer reads the model's answer: the texts it was asked to
// translate, as a JSON array, in the order they were asked for. An answer
// that is not JSON, or whose count is not the count asked for, is refused.
func translateAnswer(answer string, want int) ([]string, bool) {
	s := strings.TrimSpace(answer)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	var out []string
	if json.Unmarshal([]byte(s), &out) != nil || len(out) != want {
		return nil, false
	}
	for _, t := range out {
		if strings.TrimSpace(t) == "" {
			return nil, false
		}
	}
	return out, true
}

// holdsCJK tells whether a text holds a Han ideograph or a CJK mark.
func holdsCJK(text string) bool {
	for _, r := range text {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF, // CJK Unified Ideographs
			r >= 0x3400 && r <= 0x4DBF, // CJK Extension A
			r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
			r >= 0x3000 && r <= 0x303F, // CJK symbols and punctuation
			r >= 0xFF00 && r <= 0xFFEF: // fullwidth forms
			return true
		}
	}
	return false
}

// needsTranslation tells whether a text is in another language than the one
// wanted: a CJK language's text holds a Han ideograph or a CJK mark, a Latin
// one's holds none. A language whose script the core does not know is
// translated always, since nothing tells its text apart.
func needsTranslation(text string, script string) bool {
	switch script {
	case "cjk":
		return !holdsCJK(text)
	case "latin":
		return holdsCJK(text)
	}
	return true
}

// translateCache holds the translations made, so a text that was translated
// before is translated no more. Both ends are kept: a text a translation
// produced finds the text it was made from, so a conversation's history
// costs no call to be sent to a model in the other language.
var translateCache = &translations{byPair: map[string]*translationPair{}}

type translations struct {
	mu     sync.Mutex
	byPair map[string]*translationPair
	// order is the pairs in the order they were made, and head the
	// oldest one still in it.
	order []string
	head  int
}

// translationPair is the cache of one plan's two languages, model and
// prompt. Its lock guards the entries and the order they were kept in.
type translationPair struct {
	mu      sync.Mutex
	entries map[string]translationEntry
	order   []string
	head    int
}

// translationEntry is one text of a pair and the text on the other side of
// it. to says which side of the pair the text is on.
type translationEntry struct {
	text string
	to   bool
}

// translationPairMost is the most texts a pair keeps; the oldest go first.
const translationPairMost = 2048

// translationPairsMost is the most pairs of languages, model and prompt the
// cache keeps; the oldest go first. A plugin that varies its prompt from
// one request to the next must not grow the map for the life of the process.
const translationPairsMost = 64

// pair is the cache of a plan's two languages, model and prompt, made once.
func (t *translations) pair(plan *plugin.TranslatePlan) *translationPair {
	key := plan.From + "\x00" + plan.To + "\x00" + plan.Model + "\x00" + plan.System
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.byPair[key]
	if !ok {
		p = &translationPair{entries: map[string]translationEntry{}}
		t.byPair[key] = p
		t.order = append(t.order, key)
		// the oldest pair goes first, as the oldest text of a pair does
		for len(t.order)-t.head > translationPairsMost {
			delete(t.byPair, t.order[t.head])
			t.head++
		}
		if t.head > 1024 {
			t.order = append(t.order[:0], t.order[t.head:]...)
			t.head = 0
		}
	}
	return p
}

// lookup is the text on the side wanted, for a text of the pair, and whether
// the pair knows it.
func (p *translationPair) lookup(text string, wantTo bool) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[translateHash(text)]
	if !ok {
		return "", false
	}
	if e.to == wantTo {
		return text, true
	}
	return e.text, true
}

// remember keeps both ends of a translation: the text it was given and the
// text it produced, each on the side of the pair it is on.
func (p *translationPair) remember(from, to string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.put(from, translationEntry{text: to, to: false})
	p.put(to, translationEntry{text: from, to: true})
}

func (p *translationPair) put(text string, e translationEntry) {
	key := translateHash(text)
	// a text put again is the newest text of the pair: the copy of its
	// key the order kept behind is taken out, so the window never drops
	// the live entry when it passes that copy.
	if at := p.indexOf(key); at >= 0 {
		p.order = append(p.order[:at], p.order[at+1:]...)
	}
	p.entries[key] = e
	p.order = append(p.order, key)
	for len(p.order)-p.head > translationPairMost {
		delete(p.entries, p.order[p.head])
		p.head++
	}
	if p.head > 1024 {
		p.order = append(p.order[:0], p.order[p.head:]...)
		p.head = 0
	}
}

// indexOf is where a key sits in the order the texts were kept in, from the
// oldest one still in it, or -1 for none.
func (p *translationPair) indexOf(key string) int {
	for i := p.head; i < len(p.order); i++ {
		if p.order[i] == key {
			return i
		}
	}
	return -1
}

// translateHash is a text's key in a pair's cache.
func translateHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Let the gateway ask a model with an agent of its own**

In `internal/gateway/classify.go`, split `askChat` so a translation can name its own agent and keep the caller's cancellation. `askChat` keeps its own behaviour exactly; `askChatCtx` is what both use:

```go
// askChat asks a decision provider's model through its own API, and
// any other model through the gateway itself, as a client would: which of
// intents the message is and, when effort, how hard the turn is, each in a
// call of its own, at once.
func (s *Server) askChat(wait time.Duration, held access.Identity, model string, body []byte) (string, error) {
	return s.askChatCtx(context.Background(), wait, held, model, RouterAgent, body)
}

// askChatCtx is askChat with the caller's context and the agent the call is
// told apart by: the router's own calls and a translation's are two kinds of
// a row in the ledger.
func (s *Server) askChatCtx(ctx context.Context, wait time.Duration, held access.Identity, model, agent string, body []byte) (string, error) {
	// what the call is called in the usage log's cause: the router's own
	// call is a decision model's, a translation's a translation's.
	what := "translation"
	if agent == RouterAgent {
		what = "decision model"
	}
	ctx, cancel := context.WithTimeoutCause(ctx, wait, sideTimedOut{what, wait})
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://magpie/v1/chat/completions", nil)
	if err != nil {
		return "", err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", agent)
	if len(held.Accounts) > 0 {
		r = r.WithContext(access.WithIdentity(r.Context(), held))
	}
	w := httptest.NewRecorder()
	s.serve(w, r, provider.Chat, body)
	if ctx.Err() != nil {
		if agent == RouterAgent {
			// the router's call names the model that gave no answer, as
			// it did before a translation had an agent of its own.
			if wait < classifyTimeout() {
				return "", cutShort(model, wait, classifyTimeout())
			}
			return "", fmt.Errorf("%s gave no answer in %s", model, wait)
		}
		return "", fmt.Errorf("the translation asked of %s gave no answer in %s", model, wait)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		return "", fmt.Errorf("%s: %d, not an answer", model, w.Code)
	}
	if w.Code >= 300 || out.Error != nil {
		msg := http.StatusText(w.Code)
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		return "", chatFailed{fmt.Errorf("%s: %s", model, msg), w.Code, w.Body.Bytes()}
	}
	if len(out.Choices) == 0 {
		return "", answerError{fmt.Errorf("%s gave no answer", model)}
	}
	return out.Choices[0].Message.Content, nil
}
```

The original `askChat` body moves into `askChatCtx` unchanged apart from the two lines that name the agent and take the context.

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test -tags nogui ./internal/gateway -run 'TestTranslator' -count=1`
Expected: PASS.

If the texts come back untranslated, the model was not reached: check that the provider serves the model the plan names (`a/ta`) and that `fresh(t)` left the providers in place.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/translate.go internal/gateway/translate_test.go internal/gateway/classify.go
git commit -m "gateway: a translator asks a model for texts in another language

The texts go as a JSON array and the answer is read as one, so a text
that was merged or dropped leaves the whole batch untranslated. Both ends
of a translation are kept, so a text a translation produced finds the text
it was made from: a conversation's history costs no call to be sent to a
model in the other language.

Verified: go test -tags nogui ./internal/gateway -run TestTranslator. The
test fails without the change: there is no translator to ask."
```

---

### Task 4: The request's texts reach the model in English

**Files:**
- Modify: `internal/gateway/gateway.go` (`translate`: translate the request's texts)
- Modify: `internal/gateway/middleware.go` (`gatewayHooks` keeps the plan; `serveAgent` follows the new signature)
- Modify: `internal/gateway/codex_backend.go` (the other caller follows the new signature)
- Modify: `internal/gateway/gateway_hook_test.go` (the two callers of `gatewayHooks` follow the new signature)
- Test: `internal/gateway/translate_test.go`

**Interfaces:**
- Consumes: `translatorFor`, `translateRequestText` (Task 3), `plugin.GatewayRun.Translate` (Task 2).
- Produces: `func gatewayHooks(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte) (http.ResponseWriter, *http.Request, []byte, func())`.

- [ ] **Step 1: Write the failing test**

Add to `internal/gateway/translate_test.go`, and add `"github.com/yetone/magpie/internal/provider"` to its imports:

Each test names a provider of its own, so the translations one test made are
not another's: the translator's own tests already take a1 to a10, and the
gateway's other tests take b and c.

```go
// A request's Chinese prompt reaches the provider in English. The reply's
// own side is Task 5's, and is not asserted here.
func TestTranslationThroughTheGateway(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a11", "ka", []string{"m"}, m)
	var asked string
	serveOn(t, "a12", "kb", []string{"m"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		asked = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	s := New()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"a12/m","messages":[{"role":"user","content":"帮我写个函数"}]}`))
	req = req.WithContext(withTranslatePlan(req.Context(), aPlan("a11/m")))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(asked, "translated") {
		t.Fatalf("the provider was asked: %s", asked)
	}
	if strings.Contains(asked, "帮我写个函数") {
		t.Fatalf("the provider was asked in Chinese: %s", asked)
	}
}

// A request magpie makes of itself is not translated: it goes through serve
// and not through the hooks, so it has no plan.
func TestMagpiesOwnRequestIsNotTranslated(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a15", "ka", []string{"m"}, m)
	s := New()
	r := httptest.NewRequest("POST", "http://magpie/v1/chat/completions", nil)
	r.Header.Set("User-Agent", RouterAgent)
	rec := httptest.NewRecorder()
	s.serve(rec, r, provider.Chat, []byte(`{"model":"a15/m","messages":[{"role":"user","content":"帮我写个函数"}]}`))
	if m.calls() != 0 {
		t.Fatalf("a translation was made: %d", m.calls())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags nogui ./internal/gateway -run 'TestTranslationThroughTheGateway|TestMagpiesOwnRequestIsNotTranslated' -count=1`
Expected: FAIL — the provider is asked with the Chinese text.

- [ ] **Step 3: Keep the plan on the request**

In `internal/gateway/middleware.go`, change `gatewayHooks` to return the request:

```go
func gatewayHooks(w http.ResponseWriter, r *http.Request, from provider.Protocol, body []byte) (http.ResponseWriter, *http.Request, []byte, func()) {
	if !plugin.HasGateway() {
		return w, r, body, func() {}
	}
	...
	run := plugin.GatewayBegin(r.Context(), body, info)
	waited()
	if run == nil {
		return w, r, body, func() {}
	}
	if run.Translate != nil {
		r = r.WithContext(withTranslatePlan(r.Context(), run.Translate))
	}
	w, done := gatewayResponse(w, r, from, run.Body, run)
	return w, r, run.Body, func() { done(); run.Close() }
}
```

and in `serveAgent`:

```go
	w, r, body, hooked := gatewayHooks(w, r, from, body)
```

and in `internal/gateway/codex_backend.go`:

```go
		w, r, body, hooked = gatewayHooks(w, r, provider.Responses, body)
```

and in `internal/gateway/gateway_hook_test.go`, the two places that call
`gatewayHooks` themselves, which follow the new signature as well: the first
keeps the request it is given (the plan is on it), the second throws it away
(it does not read the request after the call):

```go
				w, r, b, done := gatewayHooks(w, r, provider.Chat, []byte(`{"model":"fake/m1","origin":"local","messages":[{"role":"user","content":"hello"}]}`))
```

```go
		_, _, b, done := gatewayHooks(w, req, provider.Chat, []byte(`{"model":"fake/m1","messages":[{"role":"user","content":"hello"}]}`))
```

- [ ] **Step 4: Translate the request's texts**

In `internal/gateway/gateway.go`, in `translate`, after the request is read and before it is used:

```go
	// the texts the plugin's translator wants in another language, before
	// the body is built for the provider (translate.go)
	if tr := s.translatorFor(r); tr != nil {
		tr.translateRequestText(r.Context(), request)
	}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test -tags nogui ./internal/gateway -run 'TestTranslationThroughTheGateway|TestMagpiesOwnRequestIsNotTranslated' -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/gateway.go internal/gateway/middleware.go internal/gateway/codex_backend.go internal/gateway/gateway_hook_test.go internal/gateway/translate_test.go
git commit -m "gateway: a request's texts reach the model in the language asked

The texts are translated on the request IR, so every client protocol, every
provider and every fallback attempt is covered by one change, and a tool's
arguments and a tool's output are left as they are.

Verified: go test -tags nogui ./internal/gateway -run
'TestTranslationThroughTheGateway|TestMagpiesOwnRequestIsNotTranslated'.
The test fails without the change: the provider is asked with the Chinese
text."
```

---

### Task 5: The reply's text reaches the client in Chinese

**Files:**
- Create: `internal/gateway/translate_reply.go`
- Modify: `internal/gateway/gateway.go` (`encoder` takes the translator; a whole reply's text is translated)
- Modify: `internal/gateway/continuation.go` (`streamTranslated` passes the translator)
- Modify: `internal/gateway/relay_status.go` (`relayStatus` passes the translator; a whole reply's text is translated)
- Modify: `internal/gateway/claude_subscription.go` (the Claude bridge's encoder is given no translator)
- Modify: the gateway's own tests that build an encoder — `aistudio_thinking_test.go`, `automode_test.go`, `call_item_ids_test.go`, `format_fence_test.go`, `namespace_test.go`, `phase_test.go`, `response_id_test.go`, `search_call_ids_test.go`, `toolsearch_test.go` — the new argument is `nil` in each of them, as none of those tests is about a translation
- Test: `internal/gateway/translate_test.go`

**Interfaces:**
- Consumes: `translatorFor` (Task 3), `translate` (Task 3).
- Produces: `func encoder(proto provider.Protocol, w *sseWriter, r *Request, tr *translator, usage ...*Usage) streamEncoder`; `func (e *translator) translateResult(ctx context.Context, res *Result)`.

- [ ] **Step 1: Write the failing test**

Add to `internal/gateway/translate_test.go`:

```go
// recordedEncoder keeps the events it is given, in the order they came.
type recordedEncoder struct {
	events []Event
}

func (e *recordedEncoder) event(ev Event) { e.events = append(e.events, ev) }
func (e *recordedEncoder) finish()        {}
func (e *recordedEncoder) keepalive()      {}

// A reply's text reaches the client in the language asked, and the events
// that came after it keep their place.
func TestTranslatedReplyKeepsItsOrder(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a16", "ka", []string{"m"}, m)
	s := New()
	tr := aTranslator(t, s, aPlan("a16/m"))
	rec := &recordedEncoder{}
	enc := &translateEncoder{streamEncoder: rec, tr: tr, ctx: tr.ctx}
	enc.event(Event{Kind: KText, Text: "done"})
	enc.event(Event{Kind: KToolStart, ID: "t1", Name: "read"})
	enc.event(Event{Kind: KStop, Stop: "tool"})
	enc.finish()
	if len(rec.events) != 3 {
		t.Fatalf("events: %+v", rec.events)
	}
	if rec.events[0].Kind != KText || rec.events[0].Text != "翻译" {
		t.Fatalf("first event: %+v", rec.events[0])
	}
	if rec.events[1].Kind != KToolStart {
		t.Fatalf("second event: %+v", rec.events[1])
	}
	if rec.events[2].Kind != KStop {
		t.Fatalf("third event: %+v", rec.events[2])
	}
}

// A whole reply's text is translated before it is rendered, so a client
// that asked for no stream is given the same language a streaming client is.
func TestTranslatedWholeReply(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a17", "ka", []string{"m"}, m)
	s := New()
	tr := aTranslator(t, s, aPlan("a17/m"))
	res := Result{Parts: []Part{{Kind: Text, Text: "done"}}}
	tr.translateResult(context.Background(), &res)
	if res.Parts[0].Text != "翻译" {
		t.Fatalf("the reply: %+v", res.Parts[0])
	}
}

// A reply the model gave in English reaches the client in Chinese, through
// the whole gateway: the request's own side is Task 4's, and is already
// there by now.
func TestTranslatedReplyThroughTheGateway(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a13", "ka", []string{"m"}, m)
	serveOn(t, "a14", "kc", []string{"m"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	s := New()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"a14/m","messages":[{"role":"user","content":"帮我写个函数"}]}`))
	req = req.WithContext(withTranslatePlan(req.Context(), aPlan("a13/m")))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "翻译") {
		t.Fatalf("the client was told: %s", rec.Body.String())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags nogui ./internal/gateway -run 'TestTranslatedReply|TestTranslatedWholeReply' -count=1`
Expected: FAIL to compile — `translateEncoder` undefined.

- [ ] **Step 3: Write the reply's side**

Create `internal/gateway/translate_reply.go`:

```go
package gateway

import (
	"context"
	"strings"
)

// translateEncoder holds a reply's text while it is translated, and lets it,
// and the events that came after it, go on in the order they came.
//
// The text is held rather than translated a piece at a time: a piece at a
// time is a call a piece, and a piece's translation is a piece's worth of
// sense. The reply's other events wait with it, so the client reads the
// answer in the order the model gave it: Codex reads the Responses
// protocol's output items in order, and a text that came after a function
// call would show out of order.
type translateEncoder struct {
	streamEncoder
	tr    *translator
	ctx   context.Context
	text  strings.Builder
	after []Event
	done  bool
}

func (e *translateEncoder) event(ev Event) {
	if e.done {
		e.streamEncoder.event(ev)
		return
	}
	if ev.Kind == KText {
		e.text.WriteString(ev.Text)
		return
	}
	if e.text.Len() == 0 {
		// nothing to translate yet: the event goes on as it came
		e.streamEncoder.event(ev)
		return
	}
	e.after = append(e.after, ev)
	e.release()
}

// release translates the text held and lets it, and the events held with it,
// go on in the order they came.
func (e *translateEncoder) release() {
	if e.done {
		return
	}
	e.done = true
	text := e.text.String()
	e.text.Reset()
	if text != "" {
		out := e.tr.translate(e.ctx, []string{text}, false)
		e.streamEncoder.event(Event{Kind: KText, Text: out[0]})
	}
	for _, ev := range e.after {
		e.streamEncoder.event(ev)
	}
	e.after = nil
}

func (e *translateEncoder) finish() {
	e.release()
	e.streamEncoder.finish()
}

// translateResult translates the text parts of a whole reply, so a client
// that asked for no stream is given the same language a streaming client is.
func (e *translator) translateResult(ctx context.Context, res *Result) {
	var parts []*Part
	var texts []string
	for i := range res.Parts {
		p := &res.Parts[i]
		if p.Kind != Text {
			continue
		}
		parts = append(parts, p)
		texts = append(texts, p.Text)
	}
	if len(texts) == 0 {
		return
	}
	out := e.translate(ctx, texts, false)
	for i, p := range parts {
		p.Text = out[i]
	}
}
```

- [ ] **Step 4: Wrap the encoder**

In `internal/gateway/gateway.go`, in `encoder`:

```go
func encoder(proto provider.Protocol, w *sseWriter, r *Request, tr *translator, usage ...*Usage) streamEncoder {
	enc := makeEncoder(proto, w, r)
	if len(usage) > 0 && usage[0] != nil {
		enc = &usageEncoder{streamEncoder: enc, usage: usage[0]}
	}
	// a model's whole floats in a call's arguments reach the client as
	// integers (grok_integral.go, #1431, #3152), outside usageEncoder,
	// which reads its encoder's reply id
	enc = &wholeEncoder{streamEncoder: enc}
	if r.CR {
		enc = &crEncoder{streamEncoder: enc}
	}
	if r.wantsJSON() {
		// a fenced JSON answer reaches a structured-output client as the
		// JSON it asked for (format_fence.go)
		enc = &fenceEncoder{streamEncoder: enc}
	}
	if tr != nil {
		// the text the client is told is translated, and the events that
		// followed it wait for the translation (translate_reply.go)
		enc = &translateEncoder{streamEncoder: enc, tr: tr, ctx: tr.ctx}
	}
	return enc
}
```

- [ ] **Step 5: Pass the translator where the encoder is built**

In `internal/gateway/continuation.go`, in `streamTranslated`:

```go
	enc := encoder(from, sw, request, s.translatorFor(r), u)
```

In `internal/gateway/relay_status.go`, in `relayStatus`:

```go
		enc := encoder(from, sw, req, s.translatorFor(r), usage)
```

In `internal/gateway/claude_subscription.go`, where the encoder is built for the Claude bridge, which is a turn magpie runs itself:

```go
		enc := encoder(from, sw, req, nil, usage)
```

and in the gateway's own tests that build an encoder, the translator is
`nil`, as it is in the Claude bridge's: those tests are not about a
translation, and an encoder without a translator is the one every reply had
before this change.

```go
	enc := encoder(provider.Responses, newSSEWriter(rec), req, nil)
```

- [ ] **Step 6: Translate a whole reply**

In `internal/gateway/gateway.go`, in `translate`, before the reply is rendered:

```go
	if tr := s.translatorFor(r); tr != nil {
		tr.translateResult(r.Context(), &res2)
	}
	out := renderUsage(from, res2, request, u)
```

In `internal/gateway/relay_status.go`, before the whole reply is written:

```go
	if tr := s.translatorFor(r); tr != nil {
		tr.translateResult(r.Context(), &res)
	}
	w.Header().Set("Content-Type", "application/json")
```

- [ ] **Step 7: Run the test to verify it passes**

Run: `go test -tags nogui ./internal/gateway -run 'TestTranslatedReply|TestTranslatedWholeReply' -count=1`
Expected: PASS.

- [ ] **Step 8: Run the gateway's tests**

Run: `go test -tags nogui ./internal/gateway -count=1`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/gateway/translate_reply.go internal/gateway/gateway.go internal/gateway/continuation.go internal/gateway/relay_status.go internal/gateway/claude_subscription.go internal/gateway/translate_test.go internal/gateway/aistudio_thinking_test.go internal/gateway/automode_test.go internal/gateway/call_item_ids_test.go internal/gateway/format_fence_test.go internal/gateway/namespace_test.go internal/gateway/phase_test.go internal/gateway/response_id_test.go internal/gateway/search_call_ids_test.go internal/gateway/toolsearch_test.go
git commit -m "gateway: a reply's text reaches the client in the language asked

The text is held while it is translated and the events that followed it
wait with it, so the client reads the answer in the order the model gave
it. A whole reply is translated before it is rendered, so a client that
asked for no stream is given the same language a streaming client is.

Verified: go test -tags nogui ./internal/gateway -run TestTranslatedReply,
and the gateway's own tests. The test fails without the change: the client
is told the English text."
```

---

### Task 6: The translation is told apart in the ledger

**Files:**
- Modify: `internal/gateway/codex_backend.go` (`callKind` learns the translation's kind)
- Modify: `internal/usage/purpose.go` (the kind and its purpose)
- Modify: `internal/gui/assets/purposes.js` (regenerated)
- Modify: `internal/gui/assets/i18n.js` (the name in every language)
- Test: `internal/gateway/translate_test.go`

**Interfaces:**
- Consumes: `TranslateAgent` (Task 3).
- Produces: `usage.KindTranslate`.

- [ ] **Step 1: Write the failing test**

Add to `internal/gateway/translate_test.go`:

```go
// A translation is told apart from the turn's own call in the ledger.
func TestTranslationHasItsOwnKind(t *testing.T) {
	if got := callKind(http.Header{"User-Agent": []string{TranslateAgent}}); got != "translate" {
		t.Fatalf("kind: %q", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags nogui ./internal/gateway -run TestTranslationHasItsOwnKind -count=1`
Expected: FAIL — the kind is "".

- [ ] **Step 3: Give the kind its name**

In `internal/gateway/codex_backend.go`, in `callKind`, after the `RouterAgent` case:

```go
	if v == "" && h.Get("User-Agent") == TranslateAgent {
		v = "translate" // a translation magpie made of a request's texts
	}
```

In `internal/usage/purpose.go`, in the kind constants:

```go
	// KindTranslate is the kind of a row of a translation magpie made: the
	// texts of a request or a reply in another language (gateway's
	// translate.go)
	KindTranslate = "translate"
```

and in `purposeKinds`:

```go
	add("Translation", KindTranslate)
```

- [ ] **Step 4: Regenerate the GUI's kinds**

Run: `go generate ./internal/usage` from the repo root.
Expected: `internal/gui/assets/purposes.js` is rewritten with the new kind.

- [ ] **Step 5: Translate the name**

In `internal/gui/assets/i18n.js`, beside the "Advisor" entry, add the name in every language:

```js
    "Translation": "翻译",
```

```js
    "Translation": "翻譯",
```

```js
    "Translation": "翻訳",
```

```js
    "Translation": "Übersetzung",
```

(English needs no entry: the name is its own translation.)

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test -tags nogui ./internal/gateway -run TestTranslationHasItsOwnKind -count=1`
Expected: PASS.

Run: `node --test internal/gui/tests/i18n-en-no-cjk.test.cjs` from the repo root.
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/gateway/codex_backend.go internal/usage/purpose.go internal/gui/assets/purposes.js internal/gui/assets/i18n.js internal/gateway/translate_test.go
git commit -m "usage: a translation is a kind of its own in the ledger

A translation magpie made is told apart from the turn's own call, so the
usage rows say which of the two spent the tokens.

Verified: go test -tags nogui ./internal/gateway -run
TestTranslationHasItsOwnKind, and node --test
internal/gui/tests/i18n-en-no-cjk.test.cjs. The test fails without the
change: the kind is empty."
```

---

### Task 7: The reference

**Files:**
- Create: `docs/subsystems/gateway-translation.md`
- Modify: `docs/subsystems/gateway-request-hooks.md` (the `onTranslate` hook)
- Modify: `docs/subsystems/README.md` (the new reference in the table)

**Interfaces:**
- Consumes: everything the tasks above built.
- Produces: nothing the code reads.

- [ ] **Step 1: Write the reference**

Create `docs/subsystems/gateway-translation.md`:

````markdown
# Prompt translation

A request's texts can be translated into another language before they reach
a model, and the model's reply translated back, both by a model the user
names. The policy is a plugin's; the mechanism is the gateway's.

## Responsibilities and sources of truth

| Part | Responsibility | Source |
| --- | --- | --- |
| The plan | The model to translate with, the two languages, the scripts and the prompt | `plugin.TranslatePlan` in [`internal/plugin/gateway.go`](../../internal/plugin/gateway.go); the hook `onTranslate` in [`internal/plugin/host.js`](../../internal/plugin/host.js) |
| The request's texts | Which texts are translated, and the call that translates them | [`internal/gateway/translate.go`](../../internal/gateway/translate.go) |
| The reply's texts | The encoder that holds the reply's text, and the whole reply's translation | [`internal/gateway/translate_reply.go`](../../internal/gateway/translate_reply.go) |
| The wait | How long a translation is given to answer | `TranslateTimeout` in [`internal/settings/settings.go`](../../internal/settings/settings.go) |
| The ledger | The kind a translation's row carries | `usage.KindTranslate` in [`internal/usage/purpose.go`](../../internal/usage/purpose.go) |

## Runtime path

1. A request an agent sent goes through `serveAgent`. Its hook round trip
   (`gatewayHooks`) asks the plugins' `onTranslate` for a plan, and the plan
   is kept on the request's context.
2. `translate` reads the request into the IR, and the texts of the user and
   assistant messages are translated before the body is built for the
   provider. The system prompt, the tools, the tool calls, the tool results
   and the reasoning are left as they are.
3. The provider is asked. Its reply is decoded into the IR's events.
4. A streamed reply's text is held by `translateEncoder` and translated when
   it ends; the events that came after it wait with it, so the client reads
   the answer in the order the model gave it. A whole reply's text parts are
   translated before it is rendered.
5. The translation is asked of the model the plan names, through the gateway
   itself (`askChatCtx`), so it is counted in the usage rows under its own
   kind and cannot ask the plugins again.

## Constraints and failure behavior

- Only a message's text parts are translated, and only in the user and
  assistant messages. A tool's arguments are code, and a tool's output is
  what a command said.
- A text that is already in the language wanted is left as it is, told by
  the script the plan named.
- Both ends of a translation are kept, so a text a translation produced
  finds the text it was made from. A conversation's history therefore costs
  no call.
- A failure leaves the request as it was: no plan, a model that is not
  served, a model that fails or is late, or an answer whose count is not the
  count asked for. Half a translation is worse than none.
- The wait is `TranslateTimeout`, 30 seconds by default and at most 300.

## Verification

```sh
go test -tags nogui ./internal/gateway -run 'TestTranslat' -count=1
go test -tags nogui ./internal/plugin -run 'TestGatewayHookTranslate' -count=1
go test -tags nogui ./internal/settings -run TestTranslateTimeout -count=1
```
````

- [ ] **Step 2: Document the hook**

In `docs/subsystems/gateway-request-hooks.md`, after the "Entry and request contract" section, add:

````markdown
## The translate plan

A hook may export `onTranslate(ctx)` beside `onRequest` and `onResponse`. It
is asked once per request, in the request's own round trip, and returns the
plan the gateway translates with, or null for none:

```js
export async function onTranslate(ctx) {
  return {
    model: "gemini-3-flash",
    from: "zh", to: "en",
    fromName: "Chinese", toName: "English",
    fromScript: "cjk", toScript: "latin",
    system: "Translate the following {from} text into {to}. Output only the translation."
  }
}
```

The gateway asks the model itself, in process, and does the translating, so
the plan is a policy and not a call: a hook that called back into magpie
would have its own request offered to `onTranslate` again. A plan that names
no model, no language or no prompt is no plan. See [Prompt
translation](gateway-translation.md).
````

- [ ] **Step 3: Link the reference**

In `docs/subsystems/README.md`, in the table, after the "Asynchronous gateway request hooks" row:

```markdown
| A request's texts translated for a model, and the reply translated back | [Prompt translation](gateway-translation.md) |
```

- [ ] **Step 4: Check the links resolve**

Run: `grep -o '(\.\./\.\./[a-z/-]*\.go)' docs/subsystems/gateway-translation.md | tr -d '()' | sed 's|\.\./\.\./||' | while read -r f; do test -f "$f" || echo "missing: $f"; done`
Expected: nothing printed.

- [ ] **Step 5: Commit**

```bash
git add docs/subsystems/gateway-translation.md docs/subsystems/gateway-request-hooks.md docs/subsystems/README.md
git commit -m "docs: the reference for a request's texts translated for a model

What the plugin decides and what the gateway does, and where each is.

Verified: the links in the new reference resolve, and go test -tags nogui
./internal/gateway -run 'TestTranslat' passes."
```

---

### Task 8: The whole build

**Files:**
- None. This task runs the checks.

**Interfaces:**
- Consumes: everything above.
- Produces: nothing.

- [ ] **Step 1: Format**

Run: `gofmt -l internal/gateway internal/plugin internal/settings internal/usage`
Expected: nothing printed.

- [ ] **Step 2: Vet and build**

Run: `go vet ./... && GOOS=linux go vet -tags nogui ./... && GOOS=windows go vet ./...`
Expected: nothing printed.

Run: `go build && GOOS=linux go build -tags nogui && GOOS=windows go build`
Expected: nothing printed.

- [ ] **Step 3: The suite**

Run: `go test -tags nogui ./... -count=1`
Expected: PASS. A red that fails on `origin/main` too is named with its issue, not left out.

- [ ] **Step 4: The GUI's kinds**

Run: `node --test internal/gui/tests/i18n-en-no-cjk.test.cjs` from the repo root.
Expected: PASS.

- [ ] **Step 5: Record what was run**

Write the results of Steps 1 to 4 into the body of the last commit that
changed something, as `docs/code-standards.md` asks. The record names what
was run, at which head, and what was not run.
