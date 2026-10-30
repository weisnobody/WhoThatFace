# Update User Documentation

## Objective

Bring `README.md`, `installation.md`, and `usage.md` into alignment
with the current CLI and provide a complete, safe first-run and maintenance
workflow.

## Scope

1. Use the current CLI implementation and command help as the source of truth
   for examples and options.
2. Expand usage guidance for configuration inspection, collection lifecycle,
   job control and failure recovery, reference processing, candidate-run
   inspection, account administration, person privacy controls, search
   filters, charts, service shutdown, backup/restore verification, and
   destructive derived-data purging.
3. Explain role and collection permissions where account administration is
   documented, and call out privacy-sensitive data and destructive operations.
4. Correct documentation structure issues, including duplicate section
   numbering and stale/incomplete command descriptions.
5. Add README links and a short next-step path to the installation, usage,
   and configuration reference documents.
6. Make installation examples explicit about the built binary, selected
   configuration, registered pipeline, and worker script, while linking to the
   full workflow.

## Validation

- Cross-check every new command and option against `cmd/face-me-bro/main.go`.
- Check Markdown formatting, links, numbering, and stale terminology.
- Review the final diff for private data, generated files, or commands that
  could be mistaken for non-destructive operations.

## Assumptions

- This is a documentation-only change; CLI behavior is not changed.
- Runtime examples continue to use the repository-local `runtime/` layout.
- The CLI and its command-specific help remain authoritative if behavior and
  older prose diverge.
