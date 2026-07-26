# Lessons Learned

## Go Middleware Execution Order (Inside-Out)
- **Problem**: In Go HTTP middleware chaining, wrapping is done inside-out. The middleware wrapped last becomes the outermost one and executes first.
- **Example**:
  ```go
  // If we wrap like this:
  h = middleware.WithResourcesContext(client, o, c, po, tr, h)
  h = middleware.LimitQueryRange(h)
  ```
  `LimitQueryRange` wraps `WithResourcesContext`.
  Thus, `LimitQueryRange` executes *before* `WithResourcesContext`.
  If `LimitQueryRange` relies on request context variables injected by `WithResourcesContext`, it will find them to be `nil` because the context has not been populated yet.
- **Solution**: Swap the order of registration so that `WithResourcesContext` wraps `LimitQueryRange`.
  ```go
  h = middleware.LimitQueryRange(h)
  h = middleware.WithResourcesContext(client, o, c, po, tr, h)
  ```
  Now, `WithResourcesContext` runs first, injects resources into the context, and then calls the next handler (`LimitQueryRange`), which successfully reads resources from the context.

## GitHub Open Source Contribution & PR Workflow Rules
- **Rule 1: Always apply DCO Sign-off (`git commit -s`)**:
  - When committing changes intended for upstream open-source PRs, ALWAYS use `git commit -s` (`--signoff`) to attach `Signed-off-by: Name <email>` header. Without this, the GitHub DCO Bot check will fail.
- **Rule 2: Strictly follow `.github/pull_request_template.md`**:
  - Before creating a PR via `gh pr create` or web UI, check if `.github/pull_request_template.md` exists. Always format the PR title and description strictly adhering to the template sections (e.g. `## Description`, `## Type of Change`, `## AI Disclosure`).
- **Rule 3: Target the correct Remote Repository**:
  - Distinguish between internal/private remotes (e.g. `thinker0-line`) and public GitHub remotes (e.g. `thinker0`). Public open-source PRs must be pushed to `thinker0` (`git@github.com:thinker0/trickster.git`).
- **Rule 4: Double-Check DCO and CI Status before declaring "Done"**:
  - Do not assume pushing a commit is the final step. Always run `gh pr checks <PR>` or inspect `git log <branch> --format="%B"` to verify that every single commit in the PR's history has the `Signed-off-by` line. DCO checks fail if even a single commit lacks it.

