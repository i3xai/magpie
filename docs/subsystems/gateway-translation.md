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
   and the reasoning are left as they are.
4. The provider is asked. Its reply is decoded into the IR's events.
5. A streamed reply's text is held by `translateEncoder` until the event that
   comes after it, and translated then: the text goes first and the events
   that came after it wait for it, so the client reads the answer in the order
   the model gave it. A text that comes after a tool call is held and
   translated on its own. A whole reply's text parts are translated before it
   is rendered.
6. The translation is asked of the model the plan names, through the gateway
   itself (`askChatCtx`), so it is counted in the usage rows under its own
   kind and cannot ask the plugins again.

## Constraints and failure behavior

- Only a message's text parts are translated, and only in the user and
  assistant messages. A tool's arguments are code, and a tool's output is
  what a command said.
- A client that asked for structured output is not translated: its answer is
  JSON, and a translation of it is neither valid JSON nor the JSON the client
  asked for.
- A request whose texts are translated is not relayed as written. A native
  compaction and a sealed subagent's task keep their relay, so their texts
  are not translated.
- A request that goes to Codex's own sign-in (the native path
  `/backend-api/codex`) is not translated: that path sends the request as it
  came and never builds the IR.
- A text that is already in the language wanted is left as it is, told by
  the script the plan named.
- Both ends of a translation are kept, so a text a translation produced
  finds the text it was made from. A conversation's history therefore costs
  no call.
- A failure leaves the request as it was: no plan, a model that is not
  served, a model that fails or is late, or an answer whose count is not the
  count asked for. Half a translation is worse than none.
- The translation's own call is not translated: it is made with the request's
  own context, and the plan rides on that context, so a request whose agent
  is the translation's is told apart and left alone (`magpiesOwnTranslation`).
- The wait is `TranslateTimeout`, 30 seconds by default and at most 300.

## Verification

```sh
go test -tags nogui ./internal/gateway -run 'TestTranslat' -count=1
go test -tags nogui ./internal/plugin -run 'TestGatewayHookTranslate' -count=1
go test -tags nogui ./internal/settings -run TestTranslateTimeout -count=1
```
