# Prompt translation

A request's texts can be translated into another language before they reach
a model, and the model's reply translated back, both by a model the user
names. The policy is a plugin's; the mechanism is the gateway's.

The hook that gives the policy is described in [Asynchronous gateway request
hooks](gateway-request-hooks.md).

## Responsibilities and sources of truth

| Part | Responsibility | Source |
| --- | --- | --- |
| The plan | The model to translate with, the two languages, the scripts and the prompt | `plugin.TranslatePlan` in [`internal/plugin/gateway.go`](../../internal/plugin/gateway.go); the hook `onTranslate` in [`internal/plugin/host.js`](../../internal/plugin/host.js) |
| The request's texts | Which texts are translated, and the call that translates them | [`internal/gateway/translate.go`](../../internal/gateway/translate.go) |
| Magpie's own calls | Which calls are not translated, and how the plan is taken off their context | `withoutTranslatePlan` in [`internal/gateway/translate.go`](../../internal/gateway/translate.go); the markers it is called at: `ownAsk` in [`internal/gateway/middleware.go`](../../internal/gateway/middleware.go), `askChatCtx` in [`internal/gateway/classify.go`](../../internal/gateway/classify.go), the title calls in [`internal/gateway/codex_titles.go`](../../internal/gateway/codex_titles.go), and the compaction's summary and the fallback in [`internal/gateway/codex_backend.go`](../../internal/gateway/codex_backend.go) |
| The reply's texts | The encoder that holds the reply's text, and the whole reply's translation | [`internal/gateway/translate_reply.go`](../../internal/gateway/translate_reply.go) |
| The wait | How long a translation is given to answer | `TranslateTimeout` and the `TranslateWait` that reads it in [`internal/settings/settings.go`](../../internal/settings/settings.go) |
| The ledger | The kind a translation's row carries | `usage.KindTranslate` in [`internal/usage/purpose.go`](../../internal/usage/purpose.go) |

## Runtime path

1. A request an agent sent goes through `serveAgent`. Its hook round trip
   (`gatewayHooks`) asks the plugins' `onTranslate` for a plan, and the plan
   is kept on the request's context.
2. A request whose texts are to be translated is not relayed as written: a
   relay sends the client's own bytes and never reaches the IR. It is asked
   on the client's own API where the provider serves the model there, and it
   goes the way every other request goes, through the IR.
3. `translate` reads the request into the IR, and the texts of the user and
   assistant messages are translated before the body is built for the
   provider. The system prompt, the tools, the tool calls, the tool results
   and the reasoning are left as they are. A client that asked for structured
   output is not translated at all (see the constraints below).
4. The provider is asked. Its reply is decoded into the IR's events.
5. A streamed reply's text is held by `translateEncoder` until the event that
   comes after it, and translated then: the text goes first and the events
   that came after it wait for it, so the client reads the answer in the order
   the model gave it. A text that comes after a tool call is held and
   translated on its own. A whole reply's text parts are translated before it
   is rendered. The renderers that answer the client, and the translator each
   is given:

   | The renderer | The reply | The translator |
   | --- | --- | --- |
   | `streamTranslated` in [`internal/gateway/continuation.go`](../../internal/gateway/continuation.go) | The agent's own streamed reply | The request's own |
   | `relayStatus` in [`internal/gateway/relay_status.go`](../../internal/gateway/relay_status.go) | A backend that reports its own statuses, streamed or whole | The request's own, when there is one |
   | `relay` in [`internal/gateway/claude_subscription.go`](../../internal/gateway/claude_subscription.go) | A reply magpie's own search or advisor answered, streamed or whole | The request's own, when the search answers the client; none for the Claude bridge, which answers a turn magpie runs itself |
   | `translate` in [`internal/gateway/gateway.go`](../../internal/gateway/gateway.go) | A whole reply, collected into a `Result` and rendered | The request's own |

6. The translation is asked of the model the plan names, through the gateway
   itself (`askChatCtx`), so it is counted in the usage rows under its own
   kind and cannot ask the plugins again.

## Constraints and failure behavior

- Only a message's text parts are translated, and only in the user and
  assistant messages. A tool's arguments are code, and a tool's output is
  what a command said.
- A client that asked for structured output is not translated at all: neither
  its prompt nor its answer. Its answer is JSON, and a translation of it is
  neither valid JSON nor the JSON the client asked for, so a translated
  prompt alone would leave the client answered in a language it did not ask
  in. The prompt is left as it came, so the model answers in the client's own
  language and the JSON's values are in it too.
- A request whose texts are translated is not relayed as written. A native
  compaction and a sealed subagent's task keep their relay, so their texts
  are not translated.
- A request that goes to Codex's own sign-in (the native path
  `/backend-api/codex`) is not translated: that path sends the request as it
  came and never builds the IR. Neither is Codex's own turn served on
  magpie's accounts when its sign-in is too busy (the fallback), nor the
  summary magpie asks of a model for a compaction: the IR drops the sealed
  reasoning and the compaction items Codex needs.
- A text that is already in the language wanted is left as it is, told by
  the script the plan named.
- Both ends of a translation are kept, so a text a translation produced
  finds the text it was made from. A conversation's history therefore costs
  no call.
- A failure leaves the request as it was: no plan, a model that is not
  served, a model that fails or is late, or an answer whose count is not the
  count asked for. Half a translation is worse than none.
- A request magpie makes of itself is not translated: the plan rides on the
  agent's request's context, and it is taken off the context of every call
  magpie makes of itself — the search, the vision stand-in, the advisor, the
  router, the translation itself, the Codex titles, the Codex predictions
  and the compaction's summary — so those calls go as they came. The
  User-Agent of magpie's own translations is no longer a gate: a client that
  sends it is translated all the same, and the ledger tells a translation's
  row from the turn's own by it.
- The wait is `TranslateTimeout`, 30 seconds by default and at most 300. It is
  set in Settings › Models › Translation, and from the command line
  (`magpie settings set translateTimeout <seconds>`).

## Verification

```sh
go test -tags nogui ./internal/gateway -run 'TestTranslat' -count=1
go test -tags nogui ./internal/gateway -run 'TestMagpiesOwnCall|TestTheTranslation|TestTheTranslatesUserAgent|TestTheCodexTitle|TestThePluginPlan' -count=1
go test -tags nogui ./internal/gateway -run 'TestTheSearchsReply' -count=1
go test -tags nogui ./internal/plugin -run 'TestGatewayHookTranslate' -count=1
go test -tags nogui ./internal/settings -run TestTranslateTimeout -count=1
```

The plugin's hooks need bun on `PATH`: `testenv.Bun` skips the hook tests
otherwise, and a skipped test is not a passing one.
