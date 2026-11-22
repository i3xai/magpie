# magpie-prompt-translation

A request's texts are translated into another language before the model
sees them, and the model's reply translated back, so a client that asks in
Chinese can talk to a model that answers in English.

This package is the policy and nothing else: it says which model to ask,
which two languages, and the prompt the texts go through. The gateway asks
the model itself, in process, so the plugin holds no key and never calls a
vendor. The wait a translation is given is set in Settings › Models ›
Translation (30 seconds by default, at most 300), or from the command line
with `magpie settings set translateTimeout <seconds>`.

The reference is [Prompt translation](../../docs/subsystems/gateway-translation.md);
the hook itself is described in [Asynchronous gateway request
hooks](../../docs/subsystems/gateway-request-hooks.md).

## What it costs

One call to the model that translates, for the texts of a turn that need it.
The reply's texts cost one more, and both ends of a translation are kept:
the Chinese the client sees is found again in the cache and looked back up,
so the next turn's history costs no call and only the new question is
asked about. The cache lives in the process, so the first turn after a
restart pays for the history once.

Every translation lands in the usage rows on its own, under the purpose
**Translation** (the kind `translate`), so what the feature cost is
visible in Usage › … and in `magpie usage --csv`.

## Installing

```sh
magpie plugin add /path/to/magpie-prompt-translation
```

The plugin runs on Bun, which magpie downloads itself the first time a
plugin needs it. To remove it again:

```sh
magpie plugin rm magpie-prompt-translation
```

## Configuring

The package translates right away with the settings written into it: the
model it asks, the two languages and the prompt. A user with another model
names it in the editor, and the editor starts from the package's own
suggestion.

The settings are this package's options. Edit them in Settings › Plugins ›
magpie-prompt-translation's JSON options; what is set there overrides what
is written in the hook.

| Option | What it is |
| --- | --- |
| `model` | The model to ask, as magpie names it. It must be one magpie can route; the default is `gemini-3-flash`, which most readers will have to change. |
| `from` | The language the request comes in, as the plugin names it (`zh`). |
| `to` | The language the model is asked in (`en`). |
| `fromName` | The language as the prompt names it (`Chinese`); it fills `{from}`. |
| `toName` | The language as the prompt names it (`English`); it fills `{to}`. |
| `fromScript` | How the first language is written: `cjk` for Han ideographs, `latin` for the Latin alphabet. |
| `toScript` | How the second language is written. |
| `system` | The prompt the texts go through. `{from}` and `{to}` are filled with the language names. |
| `only` | A model name: only requests going to that model are translated. Empty, every model's is. |

A text that is already in the language wanted is left as it is, told by the
script: a prompt with no Han ideographs is not translated into English
again. An empty `toScript` means the gateway does not know how that
language is written, and every text is translated.

A plan that names no model, no language or no prompt is no plan: nothing is
translated and the request goes as it came. So is a model that is not
served, one that fails, and one that answers too late — a half translation
would corrupt the conversation, and the whole batch is refused instead.

## Trying it

Send a request in Chinese and read the reply:

```sh
curl -s http://127.0.0.1:3425/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $MAGPIE_KEY" \
  -d '{"model":"<a model you talk to>","messages":[{"role":"user","content":"用一句话说明光合作用。"}]}'
```

Three things say the feature is on:

1. The reply comes back in Chinese, even though the model answered in
   English.
2. `magpie usage --csv` shows a row of its own for the translation, with the
   agent `magpie-translate/1`.
3. The next turn, carrying the Chinese history, costs one translation
   again, not two: the history was found in the cache.

To see what the model itself received, ask it to repeat the question it
was given: it repeats it in English.

## What it does not translate

- **A client that asked for structured output.** Neither its prompt nor
  its answer: the answer is JSON, and a translation of it is neither valid
  JSON nor the JSON the client asked for. A translated prompt alone would
  leave the client answered in a language it did not ask in.
- **Codex's own path** (`/backend-api/codex`), the fallback when Codex's
  sign-in is too busy, and the summary magpie asks of a model for a
  compaction: those send the request as it came and never build the
  request the gateway translates.
- **The texts that are not a message's own**: the system prompt, the
  tools, the tool calls, the tool results and the reasoning are left as
  they are. A tool's arguments are code, and a tool's output is what a
  command said.

A request whose texts are translated is not relayed as written: it goes
through the request the gateway builds, so the Chat protocol's `store`,
`metadata` and `user` are lost on that path, and the Anthropic protocol's
`cache_control` and `metadata` survive.

## The hook

```js
export async function onTranslate(ctx) {
  return {
    model: "gemini-3-flash",                 // the model to ask
    from: "zh", to: "en",
    fromName: "Chinese", toName: "English",
    fromScript: "cjk", toScript: "latin",
    system: "Translate the following {from} text into {to}. Output only the translation.",
  };
}
```

`ctx` is the request's own context, with `model` the model the request
goes to, and `options` this package's options. Returning null leaves the
request as it came, and the next plugin is free to give a plan of its own.
