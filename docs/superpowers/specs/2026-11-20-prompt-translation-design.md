# Prompt translation between models — design

A prompt written in Chinese is translated into English by one model, sent to
another model, and the reply is translated back into Chinese for the client.
This document says where in magpie that belongs, what the core provides, and
what a plugin decides.

## Goal

- A request whose model is B carries its prompt to B in English, translated by
  a model A the user names.
- B's reply reaches the client in Chinese, translated by the same model A.
- The translation is the plugin's policy: which requests are translated, which
  model A is asked, and with what prompt. The core provides the mechanism.

## What the core provides

Two primitives, and nothing else:

1. **A request-side hook on the request IR.** The core hands the plugin's
   translator the texts that need translating and writes the translations
   back into the IR before the provider body is built.
2. **A response-side encoder.** The core holds the reply's text, asks the
   plugin's translator for the Chinese, and releases the text to the client.

The plugin supplies a plan: the model A, the prompt, and the languages.

## Why the core, and not only a plugin

Both plugin surfaces that touch the gateway are bounded in a way an LLM call
does not fit:

| Surface | Bound | Verdict |
| --- | --- | --- |
| JS gateway middleware (`internal/middleware`) | `onRequest` 250 ms, `onEvent` 50 ms an event, `onResponse` 250 ms; a hook past its bound is interrupted and fails open | A translation call takes seconds. It cannot go here. |
| Asynchronous request hooks (`internal/plugin`, `docs/subsystems/gateway-request-hooks.md`) | `onRequest` may rewrite the body and may wait up to 30 s; `onResponse` "cannot rewrite client output" | The request half fits. The reply half is ruled out by the contract. |

Underneath that, a streamed reply's bytes are gone once they reach the
client, so a reply that is to be rewritten has to be held by the gateway
before it is written. That is core work in every case.

## Where the two primitives sit

### Request side: the request IR

`serve` reads a request into the IR (`parse`), and every provider attempt
builds its body from that IR (`buildFor` in `internal/gateway/gateway.go`,
which calls `buildChat`, `buildResponses`, `anthropicBody` and
`geminiRequest`). The IR is therefore the pivot: text changed there reaches
every provider, in every client protocol, on every fallback attempt, with no
per-protocol byte surgery.

The primitive goes in `translate` in `internal/gateway/gateway.go`, right
after `parseTranslated` and before the body is built.

