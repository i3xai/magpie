package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
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
	// the answers are in the other script, as a translation's are: one for
	// each text asked for, in the order they were asked for
	answers := make([]string, len(texts))
	for i, text := range texts {
		answers[i] = "translated"
		if !holdsCJK(text) {
			answers[i] = "翻译"
		}
	}
	arr, _ := json.Marshal(answers)
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

// forgetTranslations clears the translations kept, so a test run again
// (-count) starts where it did the first time: a text the cache knows would
// be found in it and never asked for again.
func forgetTranslations() {
	translateCache.mu.Lock()
	defer translateCache.mu.Unlock()
	translateCache.byPair = map[string]*translationPair{}
	translateCache.order = nil
	translateCache.head = 0
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

// A batch of texts is answered in the order they were asked for: each
// answer is put back where its own text was.
func TestTranslatorAnswersABatchInOrder(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a7", "ka", []string{"m"}, m)
	s := New()
	plan := aPlan("a7/m")
	// the core knows no script for this language, so every text of it is
	// translated: a Chinese one and an English one, answered in the
	// scripts opposite their own.
	plan.ToScript = ""
	tr := aTranslator(t, s, plan)
	out := tr.translate(context.Background(), []string{"你好", "hello"}, true)
	if len(out) != 2 || out[0] != "translated" || out[1] != "翻译" {
		t.Fatalf("translated: %q", out)
	}
	if m.calls() != 1 {
		t.Fatalf("calls: %d", m.calls())
	}
	if len(m.asked[0]) != 2 || m.asked[0][0] != "你好" || m.asked[0][1] != "hello" {
		t.Fatalf("asked for: %q", m.asked[0])
	}
}

// A request's texts are translated; the system prompt, a tool's arguments,
// a tool's output and the reasoning are left as they are: a tool's
// arguments are code, and a result is what a command said.
func TestTranslatorLeavesTheToolsAndTheSystemAlone(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a8", "ka", []string{"m"}, m)
	s := New()
	tr := aTranslator(t, s, aPlan("a8/m"))
	req := &Request{
		Messages: []Message{
			{Role: "system", Parts: []Part{{Kind: Text, Text: "你是助手"}}},
			{Role: "user", Parts: []Part{{Kind: Text, Text: "你好"}}},
			{Role: "assistant", Parts: []Part{
				{Kind: Thinking, Text: "先想一下"},
				{Kind: ToolCall, Name: "Bash", Args: json.RawMessage(`{"command":"ls"}`)},
				{Kind: Text, Text: "世界"},
			}},
			{Role: "user", Parts: []Part{{Kind: ToolResult, Text: "你好, 文件"}}},
		},
	}
	tr.translateRequestText(context.Background(), req)
	// the user's and the assistant's texts are translated
	if req.Messages[1].Parts[0].Text != "translated" {
		t.Fatalf("the user's text: %q", req.Messages[1].Parts[0].Text)
	}
	if req.Messages[2].Parts[2].Text != "translated" {
		t.Fatalf("the assistant's text: %q", req.Messages[2].Parts[2].Text)
	}
	// the system prompt, the reasoning, the tool call and the tool's result
	// are as they were
	if req.Messages[0].Parts[0].Text != "你是助手" {
		t.Fatalf("the system prompt: %q", req.Messages[0].Parts[0].Text)
	}
	if req.Messages[2].Parts[0].Text != "先想一下" {
		t.Fatalf("the reasoning: %q", req.Messages[2].Parts[0].Text)
	}
	if string(req.Messages[2].Parts[1].Args) != `{"command":"ls"}` {
		t.Fatalf("the tool call's arguments: %s", req.Messages[2].Parts[1].Args)
	}
	if req.Messages[3].Parts[0].Text != "你好, 文件" {
		t.Fatalf("the tool's result: %q", req.Messages[3].Parts[0].Text)
	}
}

// A text put back is the newest text of its pair: the cache does not drop
// it when the window passes the copy of its key it kept behind.
func TestTranslatorKeepsATextThatWasPutBack(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a9", "ka", []string{"m"}, m)
	s := New()
	tr := aTranslator(t, s, aPlan("a9/m"))
	pair := translateCache.pair(tr.plan)
	// the pair is filled to its bound
	for i := range translationPairMost / 2 {
		pair.remember(fmt.Sprintf("你好%d", i), fmt.Sprintf("hello%d", i))
	}
	// the first text is put back: it is the newest text of the pair again
	pair.remember("你好0", "hello0")
	// and the window passing the copy of its key it kept behind does not
	// drop it
	pair.remember("再见", "bye")
	if _, ok := pair.lookup("你好0", true); !ok {
		t.Fatal("the text put back was dropped")
	}
}

// The cache keeps a bounded number of pairs: a plugin that varies its
// prompt from one request to the next must not grow it for the life of the
// process.
func TestTranslatorKeepsThePairsBounded(t *testing.T) {
	fresh(t)
	first := &plugin.TranslatePlan{Model: "m", From: "zh", To: "en", System: "the first prompt"}
	translateCache.pair(first).remember("你好", "hello")
	for i := range translationPairsMost {
		translateCache.pair(&plugin.TranslatePlan{Model: "m", From: "zh", To: "en", System: fmt.Sprintf("prompt %d", i)})
	}
	// the oldest pair is gone: the texts it kept are not found any more
	if _, ok := translateCache.pair(first).lookup("你好", true); ok {
		t.Fatal("the oldest pair was kept")
	}
}

// A call that gave no answer names what gave no answer: the router's call
// names the model, as it did before a translation had an agent of its own,
// and a translation's names the translation.
func TestAskChatCtxNamesWhatGaveNoAnswer(t *testing.T) {
	fresh(t)
	serveOn(t, "a10", "ka", []string{"m"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the caller's context is ended before the call
	body := []byte(`{"model":"a10/m"}`)
	_, err := s.askChatCtx(ctx, 60*time.Second, access.Identity{}, "a10/m", RouterAgent, body)
	if err == nil || !strings.Contains(err.Error(), "a10/m gave no answer") || strings.Contains(err.Error(), RouterAgent) {
		t.Fatalf("the router's call: %v", err)
	}
	_, err = s.askChatCtx(ctx, 60*time.Second, access.Identity{}, "a10/m", TranslateAgent, body)
	if err == nil || !strings.Contains(err.Error(), "the translation asked of a10/m gave no answer") {
		t.Fatalf("the translation's call: %v", err)
	}
}

// The cache lives for the gateway's life and every request reaches it, so
// two translations at once may not write over each other, and each gets
// what it asked for.
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
				for _, text := range []string{fmt.Sprintf("你好%d-%d", i, j), "共同的"} {
					out := tr.translate(context.Background(), []string{text}, true)
					if len(out) != 1 || out[0] != "translated" {
						t.Errorf("%q was translated to %q", text, out)
						return
					}
				}
			}
		}(i)
	}
	wg.Wait()
}

