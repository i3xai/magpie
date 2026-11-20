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

// withoutTranslatePlan takes the plan off a context: a request magpie makes
// of itself is not translated (the search, the vision stand-in, the advisor,
// the router, the translation itself, the titles, the compaction). The plan
// rides on the agent's request's context, and those calls are made with it.
func withoutTranslatePlan(ctx context.Context) context.Context {
	return context.WithValue(ctx, translateKey{}, (*plugin.TranslatePlan)(nil))
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
//
// The cache lives for the process and every request the gateway serves
// reaches it, so each pair's entries are under its own lock, and the pairs
// themselves are bounded: the oldest go first.
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

// put keeps a text of the pair; the pair's lock is held.
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
