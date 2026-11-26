package sign

import (
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/secure-systems-lab/go-securesystemslib/encrypted"

	registry2 "github.com/ansibleforms/builder/internal/registry"
)

// cosignKey makes a key as cosign generate-key-pair writes it.
//
// Args:
//
//	t: the test.
//	password: its password.
//
// Returns:
//
//	The key file's text, and the key.
func cosignKey(t *testing.T, password string) (string, *ecdsa.PrivateKey) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := encrypted.EncryptWithCustomKDFParameters(der, []byte(password), encrypted.Legacy)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED SIGSTORE PRIVATE KEY", Bytes: sealed})), key
}

// TestLoadKey checks that a cosign key opens with its password only.
func TestLoadKey(t *testing.T) {
	file, _ := cosignKey(t, "pw")
	if _, err := LoadKey(file, "pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKey(file, "wrong"); err == nil || !strings.Contains(err.Error(), "password") {
		t.Errorf("a wrong password: %v", err)
	}
	if _, err := LoadKey("not a key", "pw"); err == nil {
		t.Error("no PEM was taken")
	}
	plain := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1}}))
	if _, err := LoadKey(plain, ""); err == nil || !strings.Contains(err.Error(), "not a cosign key") {
		t.Errorf("a plain key: %v", err)
	}
}

// TestBundle checks the bundle: its statement names the digest, its signature verifies over the
// DSSE encoding with the public key, and its hint is the key's.
func TestBundle(t *testing.T) {
	file, key := cosignKey(t, "")
	signer, err := LoadKey(file, "")
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("ab", 32)
	data, err := Bundle(signer, digest)
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		MediaType            string
		VerificationMaterial struct{ PublicKey struct{ Hint string } }
		DsseEnvelope         struct {
			Payload, PayloadType string
			Signatures           []struct{ Sig string }
		}
	}
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatal(err)
	}
	if b.MediaType != bundleType || b.DsseEnvelope.PayloadType != payloadType {
		t.Errorf("types %s %s", b.MediaType, b.DsseEnvelope.PayloadType)
	}
	payload, _ := base64.StdEncoding.DecodeString(b.DsseEnvelope.Payload)
	if !strings.Contains(string(payload), `"sha256":"`+strings.Repeat("ab", 32)+`"`) || !strings.Contains(string(payload), predicateType) {
		t.Errorf("statement %s", payload)
	}
	sig, _ := base64.StdEncoding.DecodeString(b.DsseEnvelope.Signatures[0].Sig)
	sum := sha256.Sum256(pae(payloadType, payload))
	if !ecdsa.VerifyASN1(&key.PublicKey, sum[:], sig) {
		t.Error("the signature does not verify")
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	hint := sha256.Sum256(der)
	if b.VerificationMaterial.PublicKey.Hint != base64.StdEncoding.EncodeToString(hint[:]) {
		t.Error("the hint is not the key's")
	}
	if _, err := Bundle(signer, "md5:x"); err == nil {
		t.Error("a digest that is no sha256 was signed")
	}
}

// TestHostKey checks that Docker Hub's names are one registry.
func TestHostKey(t *testing.T) {
	if hostKey("docker.io") != hostKey("index.docker.io") || hostKey("Registry.Example.com") != "registry.example.com" {
		t.Error("hostKey")
	}
}

// ─── keys of every kind ───────────────────────────────────────────────────────

// sealPEM seals a key's bytes as cosign does.
//
// Args:
//
//	t: the test.
//	der: what the key file holds once opened.
//
// Returns:
//
//	The key file's text.
func sealPEM(t *testing.T, der []byte) string {
	t.Helper()
	sealed, err := encrypted.EncryptWithCustomKDFParameters(der, []byte(""), encrypted.Legacy)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED COSIGN PRIVATE KEY", Bytes: sealed}))
}

// TestKeyKinds checks Ed25519 and RSA keys, a key of a kind it cannot sign with, and bytes
// that are no key.
func TestKeyKinds(t *testing.T) {
	_, ed, _ := ed25519.GenerateKey(rand.Reader)
	rk, _ := rsa.GenerateKey(rand.Reader, 1024)
	xk, _ := ecdh.X25519().GenerateKey(rand.Reader)
	for name, key := range map[string]any{"ed25519": ed, "rsa": rk} {
		der, _ := x509.MarshalPKCS8PrivateKey(key)
		signer, err := LoadKey(sealPEM(t, der), "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := Bundle(signer, "sha256:"+strings.Repeat("0", 64)); err != nil {
			t.Errorf("%s signs: %v", name, err)
		}
	}
	der, _ := x509.MarshalPKCS8PrivateKey(xk)
	if _, err := LoadKey(sealPEM(t, der), ""); err == nil || !strings.Contains(err.Error(), "not an ECDSA") {
		t.Errorf("an X25519 key: %v", err)
	}
	if _, err := LoadKey(sealPEM(t, []byte("not a key")), ""); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Errorf("no key: %v", err)
	}
}

// broken is a signer that fails as it is told.
type broken struct {
	crypto.Signer
	signErr bool
}