`translate` is called once per candidate attempt (from `attempt`), so the
same request is translated again on every fallback. The cache in
[Translations and the cache](#translations-and-the-cache) is what keeps that
from costing a model call per attempt.

### Response side: the reply's IR events

A provider's reply is decoded into IR events (`decoderFor`), relayed by
`relayEvents`, and written to the client by a per-protocol `streamEncoder`
built in `encoder`. `fenceEncoder` in `internal/gateway/format_fence.go` is
already an encoder that holds a reply's text and rewrites it before letting
it go, which is the shape this primitive takes.

The primitive is a `translateEncoder` wrapping that encoder. It is built
where the encoder is built:

| Where | Reply |
| --- | --- |
| `streamTranslated` in `internal/gateway/continuation.go` | The agent's own streamed reply. |
| `relayStatus` in `internal/gateway/relay_status.go` | A backend that reports its own statuses, streamed or whole. |
| `translate` in `internal/gateway/gateway.go` | A whole reply, collected into a `Result` and rendered. |

A whole reply is not written by an encoder: it is collected into a `Result`
and rendered (`renderUsage`, then `render`). The same translation therefore
also runs over a `Result`'s text parts before they are rendered, or a client
that asked for no stream would get the model's English while a streaming
client got Chinese.

The Claude bridge (`internal/gateway/claude_subscription.go`) renders a whole
reply of its own, for a turn magpie runs itself. It is not translated: the
plan is not on that path.

## What the plugin gives the core

One hook in the plugin's hooks file, beside `onRequest` and `onResponse`:

```js
export async function onTranslate(ctx) {
  // ctx = { model: "<the model B the request goes to>" }
  // null: this plugin does not translate this request
  return {
    model: "gemini-3-flash",                 // model A, a model magpie routes
    from: "zh", to: "en",
    fromName: "Chinese", toName: "English",
    system: "Translate the following {from} text into {to}. Keep code, identifiers and formatting as they are. Output only the translation."
  };
}
```

The core fills `{from}` and `{to}` in `system` with `fromName` and `toName`.
The request phase uses the pair as given, the response phase uses it
reversed.

The hook is asked once per request, in the request's own hook round trip
(`gatewayHooks` in `internal/gateway/middleware.go`, which already asks the
plugin host about the request), and the plan is kept on the request's
context. The reply's half reads it from there, so a reply costs no second
round trip. A request whose plugin gives no plan is not translated and its
reply is not held.

The core asks the model A itself, through `askChat` in
`internal/gateway/classify.go` — the same in-process path the router's
classifier and `advise` in `internal/gateway/advisor_serve.go` use. Three
things follow from that, and all three are reasons to keep it:

- **No recursion.** The internal call goes through `serve`, not `serveAgent`,
  so no middleware and no hook runs on it. A plugin that called back into
  magpie's HTTP endpoint would have its own request offered to `onTranslate`
  again, and would ask again, for ever.
- **The ledger counts it.** The translation is a call magpie made, so it lands
  in the usage rows with its own kind, `translate`, and its own agent, the
  way the advisor's calls are told apart by `AdvisorAgent`. It is held to the
  caller's accounts, as the classifier's calls are held to the caller's
  (`accountHolds`), so a translation spends the account the turn spends.
- **No credentials to carry.** Model A is a model magpie routes. The plugin
  holds no API key of its own.

## What is translated

Only a message's text parts:

| Part | Translated |
| --- | --- |
| `Text` in a user message | yes |
| `Text` in an assistant message | yes, by the cache (see below) |
| `System` | no |
| `ToolCall` (its arguments) | no |
| `ToolResult` (its text) | no |
| `Thinking` | no |
| `Search`, `Advice` | no |

The filter is on the part's kind, not on the message's role: in the
Anthropic protocol a tool result is carried by a user message, so a filter
that took a user message's whole text would take the tool's output with it.

A text the cache knows is translated from it, whatever its script: the cache
holds the text it was made from, and that is known exactly, where a script is
a guess. A text the cache does not know is left as it is when it is already
in the language wanted: a text that holds no CJK is left as it is when
English is wanted, and one that holds CJK when Chinese is. That covers a
conversation that began before the feature was on, and a turn whose
translation failed and was let through unchanged.

## Translations and the cache

A translation is one call with all the texts that need it. The core sends
them as a JSON array of strings and asks for a JSON array of the same length
back; the answer is read as JSON, and a fence around it is stripped first. An
answer that is not JSON, or whose count is not the count that was asked for,
leaves every text of that call untranslated: half a translation would corrupt
the conversation, and the whole batch is refused instead.

The cache is what makes this affordable, and it is not an optimization:

- Without it, the assistant's history is re-translated on every turn, and the
  cost of a turn grows with the length of the conversation.
- Without it, every fallback attempt translates the whole request again.

The cache is one map per pair of languages and model A. Each translation
stores both ends: the text it was given and the text it produced. A lookup
for Chinese that was produced by an earlier English-to-Chinese translation
finds the English that made it, and the turn costs no call at all.

So a turn's calls are:

- the user's new words, once each, translated;
- the assistant's history, restored from the cache, at no cost;
- the reply, translated back.

The cache is kept in the gateway process and dropped when it stops. It holds
at most 2048 texts per pair of languages and model A; the oldest are dropped
first.

## The reply, and the order of its parts

The reply's text is held while it is translated and released after, so the
client sees Chinese. The reply's other events — a tool call, a usage row —
are held with it and released in the order they came.

That is a choice, and the other one was considered: releasing the tool call
as it comes and the text after would keep the tool call real-time, but the
text would arrive after it. Codex reads the Responses protocol's output items
in order, so the text landing after a function call would show out of order.
The order is kept.

A plugin that wants the text closer to real-time may ask for a smaller unit
— a sentence at a time — at the cost of a call a sentence.

The translation is a blocking call in the path of the reply. It is bounded by
the setting below, so it cannot hold a reply for ever, and it is started the
moment the reply's text is known to have ended, so the upstream waits for it
only for the rest of that message.

## Settings

`TranslateTimeout` in `internal/settings/settings.go`, in seconds, read
through a `TranslateWait` method beside `ClassifyWait`:

- 0 is the default, 30 seconds;
- from 1 to the most it may be set to, which is 300 seconds.

A translation that is not answered in time leaves its texts untranslated and
the turn goes on. The turn's own model is not held for it.

## Failures

Every failure leaves the request as it was:

| Failure | What happens |
| --- | --- |
| The plugin gives no plan | Nothing is translated. The reply is not held. |
| Model A is not a model magpie routes | Nothing is translated. |
| Model A fails or is late | The texts are sent untranslated. |
| The answer's count does not match | Every text of that call is left untranslated. |
| The plugin's hook throws or is late | That plugin gives no plan. Another plugin's plan is asked, and the request is translated if one gives a usable plan. |
| The client asked for structured output | Nothing is translated. Its answer is JSON, and a translation of it is neither valid JSON nor the JSON the client asked for. |

The failure is counted in the usage rows and logged, at the rate the gateway
logs its other failures.

## What this does not do

- It does not translate the system prompt, the tools, the tool results or the
  reasoning.
- It does not translate a request magpie makes of itself: the titles, the
  classifier, the search and the vision stand-ins go through `serve` and are
  not agent requests.
- It does not translate the images, the files or the audio.
- It does not change what B is asked, beyond the text it is given.

## Files

| File | Change |
| --- | --- |
| `internal/gateway/translate.go` | New. The two primitives, the cache, the plan and the scope. |
| `internal/gateway/gateway.go` | `translate` calls the request-side primitive and translates a whole reply's text before it is rendered. |
| `internal/gateway/continuation.go` | `streamTranslated` wraps the encoder with the reply's translator. |
| `internal/gateway/relay_status.go` | `relayStatus` wraps the encoder and translates a whole reply's text. |
| `internal/gateway/middleware.go` | `gatewayHooks` keeps the plan the plugin gave. |
| `internal/plugin/gateway.go`, `internal/plugin/host.js` | The `onTranslate` hook: asked in the request's round trip, bounded, and its failure counted. |
| `internal/settings/settings.go` | `TranslateTimeout` and `TranslateWait`. |
| `internal/usage` | The `translate` kind, so a translation is told apart from the turn's own call. |
| `docs/subsystems/gateway-translation.md` | New subsystem reference. |
| `docs/subsystems/gateway-request-hooks.md` | The `onTranslate` hook, beside `onRequest` and `onResponse`. |
| `docs/subsystems/README.md` | The new reference, in its table. |

## Tests

The tests are the behaviour, and each one is named for what it holds.

- A request's Chinese prompt reaches the provider in English, and the
  provider's English reply reaches the client in Chinese.
- A tool call's arguments and a tool result's text are not translated, in
  every client protocol.
- The reply's events reach the client in the order they came.
- The assistant's history is restored from the cache without a call, and the
  user's new words are translated once.
- A translation that fails, is late, or answers with the wrong number of
  texts leaves the request unchanged.
- A plugin that gives no plan changes nothing.
- A request magpie makes of itself is not translated.
- The translation is counted in the usage rows under its own kind.

No test reaches the network: the model A is a fake server on `httptest`, and
the plugin is a hooks file in a temporary HOME. The gateway's own tests run
under `testenv`, as the code standards ask.

## Risks

- **A long conversation.** The cache keeps the assistant's history free, but
  the user's new words are translated on every turn, and the cache is dropped
  when the gateway stops. A conversation that spans a restart pays again for
  the history it still carries.
- **The reply stalls.** The translation is a blocking call in the reply's
  path. `TranslateTimeout` bounds it; the wait is visible to the client as a
  pause before the text.
- **A mapping error.** A model A that merges two texts or drops one leaves
  the batch untranslated, which is safe but loses the turn's translation.
- **The plan is the plugin's.** A plugin that names a model A magpie does not
  route translates nothing, and says so in the log rather than in the
  client's face.
