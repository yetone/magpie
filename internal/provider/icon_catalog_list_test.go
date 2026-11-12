package provider

import "testing"

// A preset names several catalogs; each one's models get its logo
// (zzstar101 on X: step-5-preview of "stepfun-ai" had none of StepFun's).
func TestIconForCatalogFindsAListedCatalog(t *testing.T) {
	for id, want := range map[string]string{
		"stepfun-ai": "stepfun", "stepfun-ai-step-plan": "stepfun",
		"stepfun": "stepfun", "stepfun-step-plan": "stepfun",
		"xiaomi": "mimocode", "xiaomi-token-plan-cn": "mimocode",
		"openai": "openai", "azure": "azure-color", "": "",
	} {
		if got := IconForCatalog(id); got != want {
			t.Errorf("IconForCatalog(%q) = %q, want %q", id, got, want)
		}
	}
}
