# Open Source Release Checklist — tilekeeper

The remaining work to ship `tilekeeper` publicly, each item with the exact
commands. In-repo hygiene (license, CI, Justfile, toolchain, secrets scan,
rename) is already done and not repeated here — this is the open TODO only.

Repo: Go, target `github.com/mschulkind-oss/tilekeeper`. No git remote yet.
Legend: 🖥️ = must run on the **host** (the jail has no GitHub creds and no
backplane mount).

**Most of §1–§7 is automated by `scratch/oss-launch.sh`** (host-local,
gitignored) — run it from the host and it walks each step with confirmations on
the irreversible ones. The sections below are the manual reference.

---

## 1. Blocker — squash pre-OSS history, then create + push 🖥️

**Squash first — this is unfixable once pushed.** Every commit becomes public
forever, and the current history still contains `/home/matt/…` paths in old
commits (removed from HEAD but not from history). Back up, then reset to a
single commit:

```sh
git bundle create ../tilekeeper-presquash-backup.bundle --all   # safety net
git checkout --orphan _fresh && git add -A
git commit --no-verify -m "Initial commit" && git branch -M _fresh main
git log -p | grep /home/matt && echo "STOP: still leaking" || echo "clean"
```

Then create the public repo and push:

```sh
gh repo create mschulkind-oss/tilekeeper \
  --public --source=. --remote=origin \
  --description "Per-workspace tiling layout manager for Sway/Wayland" --push
```

Then apply the per-repo ruleset (free tier): block force-push + deletion on
`main`, require squash merges. GitHub → repo → Settings → Rules → New ruleset,
or `gh api` the ruleset. No PR requirement (solo dev, commit straight to main).

---

## 2. Migrate your own running install (one-time)

Your live daemon still runs as `layout-manager` with config in
`~/.config/layout-manager/` (section `[layout-manager]`). Move it over:

1. **Stop + remove the old service:**
   ```sh
   systemctl --user disable --now layout-manager
   rm -f ~/.config/systemd/user/layout-manager.service
   ```
2. **Move the config dir:**
   ```sh
   mv ~/.config/layout-manager ~/.config/tilekeeper
   ```
3. **Rename the config section** in `~/.config/tilekeeper/config.toml`: change
   the top-level `[layout-manager]` header to `[tilekeeper]`. Leave
   `[workspace.*]` sections as-is.
4. **Rename env vars** if you set any (shell profile, service drop-in): the
   `LM_` prefix is now `TK_` — `LM_LOG_LEVEL`→`TK_LOG_LEVEL`,
   `LM_EVENT_CAPTURE`→`TK_EVENT_CAPTURE`.
5. **Convert sway keybindings** *if you use any* `nop layman …` bindings — the
   layman-compat parser is gone and the binding namespace is now
   `nop tilekeeper <command>` (see [`docs/COMMANDS.md`](COMMANDS.md) for the full
   grammar and an example config). Your active `~/.config/sway/config` has no nop
   bindings, so this is likely a no-op. Examples: `nop layman window swap master`
   → `nop tilekeeper swap-master`; `nop layman layout MasterStack` →
   `nop tilekeeper layout MasterStack`.
6. **Reinstall + enable:**
   ```sh
   just deploy                              # build + install + restart, or:
   just install-service
   systemctl --user enable --now tilekeeper
   ```
   The IPC socket moves to `$XDG_RUNTIME_DIR/tilekeeper.sock` automatically.

---

## 3. Verify it builds and runs from a clean clone

Proves a stranger can build it. In a scratch dir on the host:

```sh
git clone git@github.com:mschulkind-oss/tilekeeper.git /tmp/tk-clean
cd /tmp/tk-clean
just setup      # mise install (go 1.26.2, just, staticcheck) + go mod download
just check      # format + lint (vet + staticcheck) + tests + fuzz-gate
just doctor     # environment check — should report a healthy setup
```

Fix anything that only works because of your dev environment (uncommitted
tooling, PATH assumptions). Note any surprise step here for the README.

---

## 4. README polish

Confirm the README front-door has, in order: **Install**, **Quick start**, and
**Why-this-exists**. The install section must carry the working one-liner:

```sh
go install github.com/mschulkind-oss/tilekeeper/cmd/tilekeeper@latest
```

Skim for anything that reads as internal/dev-only and move deeper detail into
`docs/` — the full command grammar now lives in [`docs/COMMANDS.md`](COMMANDS.md).

---

## 5. Distribution — confirm `go install` works end-to-end

After the first push (so the module proxy can see it), from a clean environment:

```sh
GOBIN=/tmp/tkbin go install github.com/mschulkind-oss/tilekeeper/cmd/tilekeeper@latest
/tmp/tkbin/tilekeeper version    # should print "tilekeeper <version>"
```

If you later want a Homebrew tap / AUR package, see the playbook's
`distribution.md` channel matrix — not required for v0.1.0.

---

## 6. First release 🖥️

Tag and publish once §1–§3 pass:

```sh
oss-release            # preferred: shows commits, bumps, tags, pushes, gh release
# or manually:
gh release create v0.1.0 --generate-notes
```

Edit the generated notes — don't ship "Initial commit" as the body.

---

## 7. GitHub-side metadata 🖥️

After the repo exists:

```sh
gh repo edit mschulkind-oss/tilekeeper \
  --description "Per-workspace tiling layout manager for Sway/Wayland" \
  --homepage "https://github.com/mschulkind-oss/tilekeeper" \
  --add-topic sway --add-topic wayland --add-topic tiling \
  --add-topic window-manager --add-topic go
```

Optionally add a social-preview image in Settings → General.

---

## 8. Host-side backplane hygiene 🖥️

The jail can't reach backplane; do these from the host checkout:

- **Canonical AGENTS.md block** — the repo has the interim Committing block;
  sync the full BEGIN/END agent-standards block:
  ```sh
  ~/code/backplane/bin/sync-agents-md ~/code/tilekeeper
  ```
- **Standards-sync coverage** — add the repo so future rollouts cover it:
  append `~/code/tilekeeper` to
  `~/code/backplane/docs/standards/standards-repos.txt`.
- **Attribution git hooks** — the pre-commit hook (`just check-ci`) is already
  installed; add the two attribution hooks:
  ```sh
  install -m 0755 ~/code/backplane/standards/hooks/commit-msg .git/hooks/commit-msg
  install -m 0755 ~/code/backplane/standards/hooks/pre-push   .git/hooks/pre-push
  ```

---

## 9. Post-launch

- Announce in the Sway community (r/swaywm, relevant Discords).
- Submit to `awesome-sway` / `awesome-wayland`.
- Establish a release cadence via `oss-release` as fixes land.
