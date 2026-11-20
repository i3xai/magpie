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

	"github.com/yetone/magpie/internal/provider"
)

// markingModel is a model that answers with the text it was given, marked,
// so a test can tell what was asked of it and what came back.
type markingModel struct {
	mu    sync.Mutex
	asked [][]string
}

func (m *markingModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Stream   bool `json:"stream"`
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
	answers := make([]string, len(texts))
	for i, text := range texts {
		answers[i] = "译(" + text + ")"
	}
	arr, _ := json.Marshal(answers)
	content, _ := json.Marshal(string(arr))
	// the gateway asks a provider a stream when it rebuilds the request for
	// it, and the body as it came when it relays it
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":`+string(content)+`}}]}`,
			`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + string(content) + `}}]}`))
}

// askedTexts is the texts the model was asked for, one call each.
func (m *markingModel) askedTexts() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]string(nil), m.asked...)
}

// aSearchRound is a round that answers with the texts it is given, as a
// model's reply does: the events the search's renderer is given.
func aSearchRound(texts ...string) round {
	return func(ctx context.Context, req *Request) (<-chan Event, int, string) {
		in := make(chan Event, len(texts)+1)
		for _, text := range texts {
			in <- Event{Kind: KText, Text: text}
		}
		in <- Event{Kind: KStop, Stop: "stop"}
		close(in)
		return in, 200, ""
	}
}

// replyChunk is one chunk of a Chat stream: the text it carries and the
// reason it ends the reply, if it does.
type replyChunk struct {
	Content string
	Finish  string
}

// replyChunks are the chunks a Chat stream carries, in the order they came.
func replyChunks(t *testing.T, body string) []replyChunk {
	t.Helper()
	var out []replyChunk
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		for _, c := range chunk.Choices {
			out = append(out, replyChunk{Content: c.Delta.Content, Finish: c.FinishReason})
		}
	}
	return out
}

// chatContent is the text a Chat reply's whole body carries.
func chatContent(t *testing.T, body string) string {
	t.Helper()
	var got struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(body), &got) != nil || len(got.Choices) != 1 {
		t.Fatalf("the client's reply: %s", body)
	}
	return got.Choices[0].Message.Content
}

// A reply that came through magpie's own search reaches the client in the
// language asked: the search's answer is rendered by the search's renderer,
// which is not the one the design's table names.
//
// The whole reply and the streamed one are both asserted: the renderer has
// two paths (the encoder for the stream, renderUsage for the whole reply),
// and each needs its own proof.
func TestTheSearchsReplyIsTranslated(t *testing.T) {
	for _, c := range []struct {
		name   string
		stream bool
	}{
		{"whole", false},
		{"stream", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			m := &markingModel{}
			serveOn(t, "a33", "ka", []string{"m"}, m)
			s := New()
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"帮我写个函数"}]}`))
			r = r.WithContext(withTranslatePlan(r.Context(), aPlan("a33/m")))
			req := &Request{Model: "m", Stream: c.stream}
			rec := httptest.NewRecorder()
			code, msg := s.searchReply(rec, r, provider.Chat, "Test", req, &Usage{}, aSearchRound("done"), false)
			if code != 200 {
				t.Fatalf("code %d: %s (%s)", code, rec.Body.String(), msg)
			}
			if c.stream {
				chunks := replyChunks(t, rec.Body.String())
				var texts []string
				for _, chunk := range chunks {
					if chunk.Content != "" {
						texts = append(texts, chunk.Content)
					}
				}
				if len(texts) != 1 || texts[0] != "译(done)" {
					t.Fatalf("the client was told: %s", rec.Body.String())
				}
			} else if got := chatContent(t, rec.Body.String()); got != "译(done)" {
				t.Fatalf("the client was told: %s", rec.Body.String())
			}
			asked := m.askedTexts()
			if len(asked) != 1 || len(asked[0]) != 1 || asked[0][0] != "done" {
				t.Fatalf("the translation model was asked: %v", asked)
			}
		})
	}
}

// A reply of nothing but text reaches the client as one piece, at the
// reply's end: the text is held while it is translated and let go with the
// event that came after it, which for a reply of nothing but text is the
// reply's stop.
func TestTheSearchsReplyHoldsTheTextUntilTheEnd(t *testing.T) {
	fresh(t)
	m := &markingModel{}
	serveOn(t, "a34", "ka", []string{"m"}, m)
	s := New()
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"帮我写个函数"}]}`))
	r = r.WithContext(withTranslatePlan(r.Context(), aPlan("a34/m")))
	req := &Request{Model: "m", Stream: true}
	rec := httptest.NewRecorder()
	code, msg := s.searchReply(rec, r, provider.Chat, "Test", req, &Usage{}, aSearchRound("first", "second"), false)
	if code != 200 {
		t.Fatalf("code %d: %s (%s)", code, rec.Body.String(), msg)
	}
	chunks := replyChunks(t, rec.Body.String())
	var texts []string
	at, ended := -1, -1
	for i, chunk := range chunks {
		if chunk.Content != "" {
			texts = append(texts, chunk.Content)
			at = i
		}
		if chunk.Finish != "" {
			ended = i
		}
	}
	if len(texts) != 1 || texts[0] != "译(firstsecond)" {
		t.Fatalf("the client was told: %s", rec.Body.String())
	}
	if ended != at+1 {
		t.Fatalf("the text did not come at the reply's end: %s", rec.Body.String())
	}
	asked := m.askedTexts()
	if len(asked) != 1 || len(asked[0]) != 1 || asked[0][0] != "firstsecond" {
		t.Fatalf("the translation model was asked: %v", asked)
	}
}
