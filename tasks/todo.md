# Task: Standardize on timeconv.Duration across all YAML-parsed configurations (Issue #1089)

## Todo List
- [x] Pull latest changes from upstream/main to local main and delete old feature branch <!-- id: 0 -->
- [x] Create new feature branch `chore/standardize-timeconv-duration` <!-- id: 1 -->
- [x] Relocate `pkg/util/timeconv` to `pkg/parsing/timeconv` and update all imports <!-- id: 2 -->
- [x] Convert all YAML-tagged `time.Duration` fields in options/configurations to `timeconv.Duration` <!-- id: 3 -->
- [x] Verify build, linter, tests, and git grep validation check <!-- id: 4 -->
- [ ] Push to remote branch and post reply on Issue #1089 <!-- id: 5 -->
