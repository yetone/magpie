package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// makerOwnCatalog is models.dev's api.json as magpie had it on 2026-10-29,
// these entries byte for byte. DeepSeek sells V4.1 Flash 1017 as
// deepseek-flash at $0.15/$0.60 and Mistral resells it, by a dashed id, at
// $0.30/$1.20; Mistral resells Zhipu's GLM-5.2 too. Alibaba lists Qwen3.7
// Max with no canonical and Alibaba (China) with one, at a price of its
// own. StepFun's global API and its China one each list Step 5 Preview,
// and its Step Plan lists it with no price. Meta, which no maker preset
// is, lists Muse Spark 1.2 and a contributor edition; NVIDIA lists its own
// Nemotron at $0.
const makerOwnCatalog = `
{"deepseek":{"id":"deepseek","models":{
"deepseek-flash":{"id":"deepseek-flash","name":"DeepSeek V4.1 Flash 1017","description":"October 17 update of DeepSeek V4.1 Flash with improved reasoning, coding, agentic tasks, and visual understanding","family":"deepseek-flash","attachment":true,"reasoning":true,"reasoning_options":[{"type":"toggle"},{"type":"effort","values":["low","high","max"]}],"tool_call":true,"interleaved":{"field":"reasoning_content"},"structured_output":true,"temperature":true,"knowledge":"2025-05","release_date":"2026-10-17","last_updated":"2026-10-17","modalities":{"input":["text","image"],"output":["text"]},"open_weights":true,"limit":{"context":1048576,"output":393216},"cost":{"input":0.15,"output":0.6,"reasoning":0.6,"cache_read":0.003},"canonical_model_id":"deepseek/deepseek-v4.1-flash-1017"},
"deepseek-v4-pro":{"id":"deepseek-v4-pro","name":"DeepSeek V4 Pro","description":"DeepSeek V4 Pro snapshot with million-token context and support for thinking and non-thinking modes","family":"deepseek-thinking","attachment":false,"reasoning":true,"reasoning_options":[{"type":"toggle"},{"type":"effort","values":["low","high","max"]}],"tool_call":true,"interleaved":{"field":"reasoning_content"},"structured_output":true,"temperature":true,"release_date":"2026-08-13","last_updated":"2026-08-22","modalities":{"input":["text"],"output":["text"]},"open_weights":true,"limit":{"context":1048576,"output":393216},"cost":{"input":0.66,"output":1.98,"reasoning":1.98,"cache_read":0.022},"canonical_model_id":"deepseek/deepseek-v4-pro-0813"}}},
"mistral":{"id":"mistral","models":{
"deepseek-v4-1-flash-1017":{"id":"deepseek-v4-1-flash-1017","name":"DeepSeek V4.1 Flash 1017","description":"October 17 update of DeepSeek V4.1 Flash with improved reasoning, coding, agentic tasks, and visual understanding","family":"deepseek-flash","attachment":false,"reasoning":true,"reasoning_options":[{"type":"effort","values":["none","low","high","max"]}],"tool_call":true,"structured_output":true,"temperature":true,"knowledge":"2025-05","release_date":"2026-10-17","last_updated":"2026-10-17","modalities":{"input":["text"],"output":["text"]},"open_weights":true,"limit":{"context":1048576,"output":128000},"cost":{"input":0.3,"output":1.2,"cache_read":0.007},"canonical_model_id":"deepseek/deepseek-v4.1-flash-1017"},
"glm-5-2":{"id":"glm-5-2","name":"GLM-5.2","description":"Open flagship GLM for long-horizon coding agents and million-token context work","family":"glm","attachment":false,"reasoning":true,"reasoning_options":[{"type":"effort","values":["none","high","max"]}],"tool_call":true,"structured_output":true,"temperature":true,"release_date":"2026-06-13","last_updated":"2026-06-13","modalities":{"input":["text"],"output":["text"]},"open_weights":true,"limit":{"context":1048576,"output":131072},"status":"deprecated","cost":{"input":1.4,"output":4.4,"cache_read":0.14},"canonical_model_id":"zhipuai/glm-5.2"}}},
"zhipuai":{"id":"zhipuai","models":{
"glm-5.2":{"id":"glm-5.2","name":"GLM-5.2","description":"Open flagship GLM for long-horizon coding agents and million-token context work","family":"glm","attachment":false,"reasoning":true,"reasoning_options":[{"type":"toggle"},{"type":"effort","values":["high","max"]}],"tool_call":true,"interleaved":{"field":"reasoning_content"},"structured_output":true,"temperature":true,"release_date":"2026-06-13","last_updated":"2026-06-13","modalities":{"input":["text"],"output":["text"]},"open_weights":true,"limit":{"context":1000000,"output":131072},"cost":{"input":1.19516,"output":4.18306,"cache_read":0.29879,"cache_write":0},"canonical_model_id":"zhipuai/glm-5.2"}}},
"alibaba":{"id":"alibaba","models":{
"qwen3.7-max":{"id":"qwen3.7-max","name":"Qwen3.7 Max","description":"Qwen frontier model tuned for agent frameworks, coding assistants, and long tasks","family":"qwen","attachment":false,"reasoning":true,"reasoning_options":[{"type":"toggle"},{"type":"budget_tokens"}],"tool_call":true,"structured_output":true,"temperature":true,"release_date":"2026-05-21","last_updated":"2026-05-21","modalities":{"input":["text"],"output":["text"]},"open_weights":false,"limit":{"context":1000000,"output":131072},"cost":{"input":2.5,"output":7.5,"cache_read":0.5,"cache_write":3.125}}}},
"alibaba-cn":{"id":"alibaba-cn","models":{
"qwen3.7-max":{"id":"qwen3.7-max","name":"Qwen3.7 Max","description":"Qwen frontier model tuned for agent frameworks, coding assistants, and long tasks","family":"qwen","attachment":false,"reasoning":true,"reasoning_options":[{"type":"toggle"},{"type":"budget_tokens","max":262144}],"tool_call":true,"structured_output":true,"temperature":true,"release_date":"2026-05-21","last_updated":"2026-05-21","modalities":{"input":["text"],"output":["text"]},"open_weights":false,"limit":{"context":1000000,"output":131072},"cost":{"input":1.65,"output":4.951,"cache_read":0.33,"cache_write":2.0625},"canonical_model_id":"alibaba/qwen3.7-max"}}},
"stepfun-ai":{"id":"stepfun-ai","models":{
"step-5-preview":{"id":"step-5-preview","name":"Step 5 Preview","description":"StepFun's next-generation flagship base model for coding and professional knowledge work, with native text, image, and video input and a 1M-token context window","attachment":true,"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high"]}],"tool_call":true,"interleaved":{"field":"reasoning_content"},"structured_output":true,"temperature":true,"release_date":"2026-09-16","last_updated":"2026-09-20","modalities":{"input":["text","image","video"],"output":["text"]},"open_weights":true,"limit":{"context":1024000,"input":1024000,"output":65536},"cost":{"input":1,"output":2.7,"cache_read":0.1},"canonical_model_id":"stepfun/step-5-preview"}}},
"stepfun":{"id":"stepfun","models":{
"step-5-preview":{"id":"step-5-preview","name":"Step 5 Preview","description":"StepFun's next-generation flagship base model for coding and professional knowledge work, with native text, image, and video input and a 1M-token context window","attachment":true,"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high"]}],"tool_call":true,"interleaved":{"field":"reasoning_content"},"structured_output":true,"temperature":true,"release_date":"2026-09-16","last_updated":"2026-09-20","modalities":{"input":["text","image","video"],"output":["text"]},"open_weights":true,"limit":{"context":1000000,"input":1000000,"output":65536},"cost":{"input":1.04613,"output":2.98895,"cache_read":0.10461},"canonical_model_id":"stepfun/step-5-preview"}}},
"stepfun-ai-step-plan":{"id":"stepfun-ai-step-plan","models":{
"step-5-preview":{"id":"step-5-preview","name":"Step 5 Preview","description":"StepFun's next-generation flagship base model for coding and professional knowledge work, with native text, image, and video input and a 1M-token context window","attachment":true,"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","medium","high"]}],"tool_call":true,"interleaved":{"field":"reasoning_content"},"structured_output":true,"temperature":true,"release_date":"2026-09-16","last_updated":"2026-09-20","modalities":{"input":["text","image","video"],"output":["text"]},"open_weights":true,"limit":{"context":1000000,"input":1000000,"output":65536},"canonical_model_id":"stepfun/step-5-preview"}}},
"meta":{"id":"meta","models":{
"muse-spark-1.2":{"id":"muse-spark-1.2","name":"Muse Spark 1.2","description":"Muse Spark 1.2 is a coding-focused update to Muse Spark 1.1 with improvements in code generation, complex debugging, codebase understanding, and end-to-end developer workflows.","family":"muse","attachment":true,"reasoning":true,"reasoning_options":[{"type":"effort","values":["minimal","low","medium","high","xhigh"]}],"tool_call":true,"structured_output":true,"temperature":true,"release_date":"2026-08-05","last_updated":"2026-08-05","modalities":{"input":["text","image","video","pdf","audio"],"output":["text"]},"open_weights":false,"limit":{"context":1048576,"output":131072},"cost":{"input":1.25,"output":4.25,"cache_read":0.15},"canonical_model_id":"meta/muse-spark-1.2"},
"muse-spark-1.2-contributor":{"id":"muse-spark-1.2-contributor","name":"Muse Spark 1.2 Contributor","description":"Muse Spark 1.2 is a coding-focused update to Muse Spark 1.1 with improvements in code generation, complex debugging, codebase understanding, and end-to-end developer workflows.","family":"muse","attachment":true,"reasoning":true,"reasoning_options":[{"type":"effort","values":["minimal","low","medium","high","xhigh"]}],"tool_call":true,"structured_output":true,"temperature":true,"release_date":"2026-08-05","last_updated":"2026-08-05","modalities":{"input":["text","image","video","pdf","audio"],"output":["text"]},"open_weights":false,"limit":{"context":1048576,"output":131072},"cost":{"input":0.1,"output":0.2,"cache_read":0.002},"canonical_model_id":"meta/muse-spark-1.2"}}},
"nvidia":{"id":"nvidia","models":{
"nvidia/nemotron-3-ultra-550b-a55b":{"id":"nvidia/nemotron-3-ultra-550b-a55b","name":"Nemotron 3 Ultra 550B A55B","description":"Largest Nemotron 3 model for maximum open-weight reasoning and agent accuracy","family":"nemotron","attachment":false,"reasoning":true,"reasoning_options":[{"type":"effort","values":["none","medium","high"]},{"type":"budget_tokens","min":-1,"max":32768}],"tool_call":true,"structured_output":true,"temperature":true,"knowledge":"2025-09","release_date":"2026-06-04","last_updated":"2026-06-04","modalities":{"input":["text"],"output":["text"]},"open_weights":true,"limit":{"context":1000000,"output":65536},"cost":{"input":0,"output":0},"canonical_model_id":"nvidia/nemotron-3-ultra-550b-a55b"}}}}`

