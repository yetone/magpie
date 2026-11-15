// node --test site/: the Docker image's tags the docs name are tags a
// workflow writes. magpie releases many times a day, and a Renovate user
// had a PR for each one (YaoFur on X): daily is the Daily channel's
// release, moved once a day by .github/workflows/docker-daily.yml, and is
// in the docs in every language.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";

const read = (p) => readFileSync(new URL(p, import.meta.url), "utf8");
const workflows = readdirSync(new URL("../.github/workflows/", import.meta.url))
  .filter((f) => /\.ya?ml$/.test(f))
  .map((f) => [f, read("../.github/workflows/" + f)]);

// the floating tags in the Tags row of the docs' table (a version's tags,
// 0.1.1150 and 0.1, are left out)
function tagsIn(page) {
  const row = read(page).split("\n").find((l) => /<tr><td><b>(Tags|标签|タグ)<\/b>/.test(l));
  assert.ok(row, page + ": no Tags row");
  return [...row.matchAll(/<code>([^<]+)<\/code>/g)].map((m) => m[1]).filter((c) => !/^\d/.test(c) && c !== "main").sort();
}

const pages = ["./public/docs/docker.html", "./public/docs/zh/docker.html", "./public/docs/ja/docker.html"];

test("every language's docs name the same tags, daily among them", () => {
  const en = tagsIn(pages[0]);
  assert.ok(en.includes("daily"), "daily isn't in the Tags row: " + en);
  for (const p of pages.slice(1)) assert.deepEqual(tagsIn(p), en, p);
});

test("every tag the docs name is one a workflow writes", () => {
  for (const tag of tagsIn(pages[0])) {
    const by = workflows.filter(([, y]) => y.includes(`"$IMAGE:${tag}"`)).map(([f]) => f);
    assert.ok(by.length, `no workflow writes $IMAGE:${tag}`);
  }
});

test("daily follows the Daily channel's feed, without a build", () => {
  const y = read("../.github/workflows/docker-daily.yml");
  // the feed moves once a day (site/worker.js's keepDaily); the workflow
  // only copies the image it names, by version
  assert.match(y, /FEED: https:\/\/usemagpie\.ai\/api\/latest\?channel=daily/);
  assert.match(y, /imagetools create -t "\$IMAGE:daily" "\$IMAGE:\$V"/);
  assert.doesNotMatch(y, /\$IMAGE:latest/);
  assert.doesNotMatch(y, /build-push-action|docker (buildx )?build\s/);
  assert.doesNotMatch(y, /push:|tags:/, "never a push of its own");
});
