# Contributor License

**Effective 2026-09-06.** Contributions to rousseau-agent are accepted
under the [Developer Certificate of Origin (DCO)](https://developercertificate.org/),
a lightweight per-commit attestation that you have the right to
submit the contribution under the project's license
([FSL-1.1-Apache-2.0](./LICENSE)).

## How to sign off

Add a `Signed-off-by:` trailer to every commit message. Git does this
automatically with `-s`:

```
git commit -s -m "your commit message"
```

Which produces a commit message like:

```
your commit message

Signed-off-by: Jane Doe <jane@example.com>
```

Use the email address associated with your GitHub account. The name
and email must be real — anonymous or role-account contributions
cannot be accepted because the DCO requires the contributor to make
the attestation as an individual.

## What you're attesting to

By adding `Signed-off-by:` you make the following statement
(reproduced verbatim from [developercertificate.org](https://developercertificate.org/)):

```
Developer Certificate of Origin
Version 1.1

Copyright (C) 2004, 2006 The Linux Foundation and its contributors.

Everyone is permitted to copy and distribute verbatim copies of this
license document, but changing it is not allowed.


Developer's Certificate of Origin 1.1

By making a contribution to this project, I certify that:

(a) The contribution was created in whole or in part by me and I
    have the right to submit it under the open source license
    indicated in the file; or

(b) The contribution is based upon previous work that, to the best
    of my knowledge, is covered under an appropriate open source
    license and I have the right under that license to submit that
    work with modifications, whether created in whole or in part
    by me, under the same open source license (unless I am
    permitted to submit under a different license), as indicated
    in the file; or

(c) The contribution was provided directly to me by some other
    person who certified (a), (b) or (c) and I have not modified
    it.

(d) I understand and agree that this project and the contribution
    are public and that a record of the contribution (including all
    personal information I submit with it, including my sign-off) is
    maintained indefinitely and may be redistributed consistent with
    this project or the open source license(s) involved.
```

## Why DCO and not a copyright-assignment CLA

A copyright-assignment CLA (like Google's, Apache Foundation's) is
the heavier alternative — it transfers copyright ownership to the
project. That preserves the maintainer's freedom to relicense in the
future without re-signing every past contributor. It is also
adversarial-feeling to first-time contributors and gates the PR
until an out-of-band form is signed.

DCO gives us most of what we need — a per-contribution provenance
record and an explicit affirmation that the contributor has the
right to submit the code — without the friction. The trade-off:
future relicensing (e.g. to a different fair-source variant) would
require the current contributors' consent rather than the
maintainer's unilateral action. For a project this size that is
acceptable, and it can be revisited as the contributor base grows.

## Enforcement

A GitHub Actions workflow rejects any PR whose commits lack the
`Signed-off-by:` trailer. To retroactively sign existing commits on
a branch:

```
git rebase HEAD~<N> --signoff
git push --force-with-lease
```

For a single commit at HEAD:

```
git commit --amend --signoff --no-edit
git push --force-with-lease
```

## Corporate contributions

If you are contributing on behalf of your employer, ensure your
employer has permitted you to submit code under FSL-1.1-Apache-2.0.
Most permissive open-source licenses are pre-cleared by employer
open-source policies; FSL's non-compete clause during the two-year
transition window is worth flagging to your legal team specifically.

## Questions

Open an issue on the tracker or email the maintainer. Legal-review
questions are welcome — the project would rather answer them now
than after a contested commit lands.