func useMakerOwnCatalog(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(makerOwnCatalog), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// A subscription's call is estimated at the price the model's own maker
// lists it at, not at a reseller's that a maker preset lists before it
// (bestony on Discord: Command Code's calls costed at the official price).
// Command Code serves deepseek/deepseek-v4.1-flash-1017, which Mistral
// resells at twice DeepSeek's price, and Meta's muse-spark-1.2, which only
// Meta prices.
func TestMakerPriceIsTheMakersOwn(t *testing.T) {
	useMakerOwnCatalog(t)

	cc := Provider{ID: "commandcode-plan", Name: "Command Code"}
	for _, c := range []struct {
		id                string
		input, output, cr float64
	}{
		// DeepSeek's deepseek-flash, by its canonical, not Mistral's resale
		{"deepseek/deepseek-v4.1-flash-1017", 0.15, 0.6, 0.003},
		{"deepseek-v4-1-flash-1017", 0.15, 0.6, 0.003},
		// Zhipu's own, not Mistral's glm-5-2
		{"glm-5-2", 1.19516, 4.18306, 0.29879},
		{"zai-org/GLM-5.2", 1.19516, 4.18306, 0.29879},
		// the maker preset's own entry, as before, though only
		// alibaba-cn names the model's maker
		{"Qwen/Qwen3.7-Max", 2.5, 7.5, 0.5},
		// StepFun's global API, the preset asked first, not its plan's
		{"stepfun/Step-5-Preview", 1, 2.7, 0.1},
		// a lab no maker preset is
		{"meta/muse-spark-1.2", 1.25, 4.25, 0.15},
		{"meta/muse-spark-1.2-contributor", 0.1, 0.2, 0.002},
		// ids the maker lists as given keep their own prices
		{"deepseek/deepseek-v4-pro", 0.66, 1.98, 0.022},
	} {
		for name, price := range map[string]func(string) (catalog.Price, bool){"MakerPrice": MakerPrice, "ListPrice": cc.ListPrice} {
			pr, ok := price(c.id)
			if !ok || pr.Input != c.input || pr.Output != c.output || pr.CacheRead != c.cr {
				t.Errorf("%s(%q) = %+v, %v; want $%v/$%v/$%v", name, c.id, pr, ok, c.input, c.output, c.cr)
			}
		}
	}
	// a maker's own $0 entry is no price for the model
	if pr, ok := MakerPrice("nvidia/nemotron-3-ultra-550b-a55b"); ok {
		t.Errorf("MakerPrice(nemotron) = %+v; want unpriced", pr)
	}
	if pr, ok := MakerPrice("thinkingmachines/inkling"); ok {
		t.Errorf("MakerPrice(inkling) = %+v; want unpriced, no catalog lists it", pr)
	}
}

// TestMakerPriceKnownAllocatesNothing: the Providers list asks the maker's
// price of every model on each poll, under every maker catalog's name.
// A price asked before is answered without garbage; its memo key, longer
// than the memo's first buffer, was a new allocation on every ask.
func TestMakerPriceKnownAllocatesNothing(t *testing.T) {
	useMakerOwnCatalog(t)
	n := 2
	for _, c := range makerPriceCatalogs() {
		n += len(c) + 1
	}
	if n > 1024 {
		t.Errorf("a maker price's memo key is %d bytes, past pricedOnce's 1 KB buffer", n)
	}
	for _, id := range []string{"glm-5-2", "meta/muse-spark-1.2"} {
		makerListPrice(id)
		if a := testing.AllocsPerRun(50, func() { catalog.MakerPriced(makerPriceCatalogs(), id) }); a != 0 {
			t.Errorf("MakerPriced(%q) asked again made %v allocations, want 0", id, a)
		}
	}
}
