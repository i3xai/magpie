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
// two translations at once may not write over each other.
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
