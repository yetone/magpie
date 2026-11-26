// The magpie flaps its wing, flicks its tail and bobs when the pointer comes
// over it, as the app's logo does (internal/gui/assets/app.css, .logo, with
// the same clip paths and keyframes). Each <img> of the bird becomes the same
// drawing inline, cut into body, tail and wing; at rest it is drawn whole, so
// the cuts never show. The nav's bird answers the whole brand link, the way
// the app's answers its corner. Without a pointer that hovers, or with
// reduced motion asked for, the images are left as they are.
(() => {
  if (window.magpieBird) return;
  window.magpieBird = true;
  if (!matchMedia("(hover: hover) and (pointer: fine)").matches || matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  const imgs = [...document.querySelectorAll('img[src$="/img/magpie.svg"], img[src$="/img/magpie-small.svg"]')];
  if (!imgs.length) return;

  const NS = "http://www.w3.org/2000/svg";
  const WING = "M29.8 18.3L26 19L22 21L17 23.5L14.6 24.7L10.6 26.4L6 29V32L9 31.5L12.5 32L16.4 31.8L19 31.4L24 28.8L28.5 24L30.6 21.4Z";
  const ease = "cubic-bezier(.3, 0, .3, 1)";
  const style = document.createElement("style");
  style.textContent = `
svg.mb { overflow: visible; }
.mb .parts, .mb.wag .whole { display: none; }
.mb.wag .parts { display: inline; }
.mb .tail { transform-box: view-box; transform-origin: 14px 22px; }
.mb .wing { transform-box: view-box; transform-origin: 30px 19.8px; }
.mb .mb-b { transform-box: view-box; transform-origin: 26px 38px; }
.mb.wag .tail { animation: mb-tail .9s ${ease}; }
.mb.wag .wing { animation: mb-wing .9s ${ease}; }
.mb.wag .mb-b { animation: mb-bob .9s ${ease}; }
@keyframes mb-wing { 0%, 100% { transform: none; } 12% { transform: rotate(10deg); } 26% { transform: rotate(-3deg); } 40% { transform: rotate(8deg); } 56% { transform: rotate(-2deg); } 72% { transform: rotate(3deg); } }
@keyframes mb-tail { 0%, 100% { transform: none; } 18% { transform: rotate(16deg); } 36% { transform: rotate(-7deg); } 54% { transform: rotate(9deg); } 72% { transform: rotate(-3deg); } 86% { transform: rotate(2deg); } }
@keyframes mb-bob { 0%, 100% { transform: none; } 18% { transform: rotate(-4deg); } 40% { transform: rotate(2deg); } 62% { transform: rotate(-1deg); } }`;
  document.head.append(style);

  // One hidden set of defs: each drawing once, and the cuts every bird shares.
  const defs = document.createElementNS(NS, "svg");
  defs.setAttribute("aria-hidden", "true");
  defs.style.cssText = "position:absolute;width:0;height:0;overflow:hidden";
  defs.innerHTML = `<defs><clipPath id="mb-body"><path clip-rule="evenodd" d="M16 0H44V44H0V26.4H10.6L13 24.2L16 19Z ${WING}"/></clipPath><clipPath id="mb-tail"><path d="M0 0H17L17.2 19.6L14 25L10.6 26.4H0Z"/></clipPath><clipPath id="mb-wing"><path d="${WING}"/></clipPath></defs>`;
  document.body.append(defs);

  const drawings = new Map(); // src → id of its path in defs
  const drawing = (src) => {
    if (!drawings.has(src)) {
      drawings.set(src, fetch(src).then((r) => (r.ok ? r.text() : Promise.reject())).then((text) => {
        const path = new DOMParser().parseFromString(text, "image/svg+xml").querySelector("path");
        if (!path || !path.getAttribute("d")) throw new Error("no path");
        const id = `mb-${drawings.size}-${src.includes("small") ? "s" : "l"}`;
        const p = document.createElementNS(NS, "path");
        p.id = id;
        p.setAttribute("fill-rule", path.getAttribute("fill-rule") || "evenodd");
        p.setAttribute("d", path.getAttribute("d"));
        defs.firstChild.append(p);
        return id;
      }));
    }
    return drawings.get(src);
  };

  for (const img of imgs) {
    drawing(img.getAttribute("src")).then((id) => {
      const svg = document.createElementNS(NS, "svg");
      svg.setAttribute("viewBox", "0 0 44 44");
      svg.setAttribute("aria-hidden", "true");
      svg.setAttribute("class", `${img.className} mb`.trim());
      const use = (clip) => `<use href="#${id}"${clip ? ` clip-path="url(#mb-${clip})"` : ""}/>`;
      svg.innerHTML = `<g class="mb-b"><g class="whole">${use()}</g><g class="parts">${use("body")}<g class="tail">${use("tail")}</g><g class="wing">${use("wing")}</g></g></g>`;
      img.replaceWith(svg);
      const wag = () => {
        if (svg.classList.contains("wag")) return;
        svg.classList.add("wag");
        svg.querySelector(".tail").addEventListener("animationend", () => svg.classList.remove("wag"), { once: true });
      };
      (svg.closest(".brand") || svg).addEventListener("mouseenter", wag);
    }, () => {}); // the picture stays an image
  }
})();
