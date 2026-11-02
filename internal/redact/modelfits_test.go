package redact

import (
	"strings"
	"testing"
)

// What the local model took for personal data or a secret in real agent
// requests, and isn't: each text is as a Claude Code or Codex request had
// it on 10-25, with the piece the model reported. Measured over 3 MB of
// them, the model alone masked ~1,800 such pieces; the rules 17.
func TestModelMisreadsNotMasked(t *testing.T) {
	for _, c := range []struct{ text, value, label string }{
		// the count each turn carries
		{"<total_tokens>14881209 tokens left</total_tokens>", "14881209", "account_number"},
		// run ids, hashes and versions
		{"| 6123456789 | ci.yml | success |", "6123456789", "account_number"},
		{"ba319a758 gateway: the lone tool_choice none", "ba319a758", "account_number"},
		{"Update to v0.1.1442 now", "0.1.1442", "account_number"},
		{"a screenshot of 1624x1487 pixels", "1624x1487", "private_phone"},
		// a model's and a toolchain's names in a setting
		{`groqAPIModel = "llama3-70b-8192"`, "llama3-70b-8192", "secret"},
		{`claudeAPIModel = "claude-3-5-sonnet-20241022"`, "claude-3-5-sonnet-20241022", "secret"},
		{"--model mixtral-8x7b-32768 --max-tokens 4096", "mixtral-8x7b-32768", "secret"},
		{`channel = "nightly-2026-09-01"`, "nightly-2026-09-01", "secret"},
		// the model's and the assistant's own names
		{"You are powered by the model named Opus 5.5.", "Opus", "private_person"},
		{"Claude publishes it as an Artifact.", "Claude", "private_person"},
		{"Another Claude session holds the port.", "Another Claude", "private_person"},
		// a user name inside a path, a skill's name
		{"read /Users/yetone/workspace/projects/magpie/AGENTS.md", "yetone", "private_person"},
		{"- artifact-design: Design guidance", "artifact-design", "private_person"},
		// ids cut out of longer words
		{`"tool_use_id":"toolu_01XyZabcDEF123456"`, "XyZabcDEF123456", "secret"},
		{`"id":"srvtoolu_01AbCdEfGhIjKlMnOp"`, "srvtoolu_01AbCdEfGhIjKlMnOp", "secret"},
		{"<task-id>a97c9bc67cc1f1e27</task-id>", ">a97c9bc67cc1f1e27", "secret"},
		{"hex40 ok? 1 2f1d0c9b8a7e6f5d4c3b2a1908f7e6d5c4b3a291", "2f1d0c9b8a7e6f5d4c3b2a1908f7e6d5c4b3a291", "secret"},
		{"919585d1700e515ac38da49e31bc60a527fc15c2 ok? 1", "919585d1700e515ac38da49e31bc60a527fc15c2 ok? 1", "secret"},
		// names in Go's module cache, a path's piece
		{"/Users/x/Library/Caches/go-build/63700b7ab65fd1d3@v63", "63700b7ab65fd1d3@v63", "secret"},
		{"d6426ecaaf0a28de@v31", "d6426ecaaf0a28de@v31", "private_email"},
		{"P=/Users/yetone/.npm/_npx/1e7f6d9597241db0", "/yetone/.", "private_email"},
		// a piece of a base64 image
		{"iVBORw0KGgoAAAANSUhEUgAA+k5zM7Md7tM9ehxd51xF/isvM9", "k5zM7Md7tM9ehxd51xF", "secret"},
		// a grep alternation, a session's file, a link
		{"rg 'modelTakes|modelFits|personFits' internal", "modelTakes|modelFits|personFits", "secret"},
		{"wrote 31a4f944-e7cf-4f6f-acc7-eef50d9911dd.jsonl", "31a4f944-e7cf-4f6f-acc7-eef50d9911dd.jsonl", "secret"},
		{"join at discord.gg/Ab3dEf9h today", "discord.gg/Ab3dEf9h", "secret"},
		// from Claude Code sessions (10-26, a reporter's "95% wrong"): a
		// metric's decimals, a run's id, a background task's id, a model's
		// name, a file's, a grep's regexp, made ids with more words, a
		// transcript's path, a host's name, an acronym and products
		{"2026-10-21T08:20:00Z 104.21875033916251\n", "104.21875033916251", "private_phone"},
		{"2026-10-21T08:20:00Z 104.21875033916251\n", "104.21875033916251", "account_number"},
		{"see [run 48213097715](https://ci.example/runs/48213097715)", "run 48213097715", "private_phone"},
		{"env -u GH_TOKEN gh run view 48213097715 --log", "48213097715", "secret"},
		{"Command running in background with ID: kq3m8zx2t. Output is", "kq3m8zx2t", "secret"},
		{"moved to the background (ID: kq3m8zx2t).", "kq3m8zx2t", "secret"},
		{"<task-id>kq3m8zx2t</task-id>", "kq3m8zx2t", "secret"},
		{`{"backgroundTaskId":"kq3m8zx2t"}`, "kq3m8zx2t", "secret"},
		{"'litellm_model_name': 'qwen/qwen3-30b-a3b-instruct-2507'}", "qwen/qwen3-30b-a3b-instruct-2507", "secret"},
		{"-rw-r--r--  1 x  staff  690 Aug 20 19:58 make-icons.sh", "make-icons.sh", "secret"},
		{`grep -n 'glm-5\b\|glm/glm-4' notes`, `glm-5\b\|glm/glm-4`, "secret"},
		{`"gatewayRequestId":"req_gateway_7c41e09a5bd2f368"`, "req_gateway_7c41e09a5bd2f368", "secret"},
		{`{"ledgerId":"gl_Wq7rTb2xNe5YpLaZ"}`, `ledgerId":"gl_Wq7rTb2xNe5YpLaZ`, "secret"},
		{"- tb_Ra4Kp9Lm-ZqT2wXy_8Hs: release about 17:05Z", "tb_Ra4Kp9Lm-ZqT2wXy_8Hs", "secret"},
		{"transcript at: /Users/x/.claude/projects/-Users-x-work/7e3f9a12-4c6b.jsonl", "Users-x-work/7e3f9a12-4c6b.jsonl", "private_address"},
		{`"edge":"eu.stg.acme.io","score":false`, "eu.stg.acme.io", "private_email"},
		{"wait until CI is green", "CI", "private_person"},
		{"CodeRabbit\tpending\t0", "CodeRabbit", "private_person"},
		{"Greptile Review at https://app.greptile.com/r/1", "Greptile", "private_person"},
		// the method a call is made on: gctx.GetInt64 went as a made-up key
		// in the reporter's redaction shot (#4444)
		{`rows, err := gctx.GetInt64("user_id")`, "gctx.GetInt64", "secret"},
	} {
		f := &fakeModel{values: map[string]string{c.value: c.label}}
		if out, n := Mask(c.text, Options{Secrets: true, Personal: true, Model: f.find}); n != 0 {
			t.Errorf("%q as %s: masked %q", c.value, c.label, out)
		}
	}
}

