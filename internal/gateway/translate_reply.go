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
//
// A text that came after a call is held and translated as well, so every
// text the model gave reaches the client in the language asked.
type translateEncoder struct {
	streamEncoder
	tr    *translator
	ctx   context.Context
	text  strings.Builder
	after []Event
}

func (e *translateEncoder) event(ev Event) {
	if ev.Kind == KText {
		e.text.WriteString(ev.Text)
		return
	}
	if e.text.Len() == 0 {
		// nothing to translate yet: the event goes on as it came
		e.streamEncoder.event(ev)
		return
	}
	// the text is translated and let go before the event that came after
	// it, so the client reads the answer in the order the model gave it
	e.after = append(e.after, ev)
	e.release()
}

// release translates the text held and lets it, and the events held with it,
// go on in the order they came.
func (e *translateEncoder) release() {
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
