---
name: bridge-audit
description: Audit jetmon-bridge for constraint violations before shipping
allowed-tools: Bash(grep *) Bash(git *)
---

## Changed files
!`git diff main...HEAD --name-only`

## Write query check (must be zero results)
!`grep -rn "INSERT\|UPDATE\|DELETE\|CREATE\|DROP" . --include="*.go" | grep -v "_test.go\|vendor\|\.git" || echo "CLEAN"`

## Non-context DB calls (must be zero)
!`grep -rn "\.Query\b\|\.QueryRow\b" . --include="*.go" | grep -v "Context\|_test.go" || echo "CLEAN"`

## Endpoint count (must include /time, /monitors, /events; /healthz is the only permitted extra)
!`grep -rn "mux\.\|http\.Handle\|router\." . --include="*.go" | grep -v "_test.go"`

## Graceful shutdown wiring
!`grep -rn "SIGINT\|SIGTERM\|signal\.Notify" . --include="*.go"`

## /events null check (must return [], not null on empty)
!`grep -rn "json\|events" . --include="*.go" | grep -i "nil\|null\|empty" | head -10`

## Review
Check each section. Any write query is a blocker. Any non-context DB call is a blocker. Endpoint count must be exactly 3.
