// A request's texts are translated into another language before the model
// sees them, and the model's reply translated back. This file is the whole
// policy: which model to ask, which two languages, and the prompt the
// texts go through. The gateway asks the model itself, so nothing here
// holds a key and the plugin never calls a vendor.
//
// The settings are this package's own options, which the user edits in
// Settings › Plugins. They override what is written here, so a fresh
// install translates right away and a user with another model names it in
// the editor. A plan that names no model, no language or no prompt is no
// plan: the request then goes as it came, and nothing is translated.
const defaults = {
  model: "gemini-3-flash",
  from: "zh",
  to: "en",
  fromName: "Chinese",
  toName: "English",
  fromScript: "cjk",
  toScript: "latin",
  system: "Translate the following {from} text into {to}. Keep code, identifiers, numbers and formatting as they are. Output only the translation.",
};

export async function onTranslate(ctx) {
  const o = ctx.options ?? {};
  const text = (key) => {
    const v = o[key];
    if (typeof v === "string" && v.trim()) return v.trim();
    return defaults[key] ?? "";
  };
  const model = text("model");
  const from = text("from");
  const to = text("to");
  const system = text("system");
  if (!model || !from || !to || !system) return null;
  // The model the request goes to: a plugin may translate for some models
  // and leave the others alone. "only" has no default: empty, every
  // model's is translated.
  const only = text("only");
  if (only && ctx.model && ctx.model !== only) return null;
  return {
    model,
    from,
    to,
    // The languages as the prompt names them: they fill {from} and {to}.
    fromName: text("fromName") || from,
    toName: text("toName") || to,
    // How a language is written, so the gateway knows a text that is
    // already in the language wanted: "cjk" for one written with Han
    // ideographs, "latin" for one written with the Latin alphabet. An
    // empty one is a language the gateway does not know, and a text in it
    // is translated always.
    fromScript: text("fromScript"),
    toScript: text("toScript"),
    system,
  };
}