// A request's Chinese prompt reaches the provider in English. The reply's
// own side is Task 5's, and is not asserted here.
//
// The provider is asked a stream, as every request that goes through the
// gateway's translation is (translate.go), so the fake answers with one.
func TestTranslationThroughTheGateway(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a11", "ka", []string{"m"}, m)
	var asked string
	serveOn(t, "a12", "kb", []string{"m"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		asked = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"done"}}]}`,
			`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`))
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
// and not through the hooks, so it has no plan. The translator's model is
// another one, so the call the request itself makes is not counted as a
// translation.
func TestMagpiesOwnRequestIsNotTranslated(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a13", "ka", []string{"m"}, m)
	var asked string
	serveOn(t, "a14", "kb", []string{"m"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		asked = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	s := New()
	r := httptest.NewRequest("POST", "http://magpie/v1/chat/completions", nil)
	r.Header.Set("User-Agent", RouterAgent)
	rec := httptest.NewRecorder()
	s.serve(rec, r, provider.Chat, []byte(`{"model":"a14/m","messages":[{"role":"user","content":"帮我写个函数"}]}`))
	if m.calls() != 0 {
		t.Fatalf("a translation was made: %d", m.calls())
	}
	if !strings.Contains(asked, "帮我写个函数") {
		t.Fatalf("the request was translated: %s", asked)
	}
}

// recordedEncoder keeps the events it is given, in the order they came.
type recordedEncoder struct {
	events []Event
}

func (e *recordedEncoder) event(ev Event) { e.events = append(e.events, ev) }
func (e *recordedEncoder) finish()        {}
func (e *recordedEncoder) keepalive()     {}

// A reply's text reaches the client in the language asked, and the events
// that came after it keep their place.
func TestTranslatedReplyKeepsItsOrder(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a15", "ka", []string{"m"}, m)
	s := New()
	tr := aTranslator(t, s, aPlan("a15/m"))
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

// A reply's text that came after a call is translated too, and keeps its
// place: the client reads the answer in the order the model gave it.
func TestTranslatedReplyAfterACall(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a22", "ka", []string{"m"}, m)
	s := New()
	tr := aTranslator(t, s, aPlan("a22/m"))
	rec := &recordedEncoder{}
	enc := &translateEncoder{streamEncoder: rec, tr: tr, ctx: tr.ctx}
	enc.event(Event{Kind: KText, Text: "first"})
	enc.event(Event{Kind: KToolStart, ID: "t1", Name: "read"})
	enc.event(Event{Kind: KText, Text: "second"})
	enc.event(Event{Kind: KStop, Stop: "tool"})
	enc.finish()
	if len(rec.events) != 4 {
		t.Fatalf("events: %+v", rec.events)
	}
	if rec.events[0].Kind != KText || rec.events[0].Text != "翻译" {
		t.Fatalf("first event: %+v", rec.events[0])
	}
	if rec.events[1].Kind != KToolStart {
		t.Fatalf("second event: %+v", rec.events[1])
	}
	if rec.events[2].Kind != KText || rec.events[2].Text != "翻译" {
		t.Fatalf("third event: %+v", rec.events[2])
	}
	if rec.events[3].Kind != KStop {
		t.Fatalf("fourth event: %+v", rec.events[3])
	}
}

// A whole reply's text is translated before it is rendered, so a client
// that asked for no stream is given the same language a streaming client is.
func TestTranslatedWholeReply(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a16", "ka", []string{"m"}, m)
	s := New()
	tr := aTranslator(t, s, aPlan("a16/m"))
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
	serveOn(t, "a17", "ka", []string{"m"}, m)
	serveOn(t, "a18", "kc", []string{"m"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// the provider is asked a stream, as every request that goes
		// through the gateway's translation is (translate.go), so the fake
		// answers with one
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"done"}}]}`,
			`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`))
	}))
	s := New()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"a18/m","messages":[{"role":"user","content":"帮我写个函数"}]}`))
	req = req.WithContext(withTranslatePlan(req.Context(), aPlan("a17/m")))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "翻译") {
		t.Fatalf("the client was told: %s", rec.Body.String())
	}
}

// A reply the client asked for as a stream reaches it in the language
// asked as well: the text is held while it is translated, and let go with
// the events that came after it.
func TestTranslatedStreamThroughTheGateway(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a24", "ka", []string{"m"}, m)
	serveOn(t, "a25", "kc", []string{"m"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// the provider is asked a stream, as every request that goes
		// through the gateway's translation is (translate.go), so the fake
		// answers with one
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"done"}}]}`,
			`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`))
	}))
	s := New()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"a25/m","stream":true,"messages":[{"role":"user","content":"帮我写个函数"}]}`))
	req = req.WithContext(withTranslatePlan(req.Context(), aPlan("a24/m")))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "翻译") {
		t.Fatalf("the client was told: %s", rec.Body.String())
	}
}

// The encoder a request with a plan gets translates the text it is given.
func TestTranslatedEncoder(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a23", "ka", []string{"m"}, m)
	s := New()
	tr := aTranslator(t, s, aPlan("a23/m"))
	rec := httptest.NewRecorder()
	enc := encoder(provider.Responses, newSSEWriter(rec), &Request{Model: "m"}, tr)
	enc.event(Event{Kind: KText, Text: "done"})
	enc.finish()
	if !strings.Contains(rec.Body.String(), "翻译") {
		t.Fatalf("the client was told: %s", rec.Body.String())
	}
}

// A client that asked for structured output is not translated: its answer
// is JSON, and a translation of it is neither valid JSON nor the JSON the
// client asked for.
func TestStructuredOutputIsNotTranslated(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a19", "ka", []string{"m"}, m)
	s := New()
	tr := aTranslator(t, s, aPlan("a19/m"))
	rec := httptest.NewRecorder()
	enc := encoder(provider.Responses, newSSEWriter(rec), &Request{Model: "m", Format: &Format{Type: "json_object"}}, tr)
	enc.event(Event{Kind: KText, Text: "done"})
	enc.finish()
	if !strings.Contains(rec.Body.String(), "done") {
		t.Fatalf("the structured answer: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "翻译") {
		t.Fatalf("the structured answer was translated: %s", rec.Body.String())
	}
}

// A whole answer a client asked to be JSON is not translated either: the
// JSON it asked for is what it gets.
func TestStructuredWholeReplyIsNotTranslated(t *testing.T) {
	fresh(t)
	m := &translateModel{}
	serveOn(t, "a20", "ka", []string{"m"}, m)
	serveOn(t, "a21", "kc", []string{"m"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// the provider is asked a stream, as every request that goes
		// through the gateway's translation is (translate.go), so the fake
		// answers with one
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"{\"answer\": \"done\"}"}}]}`,
			`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`))
	}))
	s := New()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"a21/m","response_format":{"type":"json_object"},"messages":[{"role":"user","content":"帮我写个函数"}]}`))
	req = req.WithContext(withTranslatePlan(req.Context(), aPlan("a20/m")))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "done") {
		t.Fatalf("the client was told: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "翻译") {
		t.Fatalf("the client's JSON was translated: %s", rec.Body.String())
	}
}

// A translation is told apart from the turn's own call in the ledger.
func TestTranslationHasItsOwnKind(t *testing.T) {
	if got := callKind(http.Header{"User-Agent": []string{TranslateAgent}}); got != "translate" {
		t.Fatalf("kind: %q", got)
	}
}