// What the checks above leave out is still masked.
func TestModelFindsStillMasked(t *testing.T) {
	for _, c := range []struct{ text, value, label string }{
		{"my account is 13393848347348", "13393848347348", "account_number"},
		{"Ask Rina Kowalczyk about it", "Rina Kowalczyk", "private_person"},
		{"Ludwig van Beethoven wrote it", "Ludwig van Beethoven", "private_person"},
		{"call me on 13800138000 tonight", "13800138000", "private_phone"},
		{"ring 020 7946 0958 after six", "020 7946 0958", "private_phone"},
		{"send it to 42 Wallaby Way, Sydney", "42 Wallaby Way, Sydney", "private_address"},
		{"the vault phrase is Zq8vR2mT6wLp", "Zq8vR2mT6wLp", "secret"},
		// dashed words in lower case are a passphrase without a size or a date
		{"the wifi passphrase is correct-horse-battery-staple", "correct-horse-battery-staple", "secret"},
		{"password: maple-4-river-77", "maple-4-river-77", "secret"},
		{"api key: 9f2c4e1a7b3d5c8e9f2c4e1a7b3d5c8e", "9f2c4e1a7b3d5c8e9f2c4e1a7b3d5c8e", "secret"},
		{"write to rina.k@kowalczyk.pl", "rina.k@kowalczyk.pl", "private_email"},
		// beside what the checks above leave out
		{"the stripe one is sk_test_AbCdEfGh_IjKlMn", "sk_test_AbCdEfGh_IjKlMn", "secret"},
		// a real secret of dotted parts, where no rule reaches it
		{"the vault phrase is alpha.bravo.charlie9", "alpha.bravo.charlie9", "secret"},
		// one a bracket of words follows is no call, nor one after a colon
		// a comma follows (#4444's guard); values of their own, as a value
		// found once is masked again wherever it stands
		{"tell Bob it is Pw7nK4xQ9rTe (rotated today)", "Pw7nK4xQ9rTe", "secret"},
		{"tell Bob it is delta.echo.foxtrot7 (keep it)", "delta.echo.foxtrot7", "secret"},
		{"note: hunterTwoThree, keep it", "hunterTwoThree", "secret"},
		{"the token is 9f2c4e1a7b3d5c8e9f2c", "9f2c4e1a7b3d5c8e9f2c", "secret"},
		{"ask McDonald, he knows", "McDonald", "private_person"},
		{"write to Rina at rina.lee@kowalczyk.pl", "Rina", "private_person"},
		{"ring +1 415.555.0132 after six", "+1 415.555.0132", "private_phone"},
		{"we live at 3/12 Smith Street, Sydney", "3/12 Smith Street, Sydney", "private_address"},
	} {
		f := &fakeModel{values: map[string]string{c.value: c.label}}
		if out, _ := Mask(c.text, Options{Secrets: true, Personal: true, Model: f.find}); strings.Contains(out, c.value) {
			t.Errorf("%q as %s: left in %q", c.value, c.label, out)
		}
	}
}
