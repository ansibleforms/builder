// Package sign signs a pushed image with a team's cosign key, offline (no transparency log), as
// cosign 3 sign --use-signing-config=false --tlog-upload=false --key does: a Sigstore bundle
// v0.3 whose DSSE envelope holds an in-toto statement naming the image's digest, attached to the
// image as an OCI 1.1 referrer (on a registry without the referrers API, listed in the
// sha256-<digest> index tag, as the OCI spec's fallback says). cosign verify --key reads it as
// its own, and the builder needs no cosign binary.
package sign

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/secure-systems-lab/go-securesystemslib/encrypted"

	"github.com/ansibleforms/builder/internal/registry"
)

// ─── what it writes ───────────────────────────────────────────────────────────

const (
	// bundleType is the Sigstore bundle's media type, and the referrer's artifact type.
	bundleType = "application/vnd.dev.sigstore.bundle.v0.3+json"
	// emptyType is the empty config's media type (OCI 1.1 artifacts).
	emptyType = "application/vnd.oci.empty.v1+json"
	// payloadType is the DSSE envelope's payload type: an in-toto statement.
	payloadType = "application/vnd.in-toto+json"
	// predicateType is what cosign names a plain signature's predicate.
	predicateType = "https://sigstore.dev/cosign/sign/v1"
)

// ─── the key ──────────────────────────────────────────────────────────────────

// LoadKey opens a cosign private key (cosign generate-key-pair): a PEM block of type ENCRYPTED
// SIGSTORE PRIVATE KEY (or ENCRYPTED COSIGN PRIVATE KEY, cosign 1), sealed with scrypt and
// nacl/secretbox under its password, holding a PKCS #8 key.
//
// Args:
//
//	keyPEM: the key file's text.
//	password: its password (COSIGN_PASSWORD), maybe empty.
//
// Returns:
//
//	The signer: ECDSA, Ed25519 or RSA.
//
// Errors:
//
//	When it is no cosign key, the password is wrong, or the key is of another kind.
func LoadKey(keyPEM, password string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, errors.New("the signing key is no PEM key")
	}
	if block.Type != "ENCRYPTED SIGSTORE PRIVATE KEY" && block.Type != "ENCRYPTED COSIGN PRIVATE KEY" {
		return nil, fmt.Errorf("the signing key is a %s, not a cosign key (cosign generate-key-pair)", block.Type)
	}
	der, err := encrypted.Decrypt(block.Bytes, []byte(password))
	if err != nil {
		return nil, errors.New("the signing key does not open with its password")
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("the signing key cannot be read: %w", err)
	}
	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		return k, nil
	case ed25519.PrivateKey:
		return k, nil
	case *rsa.PrivateKey:
		return k, nil
	}
	return nil, fmt.Errorf("the signing key is a %T, not an ECDSA, Ed25519 or RSA key", key)
}

// signBytes signs a message as sigstore's signers do: SHA-256 then ECDSA (ASN.1) or RSA
// PKCS #1 v1.5, or Ed25519 over the message itself.
//
// Args:
//
//	signer: the key.
//	msg: the message.
//
// Returns:
//
//	The signature.
//
// Errors:
//
//	When the key cannot sign.
func signBytes(signer crypto.Signer, msg []byte) ([]byte, error) {
	if k, ok := signer.(ed25519.PrivateKey); ok {
		return ed25519.Sign(k, msg), nil
	}
	sum := sha256.Sum256(msg)
	return signer.Sign(rand.Reader, sum[:], crypto.SHA256)
}

// keyHint is the bundle's hint of the key that signed it: the SHA-256 of its public key's
// PKIX form, in base64, as cosign writes it.
//
// Args:
//
//	signer: the key.
//
// Returns:
//
//	The hint.
//
// Errors:
//
//	When the public key cannot be written.
func keyHint(signer crypto.Signer) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}

// ─── the bundle ───────────────────────────────────────────────────────────────

// pae is DSSE's pre-authentication encoding of a payload: what is signed.
//
// Args:
//
//	typ: the payload's type.
//	payload: the payload.
//
// Returns:
//
//	The encoding.
func pae(typ string, payload []byte) []byte {
	return []byte(fmt.Sprintf("DSSEv1 %d %s %d %s", len(typ), typ, len(payload), payload))
}

// Bundle makes the Sigstore bundle that signs an image's digest.
//
// Args:
//
//	signer: the key.
//	digest: the image's digest (sha256:...).
//
// Returns:
//
//	The bundle's JSON.
//
// Errors:
//
//	When the digest is no sha256 one, or the key cannot sign.
func Bundle(signer crypto.Signer, digest string) ([]byte, error) {
	hexDigest, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hexDigest) != 64 {
		return nil, fmt.Errorf("'%s' is no sha256 digest", digest)
	}
	// plain maps of strings : json.Marshal has no error to give
	statement, _ := json.Marshal(map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       []any{map[string]any{"digest": map[string]string{"sha256": hexDigest}, "annotations": map[string]string{}}},
		"predicateType": predicateType,
		"predicate":     map[string]any{},
	})
	sig, err := signBytes(signer, pae(payloadType, statement))
	if err != nil {
		return nil, fmt.Errorf("the signing key cannot sign: %w", err)
	}
	hint, err := keyHint(signer)
	if err != nil {
		return nil, err
	}
	bundle, _ := json.Marshal(map[string]any{
		"mediaType":            bundleType,
		"verificationMaterial": map[string]any{"publicKey": map[string]string{"hint": hint}},
		"dsseEnvelope": map[string]any{
			"payload":     base64.StdEncoding.EncodeToString(statement),
			"payloadType": payloadType,
			"signatures":  []any{map[string]string{"sig": base64.StdEncoding.EncodeToString(sig)}},
		},
	})
	return bundle, nil
}

// ─── the referrer ─────────────────────────────────────────────────────────────

// rawManifest is a manifest written as it is.
type rawManifest struct {
	data      []byte
	mediaType types.MediaType
}

// RawManifest is the manifest's bytes.
func (m rawManifest) RawManifest() ([]byte, error) { return m.data, nil }

// MediaType is the manifest's media type.
func (m rawManifest) MediaType() (types.MediaType, error) { return m.mediaType, nil }

// keychain hands the build's registry login to the registry it names, nothing to another.
type keychain struct{ login *registry.Login }

// Resolve gives the login of a registry.
//
// Args:
//
//	r: the registry.
//
// Returns:
//
//	Its login, anonymous when it is not the build's.
func (k keychain) Resolve(r authn.Resource) (authn.Authenticator, error) {
	if k.login == nil || k.login.URL == "" || hostKey(registry.Host(k.login.URL)) != hostKey(r.RegistryStr()) {
		return authn.Anonymous, nil
	}
	return authn.FromConfig(authn.AuthConfig{Username: k.login.User, Password: k.login.Password}), nil
}

// hostKey names a registry's host as one, whichever name of Docker Hub it goes by.
//
// Args:
//
//	host: the host.
//
// Returns:
//
//	The host, Docker Hub's names as index.docker.io.
func hostKey(host string) string {
	switch h := strings.ToLower(host); h {
	case "docker.io", "registry-1.docker.io", "index.docker.io":
		return "index.docker.io"
	default:
		return h
	}
}

// Image signs a pushed image: its bundle pushed to its repository as a referrer of its digest.
//
// Args:
//
//	ref: the image's reference, with a tag or a digest (registry.example.com/af/rte:1).
//	digest: its digest ; empty : read from the registry.
//	signer: the key.
//	login: the registry's login (its http:// address : no TLS).
//
// Returns:
//
//	The signed reference (repository@digest).
//
// Errors:
//
//	When the reference is no image's, the registry refuses, or the key cannot sign.
func Image(ref, digest string, signer crypto.Signer, login *registry.Login) (string, error) {
	var opts []name.Option
	if registry.Insecure(login) {
		opts = append(opts, name.Insecure)
	}
	parsed, err := name.ParseReference(ref, opts...)
	if err != nil {
		return "", fmt.Errorf("'%s' is no image reference: %w", ref, err)
	}
	ropts := []remote.Option{remote.WithAuthFromKeychain(keychain{login})}
	repo := parsed.Context()

	// step 1 : the image's manifest, as its subject (its digest, its type and size)
	if digest == "" {
		if d, ok := parsed.(name.Digest); ok {
			digest = d.DigestStr()
		}
	}
	target := parsed
	if digest != "" {
		target = repo.Digest(digest)
	}
	head, err := remote.Head(target, ropts...)
	if err != nil {
		return "", fmt.Errorf("the image %s cannot be read: %w", target, err)
	}
	subject := v1.Descriptor{MediaType: head.MediaType, Size: head.Size, Digest: head.Digest}

	// step 2 : the bundle and the empty config, pushed as blobs
	bundle, err := Bundle(signer, subject.Digest.String())
	if err != nil {
		return "", err
	}
	bundleLayer := static.NewLayer(bundle, bundleType)
	emptyLayer := static.NewLayer([]byte("{}"), emptyType)
	for _, l := range []v1.Layer{emptyLayer, bundleLayer} {
		if err := remote.WriteLayer(repo, l, ropts...); err != nil {
			return "", fmt.Errorf("the signature cannot be pushed: %w", err)
		}
	}
	bundleDigest, _ := bundleLayer.Digest()
	emptyDigest, _ := emptyLayer.Digest()

	// step 3 : the referrer's manifest, by its digest ; go-containerregistry also updates the
	// sha256-<digest> index tag of a registry without the referrers API
	annotations := map[string]string{
		"dev.sigstore.bundle.content":       "dsse-envelope",
		"dev.sigstore.bundle.predicateType": predicateType,
		"org.opencontainers.image.created":  time.Now().UTC().Format(time.RFC3339),
	}
	manifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     types.OCIManifestSchema1,
		"artifactType":  bundleType,
		"config":        map[string]any{"mediaType": emptyType, "size": 2, "digest": emptyDigest.String()},
		"layers": []any{map[string]any{
			"mediaType":   bundleType,
			"size":        len(bundle),
			"digest":      bundleDigest.String(),
			"annotations": map[string]string{"org.opencontainers.image.title": strings.Replace(bundleDigest.String(), ":", "-", 1) + ".sigstore.json"},
		}},
		"subject":     subject,
		"annotations": annotations,
	})
	// hashing bytes in memory has no error to give
	sum, _, _ := v1.SHA256(bytes.NewReader(manifest))
	if err := remote.Put(repo.Digest(sum.String()), rawManifest{manifest, types.OCIManifestSchema1}, ropts...); err != nil {
		return "", fmt.Errorf("the signature cannot be pushed: %w", err)
	}
	return repo.Digest(subject.Digest.String()).String(), nil
}