// Public gives a public key that cannot be written.
func (b broken) Public() crypto.PublicKey { return struct{}{} }

// Sign fails, or signs nothing.
func (b broken) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	if b.signErr {
		return nil, errors.New("the HSM is gone")
	}
	return []byte("sig"), nil
}

// TestBundleFailures checks a key that cannot sign, and one whose public key cannot be written.
func TestBundleFailures(t *testing.T) {
	d := "sha256:" + strings.Repeat("0", 64)
	if _, err := Bundle(broken{signErr: true}, d); err == nil || !strings.Contains(err.Error(), "cannot sign") {
		t.Errorf("a failing signer: %v", err)
	}
	if _, err := Bundle(broken{}, d); err == nil {
		t.Error("a public key that cannot be written")
	}
}

// ─── a registry ───────────────────────────────────────────────────────────────

// testRegistry starts an in-memory registry, with an image pushed to it; refuse makes it answer
// 403 to the requests it matches.
//
// Args:
//
//	t: the test.
//	refuse: whether to refuse a request ; nil : none.
//
// Returns:
//
//	The registry's host:port, the image's reference (by tag) and its digest.
func testRegistry(t *testing.T, refuse func(*http.Request) bool) (string, string, string) {
	t.Helper()
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	// armed once the image is pushed : the refusals are for the signature only
	var armed atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if armed.Load() && refuse != nil && refuse(r) {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	img, _ := random.Image(64, 1)
	ref := host + "/af/rte:1"
	tag, _ := name.ParseReference(ref)
	if err := remote.Write(tag, img, remote.WithAuth(authn.Anonymous)); err != nil {
		t.Fatal(err)
	}
	d, _ := img.Digest()
	armed.Store(true)
	return host, ref, d.String()
}

// shared is one ECDSA key made as cosign does, for the tests that only need to sign: its
// scrypt is slow on purpose, so it is made once.
var (
	shared     crypto.Signer
	sharedOnce sync.Once
)

// signer gives the shared ECDSA key.
//
// Args:
//
//	t: the test.
//
// Returns:
//
//	The signer.
func signer(t *testing.T) crypto.Signer {
	sharedOnce.Do(func() {
		file, _ := cosignKey(t, "")
		s, err := LoadKey(file, "")
		if err != nil {
			t.Fatal(err)
		}
		shared = s
	})
	return shared
}

// TestImage signs an image by its digest, by its tag and by a reference with its digest, and
// finds each signature among its referrers.
func TestImage(t *testing.T) {
	host, ref, d := testRegistry(t, nil)
	login := &registry2.Login{URL: "http://" + host, User: "u", Password: "p"}
	for _, c := range []struct{ ref, digest string }{{ref, d}, {ref, ""}, {host + "/af/rte@" + d, ""}} {
		got, err := Image(c.ref, c.digest, signer(t), login)
		if err != nil {
			t.Fatalf("%v: %v", c, err)
		}
		if got != host+"/af/rte@"+d {
			t.Errorf("signed %s", got)
		}
	}
	sub, _ := name.NewDigest(host + "/af/rte@" + d)
	idx, err := remote.Referrers(sub)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := idx.IndexManifest()
	if len(m.Manifests) != 3 || m.Manifests[0].ArtifactType != bundleType {
		t.Errorf("referrers %+v", m.Manifests)
	}
}

// TestImageFailures checks a reference that is none, an image that is not there, a key that
// cannot sign, and a registry that refuses the bundle or the manifest.
func TestImageFailures(t *testing.T) {
	host, ref, d := testRegistry(t, nil)
	if _, err := Image("Not A Ref", "", signer(t), nil); err == nil || !strings.Contains(err.Error(), "no image reference") {
		t.Errorf("no reference: %v", err)
	}
	if _, err := Image(host+"/af/none:1", "", signer(t), nil); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Errorf("no image: %v", err)
	}
	if _, err := Image(ref, d, broken{signErr: true}, nil); err == nil {
		t.Error("a key that cannot sign")
	}
	for what, refuse := range map[string]func(*http.Request) bool{
		"the bundle": func(r *http.Request) bool { return r.Method == http.MethodPost },
		"the manifest": func(r *http.Request) bool {
			return r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/")
		},
	} {
		_, ref, d := testRegistry(t, refuse)
		if _, err := Image(ref, d, signer(t), nil); err == nil || !strings.Contains(err.Error(), "cannot be pushed") {
			t.Errorf("%s refused: %v", what, err)
		}
	}
}

// TestKeychain checks that the login goes to its own registry only, Docker Hub by any name.
func TestKeychain(t *testing.T) {
	hub, _ := name.NewRegistry("index.docker.io")
	other, _ := name.NewRegistry("other.example.com")
	k := keychain{&registry2.Login{URL: "https://docker.io", User: "u", Password: "p"}}
	if a, _ := k.Resolve(hub); a == authn.Anonymous {
		t.Error("Docker Hub's login was not given")
	}
	if a, _ := k.Resolve(other); a != authn.Anonymous {
		t.Error("the login went to another registry")
	}
	if a, _ := (keychain{}).Resolve(hub); a != authn.Anonymous {
		t.Error("no login, yet one was given")
	}
}
