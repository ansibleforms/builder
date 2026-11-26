package socket

import (
	"testing"
)

// TestOpenControllerSecrets opens a bundle the controller's sealJobSecrets made (Node.js), and
// refuses it for another job or token.
func TestOpenControllerSecrets(t *testing.T) {
	sealed := map[string]any{
		"v":    float64(1),
		"iv":   "jr27ZOtFC0RMnwgK",
		"tag":  "c6AeEkShos8Yzchf4uZ/0Q==",
		"data": "Yj+cgnYLffy+l9dKFkFiCIiDHaiyrECXtDClfdPddly7c9GrVY68g6ZoCkuhArtKdjYsmCCipJA9J7dhUTh9i9NTDzDDUaVMbIpBx2TuiG/H8bE2B7bLJFV6YPu4+Iwew+cwbeo3JLAPiqXXvsZskum57EcuIyK3",
	}
	var got struct {
		Registry struct{ URL, User, Password string }
		Signing  struct{ Key, Password string }
	}
	if err := OpenJobSecrets(sealed, "token-0123456789abcdef", 42, &got); err != nil {
		t.Fatal(err)
	}
	if got.Registry.URL != "https://registry.example.com" || got.Registry.Password != "p@ss" || got.Signing.Key != "KEY" {
		t.Errorf("opened %+v", got)
	}
	if OpenJobSecrets(sealed, "token-0123456789abcdef", 43, &got) == nil {
		t.Error("it opened for another job")
	}
	if OpenJobSecrets(sealed, "another-token-0123456", 42, &got) == nil {
		t.Error("it opened with another token")
	}
	if OpenJobSecrets(nil, "token-0123456789abcdef", 42, &got) == nil {
		t.Error("it took no bundle")
	}
}

// TestSealOpen checks that what it seals opens back.
func TestSealOpen(t *testing.T) {
	s, err := SealJobSecrets(map[string]string{"a": "b"}, "token-0123456789abcdef", 7)
	if err != nil {
		t.Fatal(err)
	}
	obj := map[string]any{"v": float64(1), "iv": s.IV, "tag": s.Tag, "data": s.Data}
	var got map[string]string
	if err := OpenJobSecrets(obj, "token-0123456789abcdef", 7, &got); err != nil || got["a"] != "b" {
		t.Errorf("opened %v, %v", got, err)
	}
	obj["v"] = float64(2)
	if OpenJobSecrets(obj, "token-0123456789abcdef", 7, &got) == nil {
		t.Error("it read another version")
	}
}

// TestSealFailures checks sealing without a token or with secrets that are no JSON, and
// bundles that do not open: no token, bad base64 in each field, no nonce, the wrong shape.
func TestSealFailures(t *testing.T) {
	if _, err := SealJobSecrets(map[string]string{}, "", 1); err == nil {
		t.Error("sealed without a token")
	}
	if _, err := SealJobSecrets(func() {}, "token-0123456789abcdef", 1); err == nil {
		t.Error("sealed what is no JSON")
	}
	s, _ := SealJobSecrets(map[string]string{"a": "b"}, "token-0123456789abcdef", 1)
	good := map[string]any{"v": float64(1), "iv": s.IV, "tag": s.Tag, "data": s.Data}
	var out map[string]string
	if OpenJobSecrets(good, "", 1, &out) == nil {
		t.Error("opened without a token")
	}
	for _, field := range []string{"iv", "tag", "data"} {
		bad := map[string]any{}
		for k, v := range good {
			bad[k] = v
		}
		bad[field] = "%%%"
		if OpenJobSecrets(bad, "token-0123456789abcdef", 1, &out) == nil {
			t.Errorf("opened with a bad %s", field)
		}
	}
	noIV := map[string]any{"v": float64(1), "iv": "", "tag": s.Tag, "data": s.Data}
	if OpenJobSecrets(noIV, "token-0123456789abcdef", 1, &out) == nil {
		t.Error("opened without a nonce")
	}
	var number int
	if OpenJobSecrets(good, "token-0123456789abcdef", 1, &number) == nil {
		t.Error("an object read into a number")
	}
}
