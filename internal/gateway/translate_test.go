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
