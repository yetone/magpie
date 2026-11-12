package gateway

import "testing"

// Huawei CodeArts answers a request parameter it can't take (a `stop` array,
// #6342) with a 400 that names a code only: the request's content, not the
// account, so the next member is asked and nobody rests.
func TestCodeArtsInvalidParamIsTheRequests(t *testing.T) {
	body := `{"error":{"message":"华为云 CodeArts: The request param is invalid, Please check it (InferHub.001001005.400)"}}`
	if !shapeRefused(400, []byte(body)) {
		t.Errorf("no next member over: %s", body)
	}
	if shapeRefused(400, []byte(`{"error":{"message":"InferHub.4004.200 benefit not found"}}`)) {
		t.Error("another CodeArts code counted as the request's shape")
	}
}
