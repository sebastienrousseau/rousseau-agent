package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrUnsigned is returned when a signed loader encounters a skill
// without a .sig companion in strict mode.
var ErrUnsigned = errors.New("skills: skill file has no .sig companion")

// ErrBadSignature is returned when signature verification fails.
var ErrBadSignature = errors.New("skills: signature verification failed")

// Verifier decides whether a skill file's signature is trusted. The
// default implementation ([SSHKeygenVerifier]) shells out to
// `ssh-keygen -Y verify` against an OpenSSH allowed-signers file —
// matches the way git verifies SSH-signed commits/tags when
// gpg.format=ssh.
type Verifier interface {
	// Verify checks that path is signed by an allowed signer. The
	// companion signature file is expected at path + ".sig".
	// Returns nil when the signature is valid; ErrUnsigned when the
	// .sig file is absent; ErrBadSignature (wrapping the underlying
	// verifier error) when the signature is present but invalid.
	Verify(ctx context.Context, path string) error
}

// VerifyOptions configures [LoadVerified].
type VerifyOptions struct {
	// Verifier is the signature backend. Nil defaults to
	// [SSHKeygenVerifier] with default settings — usable only if the
	// operator has also set AllowedSignersFile.
	Verifier Verifier
	// Strict, when true, means skill files without a valid signature
	// are DROPPED (with a WARN log). When false — the default — the
	// loader still returns unsigned skills; verification is
	// advisory. Set true in production; leave false during authoring.
	Strict bool
	// Logger receives WARN entries for skipped skills. Nil uses
	// slog.Default.
	Logger *slog.Logger
}

// LoadVerified wraps [Load]: it reads every skill under dir, then
// filters out any that fail verification per opts. When opts.Strict
// is false, verification failures are logged at WARN but the skill is
// kept. When true, failing skills are omitted from the return.
func LoadVerified(ctx context.Context, dir string, opts VerifyOptions) ([]Skill, error) {
	all, err := Load(dir)
	if err != nil {
		return nil, err
	}
	if opts.Verifier == nil || len(all) == 0 {
		return all, nil
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	out := make([]Skill, 0, len(all))
	for _, s := range all {
		if err := opts.Verifier.Verify(ctx, s.Path); err != nil {
			logger.Warn("skills.verify_failed",
				slog.String("skill", s.Name),
				slog.String("path", s.Path),
				slog.String("err", err.Error()),
			)
			if opts.Strict {
				continue
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// SSHKeygenVerifier verifies a skill's signature by shelling out to
// `ssh-keygen -Y verify` — the same command git uses under
// gpg.format=ssh. Requires:
//   - `ssh-keygen` on $PATH (in the container image already)
//   - AllowedSignersFile pointing at an OpenSSH allowed-signers file
//   - Companion .sig files produced by `ssh-keygen -Y sign`
//
// Namespace defaults to "rousseau-skills" — signers must use the
// same value when signing (`ssh-keygen -Y sign -n rousseau-skills`).
type SSHKeygenVerifier struct {
	// AllowedSignersFile points at an OpenSSH allowed-signers file
	// listing every identity permitted to sign skill files. Format:
	//     <email> <ssh-ed25519 AAAAC3...>
	// See ssh-keygen(1) ALLOWED SIGNERS section.
	AllowedSignersFile string
	// Signer, when set, restricts trust to the identity string named
	// here (matches the -I flag of ssh-keygen -Y verify). Empty means
	// accept any signer listed in the allowed-signers file.
	Signer string
	// Namespace is the SSHSIG namespace expected on the signature.
	// Empty defaults to "rousseau-skills".
	Namespace string
	// Timeout bounds each verify call. Zero uses 5 seconds.
	Timeout time.Duration
}

// Verify satisfies [Verifier].
func (v *SSHKeygenVerifier) Verify(ctx context.Context, path string) error {
	// Config precedence: bad Verifier config surfaces before data
	// issues (missing signature) so operators see the fixable problem
	// first rather than a symptom-level "no .sig" error.
	if v.AllowedSignersFile == "" {
		return errors.New("skills: SSHKeygenVerifier.AllowedSignersFile is required")
	}
	if _, err := os.Stat(v.AllowedSignersFile); err != nil {
		return fmt.Errorf("skills: allowed_signers_file: %w", err)
	}
	sigPath := path + ".sig"
	if _, err := os.Stat(sigPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrUnsigned, sigPath)
		}
		return fmt.Errorf("skills: stat sig: %w", err)
	}

	timeout := v.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ns := v.Namespace
	if ns == "" {
		ns = "rousseau-skills"
	}
	signer := v.Signer
	if signer == "" {
		// -I is required by ssh-keygen -Y verify. When the operator
		// doesn't pin one, extract the first identity from the
		// allowed-signers file — matches any signer's principal.
		s, err := firstAllowedSigner(v.AllowedSignersFile)
		if err != nil {
			return fmt.Errorf("skills: no signer pinned and none derivable: %w", err)
		}
		signer = s
	}

	args := []string{
		"-Y", "verify",
		"-f", v.AllowedSignersFile,
		"-I", signer,
		"-n", ns,
		"-s", sigPath,
	}
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied path within the skills dir
	if err != nil {
		return fmt.Errorf("skills: read %s: %w", path, err)
	}

	// #nosec G204 -- fixed argv; skill/signer paths are operator config.
	cmd := exec.CommandContext(callCtx, "ssh-keygen", args...)
	cmd.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: ssh-keygen: %v: %s", ErrBadSignature, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// firstAllowedSigner returns the first non-comment, non-blank
// signer identity in an OpenSSH allowed-signers file. Format per
// ssh-keygen(1):
//
//	<identity> <keytype> <base64-key> [comment]
//
// Lines starting with `#` and blank lines are ignored.
func firstAllowedSigner(path string) (string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Identity is the first whitespace-delimited token.
		if i := strings.IndexAny(trimmed, " \t"); i > 0 {
			return trimmed[:i], nil
		}
	}
	return "", errors.New("skills: allowed_signers_file has no identities")
}
