// The add sheet's Partners: the services that pay to be listed first in
// magpie's "Add provider" sheet, under a heading that says they sponsor
// magpie. The app fetches this list from /api/partners every six hours and
// keeps it (internal/provider/partners.go), so a change here reaches users
// with `npx wrangler deploy`, no release. At most 12 are listed, in this
// order.
//
// An entry is a preset (internal/provider/presets.go) and a few fields more:
//
//   id         a-z, 0-9 and "-", 2-41 characters; never a built-in preset's
//              id. It is kept in the providers users add from it: don't
//              change it, and don't give it to someone else later.
//   name       the row's name;   short  a shorter one, when name is long
//   chat       OpenAI chat completions base URL (…/v1)
//   responses  OpenAI Responses base URL;   anthropic  Anthropic messages base URL
//              (at least one of the three, or regions)
//   regions    [{id, name, chat, responses, anthropic, keysUrl, website}]: the
//              first is the default
//   keysUrl    where the user makes a key (a referral link is fine)
//   website    the service's home page
//   iconUrl    its logo (png, svg, webp, jpeg; square), fetched and kept by
//              each app
//   notes      the tagline by language: {en, zh, "zh-TW", ja, de}; en stands
//              in for a language without one
//   langs      the app languages it is listed in (["zh"]); none is all
//   from, until  RFC 3339 times bounding when it is listed (the paid term)
//
// Every URL is https. An entry that breaks a rule is left out by the app.
export const PARTNERS = [];
