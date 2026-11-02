// magpie: written by magpie, and written again as its models change.
// magpie's decision models (System One) and image models (Settings ›
// Images), for codemode's models.classify() and models.generateImages().
// Pi's models.json holds chat models only, so they are registered here,
// as providers magpie-systemone and magpie-images. Take Pi off magpie in
// magpie to have this file gone; with this first line changed, magpie
// leaves the file to you.
const MAGPIE = __MAGPIE__;

export default async function (pi) {
	// a Pi older than its classifier models (1.x) has no such module, or a
	// System One provider without classify: it gets nothing from here
	let typesafe;
	try {
		const { builtinProviders } = await import("@earendil-works/pi-ai/providers/all");
		typesafe = builtinProviders().find((p) => p.id === "typesafe");
	} catch {
		return;
	}
	if (typeof typesafe?.classify !== "function") return;
	const cost = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };
	if (MAGPIE.classifiers.length)
		pi.registerProvider("magpie-systemone", {
			name: "magpie System One",
			baseUrl: MAGPIE.baseUrl,
			apiKey: MAGPIE.apiKey,
			// magpie's /v1/systemone takes TypeSafe's System One protocol for
			// every decision model, Cloudflare's and OpenRouter's too
			models: MAGPIE.classifiers.map((m) => ({ type: "classifier", api: "typesafe-system-one", id: m.id, name: m.name, input: ["text"], cost, contextWindow: 64000 })),
			classifiers: {
				"typesafe-system-one": { classify: (model, context, options) => typesafe.classify(model, context, options) },
			},
		});
	if (MAGPIE.images.length)
		pi.registerProvider("magpie-images", {
			name: "magpie Images",
			baseUrl: MAGPIE.baseUrl,
			apiKey: MAGPIE.apiKey,
			models: MAGPIE.images.map((m) => ({ type: "image", api: "magpie-images", id: m.id, name: m.name, input: m.input, output: ["image"], cost })),
			images: { "magpie-images": { generateImages: draw } },
		});
}

// draw asks magpie's images API (POST …/v1/images/generations) for the
// input's text, with its images as references.
async function draw(model, context, options) {
	const out = { api: model.api, provider: model.provider, model: model.id, output: [], stopReason: "stop", timestamp: Date.now() };
	try {
		const prompt = context.input.filter((b) => b.type === "text").map((b) => b.text).join("\n");
		const images = context.input.filter((b) => b.type === "image").map((b) => `data:${b.mimeType};base64,${b.data}`);
		const body = { model: model.id, prompt, response_format: "b64_json" };
		if (images.length) body.images = images;
		const res = await (options?.fetch ?? fetch)(new URL("images/generations", `${model.baseUrl.replace(/\/+$/u, "")}/`), {
			method: "POST",
			headers: { "content-type": "application/json", authorization: `Bearer ${options?.apiKey ?? ""}`, ...options?.headers },
			body: JSON.stringify(body),
			signal: options?.signal,
		});
		const text = await res.text();
		let got;
		try {
			got = JSON.parse(text);
		} catch {
			got = undefined;
		}
		if (!res.ok) throw new Error(`magpie answered ${res.status}: ${got?.error?.message ?? text.slice(0, 500)}`);
		if (got?.text) out.output.push({ type: "text", text: got.text });
		for (const d of got?.data ?? []) {
			if (d.b64_json) out.output.push({ type: "image", data: d.b64_json, mimeType: d.mime_type ?? "image/png" });
			else if (d.url) out.output.push({ type: "text", text: d.url });
		}
		const u = got?.usage;
		if (u) {
			const input = u.input_tokens ?? 0;
			const output = u.output_tokens ?? 0;
			out.usage = { input, output, cacheRead: 0, cacheWrite: 0, totalTokens: u.total_tokens ?? input + output, cost: { ...cost0, total: 0 } };
		}
	} catch (error) {
		out.stopReason = options?.signal?.aborted ? "aborted" : "error";
		out.errorMessage = error instanceof Error ? error.message : String(error);
	}
	return out;
}

const cost0 = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };
