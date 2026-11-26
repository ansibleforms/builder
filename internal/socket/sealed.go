package socket

// A job's secrets, sealed for the RTE that runs it.
//
// The controller resolves a build's credentials (the registry login, the signing key) and hands
// them to the builder with the build: the builder never reads the credentials table nor a
// secret store. The bundle is sealed with AES-256-GCM, under a key derived from the builder's
// token and the job id: it opens for that builder and that job only, and a changed byte is
// refused rather than read, also over plain http inside a cluster.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// sealVersion is the version of the bundles it reads.
const sealVersion = 1

// sealSalt is the HKDF salt both sides use.
const sealSalt = "ansibleforms-rte-job-secrets"

// Sealed is a job's sealed secrets, as the controller sends them: base64 strings.
type Sealed struct {
	V    any    `json:"v"`
	IV   string `json:"iv"`
	Tag  string `json:"tag"`
	Data string `json:"data"`
}

// sealKey makes the key and the additional data of one job's bundle.
//
// Args:
//
//	token: the builder's token (RTE_TOKEN), known to the controller and the builder.
//	jobID: the job.
//
// Returns:
//
//	The AES-256 key and the data the tag also covers.
//
// Errors:
//
//	When there is no token.
func sealKey(token string, jobID int64) ([]byte, []byte, error) {
	if token == "" {
		return nil, nil, errors.New("no runner token to seal the job's secrets with")
	}
	info := fmt.Sprintf("job:%d", jobID)
	// a 32-byte key from SHA-256 : hkdf.Key has no error to give
	key, _ := hkdf.Key(sha256.New, []byte(token), []byte(sealSalt), info, 32)
	return key, []byte(info), nil
}

// SealJobSecrets seals a job's secrets, as the controller does: the tests seal with it.
//
// Args:
//
//	secrets: the secrets, any JSON value.
//	token: the builder's token.
//	jobID: the job.
//
// Returns:
//
//	The sealed bundle.
//
// Errors:
//
//	When there is no token, or the secrets are no JSON.
func SealJobSecrets(secrets any, token string, jobID int64) (*Sealed, error) {
	key, aad, err := sealKey(token, jobID)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(secrets)
	if err != nil {
		return nil, err
	}
	// a 32-byte key : AES-256 and GCM have no error to give, nor crypto/rand
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	iv := make([]byte, gcm.NonceSize())
	_, _ = rand.Read(iv)
	out := gcm.Seal(nil, iv, plain, aad)
	data, tag := out[:len(out)-gcm.Overhead()], out[len(out)-gcm.Overhead():]
	enc := base64.StdEncoding.EncodeToString
	return &Sealed{V: sealVersion, IV: enc(iv), Tag: enc(tag), Data: enc(data)}, nil
}

// OpenJobSecrets opens a job's sealed secrets.
//
// Args:
//
//	sealed: the bundle, as the run message holds it (a JSON object).
//	token: the builder's token.
//	jobID: the job.
//	into: where the secrets are read into (a pointer, as json.Unmarshal takes).
//
// Errors:
//
//	When there is no bundle, an unknown version, or one that does not open for this token and
//	job.
func OpenJobSecrets(sealed any, token string, jobID int64, into any) error {
	obj, ok := sealed.(map[string]any)
	if !ok {
		return errors.New("the job came without its sealed secrets")
	}
	if v, _ := obj["v"].(float64); v != sealVersion {
		return fmt.Errorf("the job's secrets are sealed with version %v, this runner reads version %d", obj["v"], sealVersion)
	}
	key, aad, err := sealKey(token, jobID)
	if err != nil {
		return err
	}
	refused := func(cause error) error {
		return fmt.Errorf("the job's sealed secrets do not open with this runner's token for this job: %w", cause)
	}
	field := func(name string) ([]byte, error) {
		s, _ := obj[name].(string)
		return base64.StdEncoding.DecodeString(s)
	}
	iv, err := field("iv")
	if err != nil {
		return refused(err)
	}
	tag, err := field("tag")
	if err != nil {
		return refused(err)
	}
	data, err := field("data")
	if err != nil {
		return refused(err)
	}
	// a 32-byte key : AES-256 has no error to give
	block, _ := aes.NewCipher(key)
	gcm, err := cipher.NewGCMWithNonceSize(block, len(iv))
	if err != nil {
		return refused(err)
	}
	plain, err := gcm.Open(nil, iv, append(data, tag...), aad)
	if err != nil {
		return refused(err)
	}
	if err := json.Unmarshal(plain, into); err != nil {
		return refused(err)
	}
	return nil
}
