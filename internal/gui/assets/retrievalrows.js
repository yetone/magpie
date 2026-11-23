// A provider's embedding and rerank models (heatonz on Discord, @rfiacne
// and solider1394 on X): agents don't chat with them, so they aren't among
// its Models and need no pick. Each kind is listed by id with the path it
// is asked on, and for a saved provider a model id to send there. The
// editor draws them as rows of their own, the add form's Fetch models
// (inline) under its list. app.js imports this as either draws them
// (retrievalRows), so it weighs nothing on the core.
window.retrievalField = function (p, inline) {
  return [["Embedding models", p.embedIds, "/v1/embeddings"], ["Rerank models", p.rerankIds, "/v1/rerank"]].flatMap(([l, ids, path]) => {
    if (!ids?.length) return [];
    const d = el("div", "mchips still");
    d.append(...ids.map((id) => el("span", "mchip", id)));
    const how = p.id ? t("Call them on {path}, e.g. as {model}", { path, model: p.id + "/" + ids[0] }) : t("Call them on {path}", { path });
    if (!inline) return field(t(l), d, how);
    const g = el("div", "rgroup");
    g.append(el("div", "rkind", t(l)), d, el("div", "hint", how));
    return [g];
  });
};
