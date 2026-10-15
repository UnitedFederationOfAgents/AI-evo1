# Prompt

[Step3Prompt](Step3Prompt.md)

We must fix our tests, and also determine whether all tests even ran properly.

Here is the output:

Running tests for all AI-evo1 sub-projects...

=== Testing ufa-configurable ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/ufa-configurable'
go test -v ./...
=== RUN   TestLoadMergePrecedence
--- PASS: TestLoadMergePrecedence (0.00s)
=== RUN   TestLoadMissingFilesOK
--- PASS: TestLoadMissingFilesOK (0.00s)
=== RUN   TestNilConfigSafe
--- PASS: TestNilConfigSafe (0.00s)
=== RUN   TestParseScalarForms
--- PASS: TestParseScalarForms (0.00s)
=== RUN   TestLoadRejectsMalformed
=== RUN   TestLoadRejectsMalformed/sequence
=== RUN   TestLoadRejectsMalformed/bad_key
=== RUN   TestLoadRejectsMalformed/unterminated
=== RUN   TestLoadRejectsMalformed/duplicate_key
=== RUN   TestLoadRejectsMalformed/empty_value
=== RUN   TestLoadRejectsMalformed/no_colon
=== RUN   TestLoadRejectsMalformed/nested
--- PASS: TestLoadRejectsMalformed (0.00s)
    --- PASS: TestLoadRejectsMalformed/sequence (0.00s)
    --- PASS: TestLoadRejectsMalformed/bad_key (0.00s)
    --- PASS: TestLoadRejectsMalformed/unterminated (0.00s)
    --- PASS: TestLoadRejectsMalformed/duplicate_key (0.00s)
    --- PASS: TestLoadRejectsMalformed/empty_value (0.00s)
    --- PASS: TestLoadRejectsMalformed/no_colon (0.00s)
    --- PASS: TestLoadRejectsMalformed/nested (0.00s)
=== RUN   TestBoolAndIntErrors
--- PASS: TestBoolAndIntErrors (0.00s)
=== RUN   TestExtractConfigDir
=== RUN   TestExtractConfigDir/none
=== RUN   TestExtractConfigDir/space_form
=== RUN   TestExtractConfigDir/equals_form
=== RUN   TestExtractConfigDir/single_dash
=== RUN   TestExtractConfigDir/missing_value
--- PASS: TestExtractConfigDir (0.00s)
    --- PASS: TestExtractConfigDir/none (0.00s)
    --- PASS: TestExtractConfigDir/space_form (0.00s)
    --- PASS: TestExtractConfigDir/equals_form (0.00s)
    --- PASS: TestExtractConfigDir/single_dash (0.00s)
    --- PASS: TestExtractConfigDir/missing_value (0.00s)
=== RUN   TestDefaultDirHonoursEnv
--- PASS: TestDefaultDirHonoursEnv (0.00s)
PASS
ok  	ufa-configurable	0.007s
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/ufa-configurable'

=== Testing ufa-version ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/ufa-version'
go test -v ./...
=== RUN   TestHandleVersionFlag_NotPresent
--- PASS: TestHandleVersionFlag_NotPresent (0.00s)
=== RUN   TestHandleVersionFlag_Present
--- PASS: TestHandleVersionFlag_Present (0.00s)
=== RUN   TestHandleVersionFlag_IgnoresProgramName
--- PASS: TestHandleVersionFlag_IgnoresProgramName (0.00s)
PASS
ok  	ufa-version	0.003s
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/ufa-version'

=== Testing ufa-loader ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/ufa-loader'
go test -v ./...
=== RUN   TestLoaderRestartsUntilChildStopsAnnouncing
2026/10/08 11:58:15 launched sh (pid 3358616)
=== UFA-LOADER-RESTART ===
{"app":"test-app","reason":"loop","pid":1,"time":"t"}
=== END-UFA-LOADER-RESTART ===
2026/10/08 11:58:15 sh (pid 3358616) exited 0 after announcing a restart (loop)
2026/10/08 11:58:15 sh announced a restart (#1) — relaunching in 1ms
2026/10/08 11:58:15 launched sh (pid 3358619)
=== UFA-LOADER-RESTART ===
{"app":"test-app","reason":"loop","pid":1,"time":"t"}
=== END-UFA-LOADER-RESTART ===
2026/10/08 11:58:15 sh (pid 3358619) exited 0 after announcing a restart (loop)
2026/10/08 11:58:15 sh announced a restart (#2) — relaunching in 1ms
2026/10/08 11:58:15 launched sh (pid 3358622)
final run (3)
2026/10/08 11:58:15 sh (pid 3358622) exited 0
--- PASS: TestLoaderRestartsUntilChildStopsAnnouncing (0.02s)
=== RUN   TestLoaderRespectsMaxRestarts
2026/10/08 11:58:15 launched sh (pid 3358625)
=== UFA-LOADER-RESTART ===
{"app":"test-app","reason":"loop","pid":1,"time":"t"}
=== END-UFA-LOADER-RESTART ===
2026/10/08 11:58:15 sh (pid 3358625) exited 0 after announcing a restart (loop)
2026/10/08 11:58:15 sh announced a restart (#1) — relaunching in 1ms
2026/10/08 11:58:15 launched sh (pid 3358628)
=== UFA-LOADER-RESTART ===
{"app":"test-app","reason":"loop","pid":1,"time":"t"}
=== END-UFA-LOADER-RESTART ===
2026/10/08 11:58:15 sh (pid 3358628) exited 0 after announcing a restart (loop)
2026/10/08 11:58:15 sh asked for a restart but the 1 restart limit was reached — exiting
--- PASS: TestLoaderRespectsMaxRestarts (0.01s)
=== RUN   TestLoaderPropagatesExitCodeWithoutRestarting
2026/10/08 11:58:15 launched sh (pid 3358631)
2026/10/08 11:58:15 sh (pid 3358631) exited 7
--- PASS: TestLoaderPropagatesExitCodeWithoutRestarting (0.00s)
=== RUN   TestLoaderCarriesStateForward
2026/10/08 11:58:15 launched sh (pid 3358632)
=== UFA-LOADER-RESTART ===
{"app":"test-app","reason":"loop","pid":1,"time":"t","state":{"n":1}}
=== END-UFA-LOADER-RESTART ===
2026/10/08 11:58:15 sh (pid 3358632) exited 0 after announcing a restart (loop)
2026/10/08 11:58:15 sh announced a restart (#1) — relaunching in 1ms
2026/10/08 11:58:15 launched sh (pid 3358635)
=== UFA-LOADER-RESTART ===
{"app":"test-app","reason":"loop","pid":1,"time":"t","state":{"n":2}}
=== END-UFA-LOADER-RESTART ===
2026/10/08 11:58:15 sh (pid 3358635) exited 0 after announcing a restart (loop)
2026/10/08 11:58:15 sh announced a restart (#2) — relaunching in 1ms
2026/10/08 11:58:15 launched sh (pid 3358638)
=== UFA-LOADER-RESTART ===
{"app":"test-app","reason":"loop","pid":1,"time":"t","state":{"n":3}}
=== END-UFA-LOADER-RESTART ===
2026/10/08 11:58:15 sh (pid 3358638) exited 0 after announcing a restart (loop)
2026/10/08 11:58:15 sh announced a restart (#3) — relaunching in 1ms
2026/10/08 11:58:15 launched sh (pid 3358641)
final run (4)
2026/10/08 11:58:15 sh (pid 3358641) exited 0
    main_test.go:146: UFA_LOADER_STATE seen per launch = [{"auto_rebuild":true,"auto_update":true,"auto_connect":true,"ac_host":"localhost","ac_port":"8084","managed_apps":["condoccer","federation-command:2","robot","sessions"]} {"n":1} {"n":2} {"n":3}], want [<none> {"n":1} {"n":2} {"n":3}]
--- FAIL: TestLoaderCarriesStateForward (0.03s)
=== RUN   TestLoaderPassesThroughStdout
2026/10/08 11:58:15 launched sh (pid 3358644)
2026/10/08 11:58:15 sh (pid 3358644) exited 0
--- PASS: TestLoaderPassesThroughStdout (0.00s)
FAIL
FAIL	ufa-loader	0.067s
=== RUN   TestAnnounceScannerRoundTrip
--- PASS: TestAnnounceScannerRoundTrip (0.00s)
=== RUN   TestScannerIgnoresOrdinaryLines
--- PASS: TestScannerIgnoresOrdinaryLines (0.00s)
=== RUN   TestScannerRecoversFromMalformedBody
--- PASS: TestScannerRecoversFromMalformedBody (0.00s)
=== RUN   TestScanReaderPassesThroughAndDetects
--- PASS: TestScanReaderPassesThroughAndDetects (0.00s)
=== RUN   TestScanReaderNoAnnouncement
--- PASS: TestScanReaderNoAnnouncement (0.00s)
=== RUN   TestAnnounceStateRoundTrip
--- PASS: TestAnnounceStateRoundTrip (0.00s)
=== RUN   TestPreviousState
    restartsignal_test.go:167: expected ok=false with StateEnvVar unset
--- FAIL: TestPreviousState (0.00s)
FAIL
FAIL	ufa-loader/restartsignal	0.007s
FAIL
make[1]: *** [Makefile:24: test] Error 1
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/ufa-loader'
make: *** [Makefile:17: test-all] Error 1


Let's fix these.


prompt: [`61ace9d`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/61ace9d41afa4d6ef39d6e4dae1075242d3854b6) → reply: [`39640dd`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/39640dd7d7ede5e175cfbfd13e5181fdf7f0df10)

## Reply

I fixed the two test failures, but I haven't run anything: `go` needed approval here, so please re-run `make test` on the host. Not all tests ran last time either. `test-all` stopped at the first failing sub-project, so the 11 after `ufa-loader` never ran, and `ufa-hostid` was never on the list.

- **Cause of both failures:** your shell inherits `UFA_LOADER_STATE` from LR, because it was opened under it. That's the LR state you can see in the failure output.
  - `TestPreviousState` read that inherited value.
  - `TestLoaderCarriesStateForward` failed because of a real bug: `ufa-loader` passed any inherited state on to its child's first launch.
- **Fixes:**
  - **The bug:** `ufa-loader/main.go` now removes `UFA_LOADER_STATE` and `UFA_LOADER_INIT` from what it passes on, through a new `childEnviron()`, before setting its own values.
  - **`TestLoaderCarriesStateForward`** now sets a stale state on purpose, so it checks for this case every time.
  - **`TestPreviousState`** clears the variable first.
  - **LR's `TestLoadPreviousStateNoEnv`** in `reststate_test.go` had the same problem and would have failed next, so it clears the variable too.
- **Running every sub-project:** in the top-level `Makefile`, `test-all` now tests every sub-project even after a failure. At the end it lists the ones that failed and exits with an error.
- **`ufa-hostid` was being skipped:** it has a `test` target but wasn't in `SUBPROJECTS`. I added it, which also puts it in `build-all` and `deploy-dev-binaries`; as a library it has nothing to deploy.
- **Still skipped:** `representable` has a test file but no Makefile, so `test-all` still doesn't run it.


## Revision A

Progress, and more failures:

[claude] ~/workspace/research/AI-evo1 > make test
Running tests for all AI-evo1 sub-projects...

=== Testing ufa-configurable ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/ufa-configurable'
go test -v ./...
=== RUN   TestLoadMergePrecedence
--- PASS: TestLoadMergePrecedence (0.00s)
=== RUN   TestLoadMissingFilesOK
--- PASS: TestLoadMissingFilesOK (0.00s)
=== RUN   TestNilConfigSafe
--- PASS: TestNilConfigSafe (0.00s)
=== RUN   TestParseScalarForms
--- PASS: TestParseScalarForms (0.00s)
=== RUN   TestLoadRejectsMalformed
=== RUN   TestLoadRejectsMalformed/sequence
=== RUN   TestLoadRejectsMalformed/bad_key
=== RUN   TestLoadRejectsMalformed/unterminated
=== RUN   TestLoadRejectsMalformed/duplicate_key
=== RUN   TestLoadRejectsMalformed/empty_value
=== RUN   TestLoadRejectsMalformed/no_colon
=== RUN   TestLoadRejectsMalformed/nested
--- PASS: TestLoadRejectsMalformed (0.00s)
    --- PASS: TestLoadRejectsMalformed/sequence (0.00s)
    --- PASS: TestLoadRejectsMalformed/bad_key (0.00s)
    --- PASS: TestLoadRejectsMalformed/unterminated (0.00s)
    --- PASS: TestLoadRejectsMalformed/duplicate_key (0.00s)
    --- PASS: TestLoadRejectsMalformed/empty_value (0.00s)
    --- PASS: TestLoadRejectsMalformed/no_colon (0.00s)
    --- PASS: TestLoadRejectsMalformed/nested (0.00s)
=== RUN   TestBoolAndIntErrors
--- PASS: TestBoolAndIntErrors (0.00s)
=== RUN   TestExtractConfigDir
=== RUN   TestExtractConfigDir/none
=== RUN   TestExtractConfigDir/space_form
=== RUN   TestExtractConfigDir/equals_form
=== RUN   TestExtractConfigDir/single_dash
=== RUN   TestExtractConfigDir/missing_value
--- PASS: TestExtractConfigDir (0.00s)
    --- PASS: TestExtractConfigDir/none (0.00s)
    --- PASS: TestExtractConfigDir/space_form (0.00s)
    --- PASS: TestExtractConfigDir/equals_form (0.00s)
    --- PASS: TestExtractConfigDir/single_dash (0.00s)
    --- PASS: TestExtractConfigDir/missing_value (0.00s)
=== RUN   TestDefaultDirHonoursEnv
--- PASS: TestDefaultDirHonoursEnv (0.00s)
PASS
ok  	ufa-configurable	(cached)
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/ufa-configurable'

=== Testing ufa-version ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/ufa-version'
go test -v ./...
=== RUN   TestHandleVersionFlag_NotPresent
--- PASS: TestHandleVersionFlag_NotPresent (0.00s)
=== RUN   TestHandleVersionFlag_Present
--- PASS: TestHandleVersionFlag_Present (0.00s)
=== RUN   TestHandleVersionFlag_IgnoresProgramName
--- PASS: TestHandleVersionFlag_IgnoresProgramName (0.00s)
PASS
ok  	ufa-version	(cached)
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/ufa-version'

=== Testing ufa-hostid ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/ufa-hostid'
go test -v ./...
?   	ufa-hostid	[no test files]
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/ufa-hostid'

=== Testing ufa-loader ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/ufa-loader'
go test -v ./...
=== RUN   TestLoaderRestartsUntilChildStopsAnnouncing
2026/10/08 12:17:33 launched sh (pid 3375086)
=== UFA-LOADER-RESTART ===
{"app":"test-app","reason":"loop","pid":1,"time":"t"}
=== END-UFA-LOADER-RESTART ===
2026/10/08 12:17:33 sh (pid 3375086) exited 0 after announcing a restart (loop)
2026/10/08 12:17:33 sh announced a restart (#1) — relaunching in 1ms
2026/10/08 12:17:33 launched sh (pid 3375089)
=== UFA-LOADER-RESTART ===
{"app":"test-app","reason":"loop","pid":1,"time":"t"}
=== END-UFA-LOADER-RESTART ===
2026/10/08 12:17:33 sh (pid 3375089) exited 0 after announcing a restart (loop)
2026/10/08 12:17:33 sh announced a restart (#2) — relaunching in 1ms
2026/10/08 12:17:33 launched sh (pid 3375092)
final run (3)
2026/10/08 12:17:33 sh (pid 3375092) exited 0
--- PASS: TestLoaderRestartsUntilChildStopsAnnouncing (0.02s)
=== RUN   TestLoaderRespectsMaxRestarts
2026/10/08 12:17:33 launched sh (pid 3375095)
=== UFA-LOADER-RESTART ===
{"app":"test-app","reason":"loop","pid":1,"time":"t"}
=== END-UFA-LOADER-RESTART ===
2026/10/08 12:17:33 sh (pid 3375095) exited 0 after announcing a restart (loop)
2026/10/08 12:17:33 sh announced a restart (#1) — relaunching in 1ms
2026/10/08 12:17:33 launched sh (pid 3375098)
=== UFA-LOADER-RESTART ===
{"app":"test-app","reason":"loop","pid":1,"time":"t"}
=== END-UFA-LOADER-RESTART ===
2026/10/08 12:17:33 sh (pid 3375098) exited 0 after announcing a restart (loop)
2026/10/08 12:17:33 sh asked for a restart but the 1 restart limit was reached — exiting
--- PASS: TestLoaderRespectsMaxRestarts (0.01s)
=== RUN   TestLoaderPropagatesExitCodeWithoutRestarting
2026/10/08 12:17:33 launched sh (pid 3375101)
2026/10/08 12:17:33 sh (pid 3375101) exited 7
--- PASS: TestLoaderPropagatesExitCodeWithoutRestarting (0.00s)
=== RUN   TestLoaderCarriesStateForward
2026/10/08 12:17:33 launched sh (pid 3375102)
=== UFA-LOADER-RESTART ===
{"app":"test-app","reason":"loop","pid":1,"time":"t","state":{"n":1}}
=== END-UFA-LOADER-RESTART ===
2026/10/08 12:17:33 sh (pid 3375102) exited 0 after announcing a restart (loop)
2026/10/08 12:17:33 sh announced a restart (#1) — relaunching in 1ms
2026/10/08 12:17:33 launched sh (pid 3375105)
=== UFA-LOADER-RESTART ===
{"app":"test-app","reason":"loop","pid":1,"time":"t","state":{"n":2}}
=== END-UFA-LOADER-RESTART ===
2026/10/08 12:17:33 sh (pid 3375105) exited 0 after announcing a restart (loop)
2026/10/08 12:17:33 sh announced a restart (#2) — relaunching in 1ms
2026/10/08 12:17:33 launched sh (pid 3375108)
=== UFA-LOADER-RESTART ===
{"app":"test-app","reason":"loop","pid":1,"time":"t","state":{"n":3}}
=== END-UFA-LOADER-RESTART ===
2026/10/08 12:17:33 sh (pid 3375108) exited 0 after announcing a restart (loop)
2026/10/08 12:17:33 sh announced a restart (#3) — relaunching in 1ms
2026/10/08 12:17:33 launched sh (pid 3375111)
final run (4)
2026/10/08 12:17:33 sh (pid 3375111) exited 0
--- PASS: TestLoaderCarriesStateForward (0.02s)
=== RUN   TestLoaderPassesThroughStdout
2026/10/08 12:17:33 launched sh (pid 3375114)
2026/10/08 12:17:33 sh (pid 3375114) exited 0
--- PASS: TestLoaderPassesThroughStdout (0.00s)
PASS
ok  	ufa-loader	0.060s
=== RUN   TestAnnounceScannerRoundTrip
--- PASS: TestAnnounceScannerRoundTrip (0.00s)
=== RUN   TestScannerIgnoresOrdinaryLines
--- PASS: TestScannerIgnoresOrdinaryLines (0.00s)
=== RUN   TestScannerRecoversFromMalformedBody
--- PASS: TestScannerRecoversFromMalformedBody (0.00s)
=== RUN   TestScanReaderPassesThroughAndDetects
--- PASS: TestScanReaderPassesThroughAndDetects (0.00s)
=== RUN   TestScanReaderNoAnnouncement
--- PASS: TestScanReaderNoAnnouncement (0.00s)
=== RUN   TestAnnounceStateRoundTrip
--- PASS: TestAnnounceStateRoundTrip (0.00s)
=== RUN   TestPreviousState
--- PASS: TestPreviousState (0.00s)
PASS
ok  	ufa-loader/restartsignal	0.003s
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/ufa-loader'

=== Testing clauditable ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/clauditable'
go test -v ./...
=== RUN   TestTriggerOnceTransferPostsExpectedPullRequest
--- PASS: TestTriggerOnceTransferPostsExpectedPullRequest (0.00s)
=== RUN   TestTriggerOnceTransferNoLocalRepresentativeIsSilent
--- PASS: TestTriggerOnceTransferNoLocalRepresentativeIsSilent (0.00s)
=== RUN   TestGetSession
=== RUN   TestGetSession/with_AGENT_SESSION_set
=== RUN   TestGetSession/without_AGENT_SESSION
=== RUN   TestGetSession/with_AGENT_SESSION=default
--- PASS: TestGetSession (0.00s)
    --- PASS: TestGetSession/with_AGENT_SESSION_set (0.00s)
    --- PASS: TestGetSession/without_AGENT_SESSION (0.00s)
    --- PASS: TestGetSession/with_AGENT_SESSION=default (0.00s)
=== RUN   TestDefaultSessionIDIncludesHost
--- PASS: TestDefaultSessionIDIncludesHost (0.00s)
=== RUN   TestCheckIsPrimary
--- PASS: TestCheckIsPrimary (0.00s)
=== RUN   TestIsUnixTimestamp
=== RUN   TestIsUnixTimestamp/1234567890
=== RUN   TestIsUnixTimestamp/0
=== RUN   TestIsUnixTimestamp/123
=== RUN   TestIsUnixTimestamp/#00
=== RUN   TestIsUnixTimestamp/abc
=== RUN   TestIsUnixTimestamp/123abc
=== RUN   TestIsUnixTimestamp/session.jsonl
=== RUN   TestIsUnixTimestamp/-123
=== RUN   TestIsUnixTimestamp/12.34
=== RUN   TestIsUnixTimestamp/1234567890-raw.txt
--- PASS: TestIsUnixTimestamp (0.00s)
    --- PASS: TestIsUnixTimestamp/1234567890 (0.00s)
    --- PASS: TestIsUnixTimestamp/0 (0.00s)
    --- PASS: TestIsUnixTimestamp/123 (0.00s)
    --- PASS: TestIsUnixTimestamp/#00 (0.00s)
    --- PASS: TestIsUnixTimestamp/abc (0.00s)
    --- PASS: TestIsUnixTimestamp/123abc (0.00s)
    --- PASS: TestIsUnixTimestamp/session.jsonl (0.00s)
    --- PASS: TestIsUnixTimestamp/-123 (0.00s)
    --- PASS: TestIsUnixTimestamp/12.34 (0.00s)
    --- PASS: TestIsUnixTimestamp/1234567890-raw.txt (0.00s)
=== RUN   TestParseMetadata
=== RUN   TestParseMetadata/empty_string
=== RUN   TestParseMetadata/single_pair
=== RUN   TestParseMetadata/multiple_pairs_comma_separated
=== RUN   TestParseMetadata/multiple_pairs_semicolon_separated
=== RUN   TestParseMetadata/with_spaces
=== RUN   TestParseMetadata/invalid_format_no_equals
=== RUN   TestParseMetadata/value_with_equals_sign
--- PASS: TestParseMetadata (0.00s)
    --- PASS: TestParseMetadata/empty_string (0.00s)
    --- PASS: TestParseMetadata/single_pair (0.00s)
    --- PASS: TestParseMetadata/multiple_pairs_comma_separated (0.00s)
    --- PASS: TestParseMetadata/multiple_pairs_semicolon_separated (0.00s)
    --- PASS: TestParseMetadata/with_spaces (0.00s)
    --- PASS: TestParseMetadata/invalid_format_no_equals (0.00s)
    --- PASS: TestParseMetadata/value_with_equals_sign (0.00s)
=== RUN   TestVerbosityRelayAfterLine
--- PASS: TestVerbosityRelayAfterLine (0.00s)
=== RUN   TestVerbosityRelayRevealsOnContainingLine
--- PASS: TestVerbosityRelayRevealsOnContainingLine (0.00s)
=== RUN   TestRunPassthroughAppliesVerbosityRelay
--- PASS: TestRunPassthroughAppliesVerbosityRelay (0.00s)
=== RUN   TestVerbosityRelayOff
--- PASS: TestVerbosityRelayOff (0.00s)
=== RUN   TestExpectedRawRecordPath
--- PASS: TestExpectedRawRecordPath (0.00s)
=== RUN   TestWriteWrittenFile
--- PASS: TestWriteWrittenFile (0.00s)
=== RUN   TestSlugify
--- PASS: TestSlugify (0.00s)
=== RUN   TestGenerateSessionID
--- PASS: TestGenerateSessionID (0.00s)
=== RUN   TestEnsureSession
--- PASS: TestEnsureSession (0.00s)
=== RUN   TestConsolidatePrimaryToJSONL
--- PASS: TestConsolidatePrimaryToJSONL (0.00s)
=== RUN   TestConsolidatePrimaryToJSONLUsesProducersProcessedFile
--- PASS: TestConsolidatePrimaryToJSONLUsesProducersProcessedFile (0.00s)
=== RUN   TestConsolidatePrimaryToJSONLFoldsRemoteOriginSecondary
--- PASS: TestConsolidatePrimaryToJSONLFoldsRemoteOriginSecondary (0.00s)
=== RUN   TestConsolidatePrimaryToJSONLPromotesLocalSecondary
    main_test.go:781: consolidation should use the local secondary's own already-processed content as-is
--- FAIL: TestConsolidatePrimaryToJSONLPromotesLocalSecondary (0.00s)
FAIL
FAIL	clauditable	0.014s
=== RUN   TestFormatWithPrefix
=== RUN   TestFormatWithPrefix/empty_text
=== RUN   TestFormatWithPrefix/single_line
=== RUN   TestFormatWithPrefix/multiple_lines_under_limit
=== RUN   TestFormatWithPrefix/lines_at_limit
=== RUN   TestFormatWithPrefix/lines_over_limit
=== RUN   TestFormatWithPrefix/trailing_newline_handled
--- PASS: TestFormatWithPrefix (0.00s)
    --- PASS: TestFormatWithPrefix/empty_text (0.00s)
    --- PASS: TestFormatWithPrefix/single_line (0.00s)
    --- PASS: TestFormatWithPrefix/multiple_lines_under_limit (0.00s)
    --- PASS: TestFormatWithPrefix/lines_at_limit (0.00s)
    --- PASS: TestFormatWithPrefix/lines_over_limit (0.00s)
    --- PASS: TestFormatWithPrefix/trailing_newline_handled (0.00s)
=== RUN   TestRecordFormatSessionLog
--- PASS: TestRecordFormatSessionLog (0.00s)
=== RUN   TestRecordFormatSessionLogWithStderr
--- PASS: TestRecordFormatSessionLogWithStderr (0.00s)
=== RUN   TestRecordFormatSessionLogWithMetadata
--- PASS: TestRecordFormatSessionLogWithMetadata (0.00s)
=== RUN   TestRecordFormatRawFile
--- PASS: TestRecordFormatRawFile (0.00s)
=== RUN   TestRecordFormatRawFileWithStderr
--- PASS: TestRecordFormatRawFileWithStderr (0.00s)
=== RUN   TestParseSessionLogEntry
--- PASS: TestParseSessionLogEntry (0.00s)
=== RUN   TestParseSessionLogEntryInvalid
=== RUN   TestParseSessionLogEntryInvalid/empty
=== RUN   TestParseSessionLogEntryInvalid/no_json
=== RUN   TestParseSessionLogEntryInvalid/invalid_json
--- PASS: TestParseSessionLogEntryInvalid (0.00s)
    --- PASS: TestParseSessionLogEntryInvalid/empty (0.00s)
    --- PASS: TestParseSessionLogEntryInvalid/no_json (0.00s)
    --- PASS: TestParseSessionLogEntryInvalid/invalid_json (0.00s)
=== RUN   TestNewEvent
--- PASS: TestNewEvent (0.00s)
=== RUN   TestFormatWrittenFile
--- PASS: TestFormatWrittenFile (0.00s)
=== RUN   TestApplyAutoMaintenance
=== RUN   TestApplyAutoMaintenance/no-op_header_on_first_file_when_no_processing_needed
=== RUN   TestApplyAutoMaintenance/no_headers_on_subsequent_file_with_no_processing
=== RUN   TestApplyAutoMaintenance/loading_bar_stripping
=== RUN   TestApplyAutoMaintenance/secret_redaction_for_sk-_key
=== RUN   TestApplyAutoMaintenance/long_response_truncation
--- PASS: TestApplyAutoMaintenance (0.00s)
    --- PASS: TestApplyAutoMaintenance/no-op_header_on_first_file_when_no_processing_needed (0.00s)
    --- PASS: TestApplyAutoMaintenance/no_headers_on_subsequent_file_with_no_processing (0.00s)
    --- PASS: TestApplyAutoMaintenance/loading_bar_stripping (0.00s)
    --- PASS: TestApplyAutoMaintenance/secret_redaction_for_sk-_key (0.00s)
    --- PASS: TestApplyAutoMaintenance/long_response_truncation (0.00s)
=== RUN   TestFormatProcessedFile
=== RUN   TestFormatProcessedFile/empty_headers_returns_content_unchanged
=== RUN   TestFormatProcessedFile/processing_headers_inserted_after_first_JSON_line
--- PASS: TestFormatProcessedFile (0.00s)
    --- PASS: TestFormatProcessedFile/empty_headers_returns_content_unchanged (0.00s)
    --- PASS: TestFormatProcessedFile/processing_headers_inserted_after_first_JSON_line (0.00s)
=== RUN   TestExtractSessionLogFromWrittenFile
--- PASS: TestExtractSessionLogFromWrittenFile (0.00s)
PASS
ok  	clauditable/pkg/records	0.006s
FAIL
make[1]: *** [Makefile:31: test] Error 1
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/clauditable'

=== Testing clod ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/clod'
go test -v ./...
=== RUN   TestExtractFilePath
=== RUN   TestExtractFilePath/create_trigger_with_simple_path
=== RUN   TestExtractFilePath/create_trigger_with_full_path
=== RUN   TestExtractFilePath/create_trigger_embedded_in_text
=== RUN   TestExtractFilePath/modify_trigger_with_path
=== RUN   TestExtractFilePath/quoted_path_with_single_quotes
=== RUN   TestExtractFilePath/quoted_path_with_double_quotes
=== RUN   TestExtractFilePath/no_trigger_found
--- PASS: TestExtractFilePath (0.00s)
    --- PASS: TestExtractFilePath/create_trigger_with_simple_path (0.00s)
    --- PASS: TestExtractFilePath/create_trigger_with_full_path (0.00s)
    --- PASS: TestExtractFilePath/create_trigger_embedded_in_text (0.00s)
    --- PASS: TestExtractFilePath/modify_trigger_with_path (0.00s)
    --- PASS: TestExtractFilePath/quoted_path_with_single_quotes (0.00s)
    --- PASS: TestExtractFilePath/quoted_path_with_double_quotes (0.00s)
    --- PASS: TestExtractFilePath/no_trigger_found (0.00s)
=== RUN   TestIsTextFile
=== RUN   TestIsTextFile/file.txt
=== RUN   TestIsTextFile/file.md
=== RUN   TestIsTextFile/file.go
=== RUN   TestIsTextFile/file.py
=== RUN   TestIsTextFile/file.json
=== RUN   TestIsTextFile/file.yaml
=== RUN   TestIsTextFile/file.yml
=== RUN   TestIsTextFile/file.sh
=== RUN   TestIsTextFile/file
=== RUN   TestIsTextFile/file.png
=== RUN   TestIsTextFile/file.jpg
=== RUN   TestIsTextFile/file.jpeg
=== RUN   TestIsTextFile/file.gif
=== RUN   TestIsTextFile/file.mp4
=== RUN   TestIsTextFile/file.mov
=== RUN   TestIsTextFile/file.avi
=== RUN   TestIsTextFile/file.pdf
=== RUN   TestIsTextFile/file.exe
--- PASS: TestIsTextFile (0.00s)
    --- PASS: TestIsTextFile/file.txt (0.00s)
    --- PASS: TestIsTextFile/file.md (0.00s)
    --- PASS: TestIsTextFile/file.go (0.00s)
    --- PASS: TestIsTextFile/file.py (0.00s)
    --- PASS: TestIsTextFile/file.json (0.00s)
    --- PASS: TestIsTextFile/file.yaml (0.00s)
    --- PASS: TestIsTextFile/file.yml (0.00s)
    --- PASS: TestIsTextFile/file.sh (0.00s)
    --- PASS: TestIsTextFile/file (0.00s)
    --- PASS: TestIsTextFile/file.png (0.00s)
    --- PASS: TestIsTextFile/file.jpg (0.00s)
    --- PASS: TestIsTextFile/file.jpeg (0.00s)
    --- PASS: TestIsTextFile/file.gif (0.00s)
    --- PASS: TestIsTextFile/file.mp4 (0.00s)
    --- PASS: TestIsTextFile/file.mov (0.00s)
    --- PASS: TestIsTextFile/file.avi (0.00s)
    --- PASS: TestIsTextFile/file.pdf (0.00s)
    --- PASS: TestIsTextFile/file.exe (0.00s)
=== RUN   TestHandleCreate
=== RUN   TestHandleCreate/create_text_file
Creating file: /tmp/clod_test_2503428875/cat_story.txt
Successfully created /tmp/clod_test_2503428875/cat_story.txt with cat-themed content.
=== RUN   TestHandleCreate/create_file_in_nested_directory
Creating file: /tmp/clod_test_2503428875/nested/dir/story.txt
Successfully created /tmp/clod_test_2503428875/nested/dir/story.txt with cat-themed content.
=== RUN   TestHandleCreate/non-text_file_returns_stub
Creating file: /tmp/clod_test_2503428875/image.png
Note: Non-text file type detected. Creation not implemented for: /tmp/clod_test_2503428875/image.png
--- PASS: TestHandleCreate (0.00s)
    --- PASS: TestHandleCreate/create_text_file (0.00s)
    --- PASS: TestHandleCreate/create_file_in_nested_directory (0.00s)
    --- PASS: TestHandleCreate/non-text_file_returns_stub (0.00s)
=== RUN   TestHandleModify
=== RUN   TestHandleModify/modify_existing_text_file
Modifying file: /tmp/clod_test_1929760457/existing.txt
Successfully modified /tmp/clod_test_1929760457/existing.txt with additional cat-themed content.
--- PASS: TestHandleModify (0.00s)
    --- PASS: TestHandleModify/modify_existing_text_file (0.00s)
=== RUN   TestCatSentences
--- PASS: TestCatSentences (0.00s)
PASS
ok  	clod	0.006s
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/clod'

=== Testing ambiguous-agent ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/ambiguous-agent'
go test -v ./...
=== RUN   TestAgentConfigs
--- PASS: TestAgentConfigs (0.00s)
=== RUN   TestVerbosityManagementDefaults
--- PASS: TestVerbosityManagementDefaults (0.00s)
=== RUN   TestAgentColors
--- PASS: TestAgentColors (0.00s)
=== RUN   TestBuildAgentArgs
=== RUN   TestBuildAgentArgs/claude_read_mode
=== RUN   TestBuildAgentArgs/claude_write_mode
=== RUN   TestBuildAgentArgs/claude_execute_mode
=== RUN   TestBuildAgentArgs/claude_with_model
=== RUN   TestBuildAgentArgs/claude_with_additional_dirs
=== RUN   TestBuildAgentArgs/clod_write_mode
=== RUN   TestBuildAgentArgs/gemini_no_add-dir_support
--- PASS: TestBuildAgentArgs (0.00s)
    --- PASS: TestBuildAgentArgs/claude_read_mode (0.00s)
    --- PASS: TestBuildAgentArgs/claude_write_mode (0.00s)
    --- PASS: TestBuildAgentArgs/claude_execute_mode (0.00s)
    --- PASS: TestBuildAgentArgs/claude_with_model (0.00s)
    --- PASS: TestBuildAgentArgs/claude_with_additional_dirs (0.00s)
    --- PASS: TestBuildAgentArgs/clod_write_mode (0.00s)
    --- PASS: TestBuildAgentArgs/gemini_no_add-dir_support (0.00s)
=== RUN   TestModeDescription
=== RUN   TestModeDescription/p
=== RUN   TestModeDescription/r
=== RUN   TestModeDescription/w
=== RUN   TestModeDescription/x
=== RUN   TestModeDescription/unknown
--- PASS: TestModeDescription (0.00s)
    --- PASS: TestModeDescription/p (0.00s)
    --- PASS: TestModeDescription/r (0.00s)
    --- PASS: TestModeDescription/w (0.00s)
    --- PASS: TestModeDescription/x (0.00s)
    --- PASS: TestModeDescription/unknown (0.00s)
=== RUN   TestGetAgentStyle
--- PASS: TestGetAgentStyle (0.00s)
=== RUN   TestSessionDirCreation
--- PASS: TestSessionDirCreation (0.00s)
=== RUN   TestClodAgentConfig
--- PASS: TestClodAgentConfig (0.00s)
=== RUN   TestOpenCodePromptHandling
--- PASS: TestOpenCodePromptHandling (0.00s)
=== RUN   TestCodexPromptHandling
--- PASS: TestCodexPromptHandling (0.00s)
PASS
ok  	ambiguous-agent	0.005s
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/ambiguous-agent'

=== Testing federation-command ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/federation-command'
go test -v ./...
=== RUN   TestNewBlinker
--- PASS: TestNewBlinker (0.00s)
=== RUN   TestBlinkerDevMode
--- PASS: TestBlinkerDevMode (0.00s)
=== RUN   TestBlinkerSetState
--- PASS: TestBlinkerSetState (0.00s)
=== RUN   TestBlinkerTick
--- PASS: TestBlinkerTick (0.00s)
=== RUN   TestBlinkerTickInactive
--- PASS: TestBlinkerTickInactive (0.00s)
=== RUN   TestBlinkerViewIdle
--- PASS: TestBlinkerViewIdle (0.00s)
=== RUN   TestBlinkerViewSelect
--- PASS: TestBlinkerViewSelect (0.00s)
=== RUN   TestBlinkerViewInactive
--- PASS: TestBlinkerViewInactive (0.00s)
=== RUN   TestBlinkerStartFlash
--- PASS: TestBlinkerStartFlash (0.00s)
=== RUN   TestBlinkerAccent
--- PASS: TestBlinkerAccent (0.00s)
=== RUN   TestBlinkerShouldBlink
--- PASS: TestBlinkerShouldBlink (0.00s)
=== RUN   TestRemoveCondocLockFile
=== RUN   TestRemoveCondocLockFile/removes_an_existing_lock_file
=== RUN   TestRemoveCondocLockFile/no-ops_when_the_lock_file_is_already_absent
--- PASS: TestRemoveCondocLockFile (0.00s)
    --- PASS: TestRemoveCondocLockFile/removes_an_existing_lock_file (0.00s)
    --- PASS: TestRemoveCondocLockFile/no-ops_when_the_lock_file_is_already_absent (0.00s)
=== RUN   TestFallbackDefaultSessionIDIncludesHost
--- PASS: TestFallbackDefaultSessionIDIncludesHost (0.00s)
=== RUN   TestVersion
--- PASS: TestVersion (2.66s)
=== RUN   TestVersionShortFlag
--- PASS: TestVersionShortFlag (1.08s)
=== RUN   TestParseRidealongCommand
=== RUN   TestParseRidealongCommand/ridealong_tour.md
=== RUN   TestParseRidealongCommand/ridealong_docs/tours/brief-tour.md
=== RUN   TestParseRidealongCommand/ridealong_--debug_tour.md
=== RUN   TestParseRidealongCommand/ridealong_--debug_docs/tours/brief-tour.md
=== RUN   TestParseRidealongCommand/ridealong_--debug
=== RUN   TestParseRidealongCommand/ridealong
--- PASS: TestParseRidealongCommand (0.00s)
    --- PASS: TestParseRidealongCommand/ridealong_tour.md (0.00s)
    --- PASS: TestParseRidealongCommand/ridealong_docs/tours/brief-tour.md (0.00s)
    --- PASS: TestParseRidealongCommand/ridealong_--debug_tour.md (0.00s)
    --- PASS: TestParseRidealongCommand/ridealong_--debug_docs/tours/brief-tour.md (0.00s)
    --- PASS: TestParseRidealongCommand/ridealong_--debug (0.00s)
    --- PASS: TestParseRidealongCommand/ridealong (0.00s)
=== RUN   TestStripSurroundingQuotes
=== RUN   TestStripSurroundingQuotes/"My_New_Session"
=== RUN   TestStripSurroundingQuotes/'My_New_Session'
=== RUN   TestStripSurroundingQuotes/My_New_Session
=== RUN   TestStripSurroundingQuotes/"unterminated
=== RUN   TestStripSurroundingQuotes/"
=== RUN   TestStripSurroundingQuotes/#00
=== RUN   TestStripSurroundingQuotes/"mismatched'
--- PASS: TestStripSurroundingQuotes (0.00s)
    --- PASS: TestStripSurroundingQuotes/"My_New_Session" (0.00s)
    --- PASS: TestStripSurroundingQuotes/'My_New_Session' (0.00s)
    --- PASS: TestStripSurroundingQuotes/My_New_Session (0.00s)
    --- PASS: TestStripSurroundingQuotes/"unterminated (0.00s)
    --- PASS: TestStripSurroundingQuotes/" (0.00s)
    --- PASS: TestStripSurroundingQuotes/#00 (0.00s)
    --- PASS: TestStripSurroundingQuotes/"mismatched' (0.00s)
=== RUN   TestUpdateSessionNameStampsOwnerOnCreate
--- PASS: TestUpdateSessionNameStampsOwnerOnCreate (0.00s)
=== RUN   TestIsValidAgent
=== RUN   TestIsValidAgent/claude
=== RUN   TestIsValidAgent/gemini
=== RUN   TestIsValidAgent/copilot
=== RUN   TestIsValidAgent/opencode
=== RUN   TestIsValidAgent/codex
=== RUN   TestIsValidAgent/grok
=== RUN   TestIsValidAgent/clod
=== RUN   TestIsValidAgent/unknown
=== RUN   TestIsValidAgent/#00
--- PASS: TestIsValidAgent (0.00s)
    --- PASS: TestIsValidAgent/claude (0.00s)
    --- PASS: TestIsValidAgent/gemini (0.00s)
    --- PASS: TestIsValidAgent/copilot (0.00s)
    --- PASS: TestIsValidAgent/opencode (0.00s)
    --- PASS: TestIsValidAgent/codex (0.00s)
    --- PASS: TestIsValidAgent/grok (0.00s)
    --- PASS: TestIsValidAgent/clod (0.00s)
    --- PASS: TestIsValidAgent/unknown (0.00s)
    --- PASS: TestIsValidAgent/#00 (0.00s)
=== RUN   TestIsValidVarName
=== RUN   TestIsValidVarName/FOO
=== RUN   TestIsValidVarName/foo
=== RUN   TestIsValidVarName/_foo
=== RUN   TestIsValidVarName/FOO_BAR
=== RUN   TestIsValidVarName/FOO123
=== RUN   TestIsValidVarName/123FOO
=== RUN   TestIsValidVarName/-FOO
=== RUN   TestIsValidVarName/#00
=== RUN   TestIsValidVarName/foo-bar
--- PASS: TestIsValidVarName (0.00s)
    --- PASS: TestIsValidVarName/FOO (0.00s)
    --- PASS: TestIsValidVarName/foo (0.00s)
    --- PASS: TestIsValidVarName/_foo (0.00s)
    --- PASS: TestIsValidVarName/FOO_BAR (0.00s)
    --- PASS: TestIsValidVarName/FOO123 (0.00s)
    --- PASS: TestIsValidVarName/123FOO (0.00s)
    --- PASS: TestIsValidVarName/-FOO (0.00s)
    --- PASS: TestIsValidVarName/#00 (0.00s)
    --- PASS: TestIsValidVarName/foo-bar (0.00s)
=== RUN   TestAbbreviatePath
=== RUN   TestAbbreviatePath//home/user
=== RUN   TestAbbreviatePath//home/user/projects
=== RUN   TestAbbreviatePath//
=== RUN   TestAbbreviatePath//home/user/very/long/path/that/exceeds/limit
--- PASS: TestAbbreviatePath (0.00s)
    --- PASS: TestAbbreviatePath//home/user (0.00s)
    --- PASS: TestAbbreviatePath//home/user/projects (0.00s)
    --- PASS: TestAbbreviatePath// (0.00s)
    --- PASS: TestAbbreviatePath//home/user/very/long/path/that/exceeds/limit (0.00s)
=== RUN   TestParseArgs
=== RUN   TestParseArgs/hello_world
=== RUN   TestParseArgs/"hello_world"
=== RUN   TestParseArgs/'hello_world'
=== RUN   TestParseArgs/-p_"test_prompt"
=== RUN   TestParseArgs/-r_file.txt
=== RUN   TestParseArgs/#00
--- PASS: TestParseArgs (0.00s)
    --- PASS: TestParseArgs/hello_world (0.00s)
    --- PASS: TestParseArgs/"hello_world" (0.00s)
    --- PASS: TestParseArgs/'hello_world' (0.00s)
    --- PASS: TestParseArgs/-p_"test_prompt" (0.00s)
    --- PASS: TestParseArgs/-r_file.txt (0.00s)
    --- PASS: TestParseArgs/#00 (0.00s)
=== RUN   TestCheckContinuation
=== RUN   TestCheckContinuation/echo_hello
=== RUN   TestCheckContinuation/echo_hello_\
=== RUN   TestCheckContinuation/echo_"hello
=== RUN   TestCheckContinuation/echo_'hello
=== RUN   TestCheckContinuation/echo_"hello"
=== RUN   TestCheckContinuation/echo_'hello'
--- PASS: TestCheckContinuation (0.00s)
    --- PASS: TestCheckContinuation/echo_hello (0.00s)
    --- PASS: TestCheckContinuation/echo_hello_\ (0.00s)
    --- PASS: TestCheckContinuation/echo_"hello (0.00s)
    --- PASS: TestCheckContinuation/echo_'hello (0.00s)
    --- PASS: TestCheckContinuation/echo_"hello" (0.00s)
    --- PASS: TestCheckContinuation/echo_'hello' (0.00s)
=== RUN   TestModeDescription
=== RUN   TestModeDescription/p
=== RUN   TestModeDescription/r
=== RUN   TestModeDescription/w
=== RUN   TestModeDescription/x
=== RUN   TestModeDescription/unknown
--- PASS: TestModeDescription (0.00s)
    --- PASS: TestModeDescription/p (0.00s)
    --- PASS: TestModeDescription/r (0.00s)
    --- PASS: TestModeDescription/w (0.00s)
    --- PASS: TestModeDescription/x (0.00s)
    --- PASS: TestModeDescription/unknown (0.00s)
=== RUN   TestParseCLIArgs
=== RUN   TestParseCLIArgs/no_args
    main_test.go:381: autoConnect = true, want false
    main_test.go:384: devMode = true, want false
=== RUN   TestParseCLIArgs/auto-connect_long
    main_test.go:384: devMode = true, want false
=== RUN   TestParseCLIArgs/auto-connect_short
    main_test.go:384: devMode = true, want false
=== RUN   TestParseCLIArgs/lr-port_separate
    main_test.go:381: autoConnect = true, want false
    main_test.go:384: devMode = true, want false
=== RUN   TestParseCLIArgs/lr-port_equals
    main_test.go:381: autoConnect = true, want false
    main_test.go:384: devMode = true, want false
=== RUN   TestParseCLIArgs/auto-connect_with_port
    main_test.go:384: devMode = true, want false
=== RUN   TestParseCLIArgs/dev-mode_long
    main_test.go:381: autoConnect = true, want false
=== RUN   TestParseCLIArgs/dev-mode_short
    main_test.go:381: autoConnect = true, want false
=== RUN   TestParseCLIArgs/version_handled
federation-command dev
=== RUN   TestParseCLIArgs/lr-port_missing_value
=== RUN   TestParseCLIArgs/lr-port_not_a_number
=== RUN   TestParseCLIArgs/lr-port_out_of_range
=== RUN   TestParseCLIArgs/unknown_ignored
    main_test.go:384: devMode = true, want false
--- FAIL: TestParseCLIArgs (0.00s)
    --- FAIL: TestParseCLIArgs/no_args (0.00s)
    --- FAIL: TestParseCLIArgs/auto-connect_long (0.00s)
    --- FAIL: TestParseCLIArgs/auto-connect_short (0.00s)
    --- FAIL: TestParseCLIArgs/lr-port_separate (0.00s)
    --- FAIL: TestParseCLIArgs/lr-port_equals (0.00s)
    --- FAIL: TestParseCLIArgs/auto-connect_with_port (0.00s)
    --- FAIL: TestParseCLIArgs/dev-mode_long (0.00s)
    --- FAIL: TestParseCLIArgs/dev-mode_short (0.00s)
    --- PASS: TestParseCLIArgs/version_handled (0.00s)
    --- PASS: TestParseCLIArgs/lr-port_missing_value (0.00s)
    --- PASS: TestParseCLIArgs/lr-port_not_a_number (0.00s)
    --- PASS: TestParseCLIArgs/lr-port_out_of_range (0.00s)
    --- FAIL: TestParseCLIArgs/unknown_ignored (0.00s)
=== RUN   TestParseCLIArgsConfigFilePrecedence
    main_test.go:415: lrAddr = "localhost:8082", want globalhost:7100
--- FAIL: TestParseCLIArgsConfigFilePrecedence (0.00s)
=== RUN   TestParseCLIArgsRejectsBadConfig
--- PASS: TestParseCLIArgsRejectsBadConfig (0.00s)
=== RUN   TestAutoConnectControlState
=== RUN   TestAutoConnectControlState/dot_selected_->_remote
=== RUN   TestAutoConnectControlState/manual_connect_in_flight_->_remote
=== RUN   TestAutoConnectControlState/idle_entry_prompt_->_local
=== RUN   TestAutoConnectControlState/typing_at_prompt_->_local
=== RUN   TestAutoConnectControlState/preferRemote_forces_remote_from_idle
=== RUN   TestAutoConnectControlState/preferRemote_forces_remote_while_typing
--- PASS: TestAutoConnectControlState (0.00s)
    --- PASS: TestAutoConnectControlState/dot_selected_->_remote (0.00s)
    --- PASS: TestAutoConnectControlState/manual_connect_in_flight_->_remote (0.00s)
    --- PASS: TestAutoConnectControlState/idle_entry_prompt_->_local (0.00s)
    --- PASS: TestAutoConnectControlState/typing_at_prompt_->_local (0.00s)
    --- PASS: TestAutoConnectControlState/preferRemote_forces_remote_from_idle (0.00s)
    --- PASS: TestAutoConnectControlState/preferRemote_forces_remote_while_typing (0.00s)
=== RUN   TestAutoConnectImpliesRemote
    main_test.go:495: defaults should be local control (no auto-connect): {autoConnect:true remote:true lrAddr:localhost:8082 devMode:true session:}
    main_test.go:504: bare --remote should be ignored now: {autoConnect:true remote:true lrAddr:localhost:8082 devMode:true session:}
--- FAIL: TestAutoConnectImpliesRemote (0.00s)
=== RUN   TestParseCLIArgsEnvOverrides
=== RUN   TestParseCLIArgsEnvOverrides/FC_AUTO_CONNECT_implies_remote_and_sets_the_address
=== RUN   TestParseCLIArgsEnvOverrides/FC_AUTO_CONNECT=off_keeps_local_control
=== RUN   TestParseCLIArgsEnvOverrides/CLI_flag_beats_env
=== RUN   TestParseCLIArgsEnvOverrides/FC_DEV_MODE_cascades_a_launching_LR's_dev_mode
=== RUN   TestParseCLIArgsEnvOverrides/dev-mode_CLI_flag_beats_env
=== RUN   TestParseCLIArgsEnvOverrides/bad_FC_LR_PORT_is_an_error
--- PASS: TestParseCLIArgsEnvOverrides (0.00s)
    --- PASS: TestParseCLIArgsEnvOverrides/FC_AUTO_CONNECT_implies_remote_and_sets_the_address (0.00s)
    --- PASS: TestParseCLIArgsEnvOverrides/FC_AUTO_CONNECT=off_keeps_local_control (0.00s)
    --- PASS: TestParseCLIArgsEnvOverrides/CLI_flag_beats_env (0.00s)
    --- PASS: TestParseCLIArgsEnvOverrides/FC_DEV_MODE_cascades_a_launching_LR's_dev_mode (0.00s)
    --- PASS: TestParseCLIArgsEnvOverrides/dev-mode_CLI_flag_beats_env (0.00s)
    --- PASS: TestParseCLIArgsEnvOverrides/bad_FC_LR_PORT_is_an_error (0.00s)
=== RUN   TestEnableDisableAutoConnect
--- PASS: TestEnableDisableAutoConnect (0.00s)
=== RUN   TestEnableAutoConnectNoopWhenAlreadyConnected
--- PASS: TestEnableAutoConnectNoopWhenAlreadyConnected (0.00s)
=== RUN   TestAutoConnectStatusLine
--- PASS: TestAutoConnectStatusLine (0.00s)
=== RUN   TestDisconnectReprClearsAutoConnect
--- PASS: TestDisconnectReprClearsAutoConnect (0.00s)
FAIL
FAIL	federation-command	3.753s
FAIL
make[1]: *** [Makefile:39: test] Error 1
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/federation-command'

=== Testing dungeon-keeper ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/dungeon-keeper'
go test -v ./...
=== RUN   TestLoadConfig
--- PASS: TestLoadConfig (0.00s)
=== RUN   TestFormatDuration
=== RUN   TestFormatDuration/30s
=== RUN   TestFormatDuration/1m
=== RUN   TestFormatDuration/5m
=== RUN   TestFormatDuration/1h
=== RUN   TestFormatDuration/5h
=== RUN   TestFormatDuration/1d
=== RUN   TestFormatDuration/2d
--- PASS: TestFormatDuration (0.00s)
    --- PASS: TestFormatDuration/30s (0.00s)
    --- PASS: TestFormatDuration/1m (0.00s)
    --- PASS: TestFormatDuration/5m (0.00s)
    --- PASS: TestFormatDuration/1h (0.00s)
    --- PASS: TestFormatDuration/5h (0.00s)
    --- PASS: TestFormatDuration/1d (0.00s)
    --- PASS: TestFormatDuration/2d (0.00s)
=== RUN   TestBackoffLevels
--- PASS: TestBackoffLevels (0.00s)
=== RUN   TestRequiredSlopspaceID
=== RUN   TestRequiredSlopspaceID/present
=== RUN   TestRequiredSlopspaceID/missing
=== RUN   TestRequiredSlopspaceID/flag_is_not_id
--- PASS: TestRequiredSlopspaceID (0.00s)
    --- PASS: TestRequiredSlopspaceID/present (0.00s)
    --- PASS: TestRequiredSlopspaceID/missing (0.00s)
    --- PASS: TestRequiredSlopspaceID/flag_is_not_id (0.00s)
=== RUN   TestNewWorker
--- PASS: TestNewWorker (0.00s)
=== RUN   TestWorkerEnsureDirectories
--- PASS: TestWorkerEnsureDirectories (0.00s)
PASS
ok  	dungeon-keeper	0.006s
=== RUN   TestNewExecutorWithOptions
--- PASS: TestNewExecutorWithOptions (0.00s)
=== RUN   TestWithClauditablePath
--- PASS: TestWithClauditablePath (0.00s)
=== RUN   TestIsClauditableWrapped
--- PASS: TestIsClauditableWrapped (0.00s)
=== RUN   TestCommandWithoutClauditable
--- PASS: TestCommandWithoutClauditable (0.00s)
=== RUN   TestCommandWithClauditable
--- PASS: TestCommandWithClauditable (0.00s)
=== RUN   TestFormatPromptForAgent
--- PASS: TestFormatPromptForAgent (0.00s)
=== RUN   TestFindBinaryLocal
--- PASS: TestFindBinaryLocal (0.00s)
=== RUN   TestFindBinaryNotFound
--- PASS: TestFindBinaryNotFound (0.00s)
=== RUN   TestAgentModeMapping
=== RUN   TestAgentModeMapping/execute
=== RUN   TestAgentModeMapping/e
=== RUN   TestAgentModeMapping/x
=== RUN   TestAgentModeMapping/write
=== RUN   TestAgentModeMapping/w
=== RUN   TestAgentModeMapping/read
=== RUN   TestAgentModeMapping/r
=== RUN   TestAgentModeMapping/prompt
=== RUN   TestAgentModeMapping/p
--- PASS: TestAgentModeMapping (0.00s)
    --- PASS: TestAgentModeMapping/execute (0.00s)
    --- PASS: TestAgentModeMapping/e (0.00s)
    --- PASS: TestAgentModeMapping/x (0.00s)
    --- PASS: TestAgentModeMapping/write (0.00s)
    --- PASS: TestAgentModeMapping/w (0.00s)
    --- PASS: TestAgentModeMapping/read (0.00s)
    --- PASS: TestAgentModeMapping/r (0.00s)
    --- PASS: TestAgentModeMapping/prompt (0.00s)
    --- PASS: TestAgentModeMapping/p (0.00s)
PASS
ok  	dungeon-keeper/pkg/executor	0.005s
?   	dungeon-keeper/pkg/readspace	[no test files]
=== RUN   TestManagerCreate
--- PASS: TestManagerCreate (0.00s)
=== RUN   TestManagerGetAndList
--- PASS: TestManagerGetAndList (0.00s)
=== RUN   TestManagerDeployAndReturn
--- PASS: TestManagerDeployAndReturn (0.00s)
=== RUN   TestManagerDeployToDifferentAgentTypes
--- PASS: TestManagerDeployToDifferentAgentTypes (0.00s)
=== RUN   TestManagerDeployAlreadyDeployed
--- PASS: TestManagerDeployAlreadyDeployed (0.00s)
=== RUN   TestManagerDelete
--- PASS: TestManagerDelete (0.00s)
=== RUN   TestManagerDeleteDeployed
--- PASS: TestManagerDeleteDeployed (0.00s)
=== RUN   TestManagerPopulateSpaces
--- PASS: TestManagerPopulateSpaces (0.00s)
=== RUN   TestManagerPopulateWhileDeployed
--- PASS: TestManagerPopulateWhileDeployed (0.00s)
PASS
ok  	dungeon-keeper/pkg/slopspace	0.016s
=== RUN   TestDefaultConfig
--- PASS: TestDefaultConfig (0.00s)
=== RUN   TestConfigDeployPath
--- PASS: TestConfigDeployPath (0.00s)
=== RUN   TestConfigDeployPathForAgentType
--- PASS: TestConfigDeployPathForAgentType (0.00s)
=== RUN   TestConfigWorkDirs
--- PASS: TestConfigWorkDirs (0.00s)
=== RUN   TestAgentTypeConstants
--- PASS: TestAgentTypeConstants (0.00s)
=== RUN   TestWorkStatusConstants
--- PASS: TestWorkStatusConstants (0.00s)
=== RUN   TestWorkTypeConstants
--- PASS: TestWorkTypeConstants (0.00s)
PASS
ok  	dungeon-keeper/pkg/types	0.003s
=== RUN   TestGenerateFilename
=== RUN   TestGenerateFilename/basic_working
=== RUN   TestGenerateFilename/basic_complete
=== RUN   TestGenerateFilename/with_spaces
=== RUN   TestGenerateFilename/with_special_chars
--- PASS: TestGenerateFilename (0.00s)
    --- PASS: TestGenerateFilename/basic_working (0.00s)
    --- PASS: TestGenerateFilename/basic_complete (0.00s)
    --- PASS: TestGenerateFilename/with_spaces (0.00s)
    --- PASS: TestGenerateFilename/with_special_chars (0.00s)
=== RUN   TestParseFilename
=== RUN   TestParseFilename/WORKING-cat_webserver_container-1777744989.jsonl
=== RUN   TestParseFilename/COMPLETE-cat_webserver_1-1777744989.jsonl
=== RUN   TestParseFilename/invalid.jsonl
=== RUN   TestParseFilename/WORKING-notsimestamp.jsonl
--- PASS: TestParseFilename (0.00s)
    --- PASS: TestParseFilename/WORKING-cat_webserver_container-1777744989.jsonl (0.00s)
    --- PASS: TestParseFilename/COMPLETE-cat_webserver_1-1777744989.jsonl (0.00s)
    --- PASS: TestParseFilename/invalid.jsonl (0.00s)
    --- PASS: TestParseFilename/WORKING-notsimestamp.jsonl (0.00s)
=== RUN   TestManagerCreateAndRead
--- PASS: TestManagerCreateAndRead (0.00s)
=== RUN   TestManagerAppendEvent
--- PASS: TestManagerAppendEvent (0.00s)
=== RUN   TestManagerTakeAndReleaseOwnership
--- PASS: TestManagerTakeAndReleaseOwnership (0.00s)
=== RUN   TestManagerComplete
--- PASS: TestManagerComplete (0.00s)
=== RUN   TestManagerFindPendingForAgentType
--- PASS: TestManagerFindPendingForAgentType (0.00s)
PASS
ok  	dungeon-keeper/pkg/worksignal	0.007s
?   	dungeon-keeper/pkg/writespace	[no test files]
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/dungeon-keeper'

=== Testing condoccer ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/condoccer'
go test -v ./...
=== RUN   TestUpdateCondocLockFirstSighting
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
--- PASS: TestUpdateCondocLockFirstSighting (0.00s)
=== RUN   TestUpdateCondocLockTransitions
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
--- PASS: TestUpdateCondocLockTransitions (0.01s)
=== RUN   TestPushCondoccerStateNoClient
--- PASS: TestPushCondoccerStateNoClient (0.00s)
=== RUN   TestHandleReprCommandToleratesJunk
2026/10/08 12:17:45 repr: ignoring unrecognised command "__condoccer:"
2026/10/08 12:17:45 repr: ignoring unrecognised command "__condoccer:bogus arg"
2026/10/08 12:17:45 repr: bad __condoccer:action payload: invalid character 'n' looking for beginning of object key string
--- PASS: TestHandleReprCommandToleratesJunk (0.00s)
=== RUN   TestCondoccerStateMsgShape
--- PASS: TestCondoccerStateMsgShape (0.00s)
=== RUN   TestStartConnectLoopThenStop
--- PASS: TestStartConnectLoopThenStop (0.05s)
=== RUN   TestStopConnectLoopNoopWhenIdle
--- PASS: TestStopConnectLoopNoopWhenIdle (0.00s)
=== RUN   TestSetAutoConnectArmsRetryLoopAndFlag
--- PASS: TestSetAutoConnectArmsRetryLoopAndFlag (0.00s)
=== RUN   TestConnectLoopResumesAfterUnintentionalDisconnect
2026/10/08 12:17:45 connected to local-representative at 127.0.0.1:46131 as "condoccer"
2026/10/08 12:17:45 disconnected from local-representative at 127.0.0.1:46131 — auto-connect resuming the retry cycle
--- PASS: TestConnectLoopResumesAfterUnintentionalDisconnect (0.01s)
=== RUN   TestConnectLoopDoesNotResumeWithoutAutoConnect
2026/10/08 12:17:45 connected to local-representative at 127.0.0.1:43343 as "condoccer"
2026/10/08 12:17:45 disconnected from local-representative at 127.0.0.1:43343
--- PASS: TestConnectLoopDoesNotResumeWithoutAutoConnect (0.21s)
=== RUN   TestNextResourceNum
--- PASS: TestNextResourceNum (0.00s)
=== RUN   TestInsertResourceBlockAboveDPlaceholder
--- PASS: TestInsertResourceBlockAboveDPlaceholder (0.00s)
=== RUN   TestInsertResourceBlockWithName
--- PASS: TestInsertResourceBlockWithName (0.00s)
=== RUN   TestInsertResourceBlockNoDescription
--- PASS: TestInsertResourceBlockNoDescription (0.00s)
=== RUN   TestInsertResourceBlockNoPlaceholder
--- PASS: TestInsertResourceBlockNoPlaceholder (0.00s)
=== RUN   TestFetchHighlightedFilesFrom
--- PASS: TestFetchHighlightedFilesFrom (0.00s)
=== RUN   TestFetchHighlightedFilesFromNoneHighlighted
--- PASS: TestFetchHighlightedFilesFromNoneHighlighted (0.00s)
=== RUN   TestFetchAllHighlightedFilesIncludesPeers
2026/10/08 12:17:45 add resource: highlighted files on node-c: listing local-representative's files: unexpected status 404
--- PASS: TestFetchAllHighlightedFilesIncludesPeers (0.00s)
=== RUN   TestFetchAllHighlightedFilesOlderLR
--- PASS: TestFetchAllHighlightedFilesOlderLR (0.00s)
=== RUN   TestAddResourceRequiresConnection
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
--- PASS: TestAddResourceRequiresConnection (0.01s)
=== RUN   TestAddResourceUnknownType
--- PASS: TestAddResourceUnknownType (0.00s)
=== RUN   TestAddVoiceNoteResource
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
    resources_test.go:311: expected no files written to the Impls folder, got [- Step1Prompt.md]
--- FAIL: TestAddVoiceNoteResource (0.01s)
=== RUN   TestAddVoiceNoteResourceRequiresContent
--- PASS: TestAddVoiceNoteResourceRequiresContent (0.00s)
=== RUN   TestParseIterationsResource
--- PASS: TestParseIterationsResource (0.00s)
=== RUN   TestHandleResourceFile
--- PASS: TestHandleResourceFile (0.01s)
=== RUN   TestHandleUploadResource
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
--- PASS: TestHandleUploadResource (0.01s)
=== RUN   TestHandleUploadResourceRejectsNonPost
--- PASS: TestHandleUploadResourceRejectsNonPost (0.00s)
=== RUN   TestHandleUploadResourceRequiresFile
--- PASS: TestHandleUploadResourceRequiresFile (0.00s)
=== RUN   TestHandleUploadResourceRejectsBadPath
--- PASS: TestHandleUploadResourceRejectsBadPath (0.00s)
=== RUN   TestSaveUploadedResourceCollisionFree
--- PASS: TestSaveUploadedResourceCollisionFree (0.00s)
=== RUN   TestSanitizeFilename
--- PASS: TestSanitizeFilename (0.00s)
=== RUN   TestExtractZipResource
--- PASS: TestExtractZipResource (0.00s)
=== RUN   TestExtractZipResourceIgnoresNonZip
--- PASS: TestExtractZipResourceIgnoresNonZip (0.00s)
=== RUN   TestExtractZipResourceRejectsTraversal
--- PASS: TestExtractZipResourceRejectsTraversal (0.00s)
=== RUN   TestExtractZipResourceInvalidZip
--- PASS: TestExtractZipResourceInvalidZip (0.00s)
=== RUN   TestHandleUploadResourceExtractsZip
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
--- PASS: TestHandleUploadResourceExtractsZip (0.00s)
=== RUN   TestFetchHighlightedFilesFromExtractsZip
--- PASS: TestFetchHighlightedFilesFromExtractsZip (0.00s)
=== RUN   TestStepLastEventIsSubstep
=== RUN   TestStepLastEventIsSubstep/no_substep_at_all
=== RUN   TestStepLastEventIsSubstep/substep_is_the_last_event
=== RUN   TestStepLastEventIsSubstep/a_reply_landed_on_the_step_after_the_substep
--- PASS: TestStepLastEventIsSubstep (0.00s)
    --- PASS: TestStepLastEventIsSubstep/no_substep_at_all (0.00s)
    --- PASS: TestStepLastEventIsSubstep/substep_is_the_last_event (0.00s)
    --- PASS: TestStepLastEventIsSubstep/a_reply_landed_on_the_step_after_the_substep (0.00s)
=== RUN   TestDetectPhaseJustResumedFromSubstep
--- PASS: TestDetectPhaseJustResumedFromSubstep (0.00s)
=== RUN   TestUpdateCondocLockWithholdsRemovalForSubstepCompletion
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
2026/10/08 12:17:45 condoc lock: git add failed: exit status 128
--- PASS: TestUpdateCondocLockWithholdsRemovalForSubstepCompletion (0.01s)
=== RUN   TestGetCondocStateCompletedSubstepContents
--- PASS: TestGetCondocStateCompletedSubstepContents (0.00s)
=== RUN   TestHandleTCAvailabilityCommand
--- PASS: TestHandleTCAvailabilityCommand (0.00s)
=== RUN   TestHandleReprCommandIgnoresTCAvailabilityAsCondoccerNamespace
--- PASS: TestHandleReprCommandIgnoresTCAvailabilityAsCondoccerNamespace (0.00s)
FAIL
FAIL	condoccer	0.347s
FAIL
make[1]: *** [Makefile:55: test] Error 1
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/condoccer'

=== Testing session-manager ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/session-manager'
go test -v ./...
=== RUN   TestPushSessionsStateNoClient
--- PASS: TestPushSessionsStateNoClient (0.00s)
=== RUN   TestHandleReprCommandToleratesJunk
2026/10/08 12:17:46 repr: ignoring unrecognised command "__sessions:"
2026/10/08 12:17:46 repr: ignoring unrecognised command "__sessions:bogus arg"
--- PASS: TestHandleReprCommandToleratesJunk (0.00s)
=== RUN   TestSessionsStateMsgShape
--- PASS: TestSessionsStateMsgShape (0.00s)
=== RUN   TestStartConnectLoopThenStop
--- PASS: TestStartConnectLoopThenStop (0.05s)
=== RUN   TestStopConnectLoopNoopWhenIdle
--- PASS: TestStopConnectLoopNoopWhenIdle (0.00s)
=== RUN   TestSetAutoConnectArmsRetryLoopAndFlag
--- PASS: TestSetAutoConnectArmsRetryLoopAndFlag (0.00s)
=== RUN   TestConnectLoopResumesAfterUnintentionalDisconnect
2026/10/08 12:17:46 connected to local-representative at 127.0.0.1:43793 as "sessions"
2026/10/08 12:17:46 disconnected from local-representative at 127.0.0.1:43793 — auto-connect resuming the retry cycle
--- PASS: TestConnectLoopResumesAfterUnintentionalDisconnect (0.01s)
=== RUN   TestConnectLoopDoesNotResumeWithoutAutoConnect
2026/10/08 12:17:46 connected to local-representative at 127.0.0.1:44997 as "sessions"
2026/10/08 12:17:46 disconnected from local-representative at 127.0.0.1:44997
--- PASS: TestConnectLoopDoesNotResumeWithoutAutoConnect (0.22s)
=== RUN   TestCreateAndListSessions
--- PASS: TestCreateAndListSessions (0.00s)
=== RUN   TestCreateSessionRecordsOwner
--- PASS: TestCreateSessionRecordsOwner (0.00s)
=== RUN   TestListSessionsMissingRecordsPath
--- PASS: TestListSessionsMissingRecordsPath (0.00s)
=== RUN   TestDescribeSessionEmptyFieldsMarshalsAsArray
--- PASS: TestDescribeSessionEmptyFieldsMarshalsAsArray (0.00s)
=== RUN   TestRenameSessionRejectsDefault
--- PASS: TestRenameSessionRejectsDefault (0.00s)
=== RUN   TestRenameSessionUpdatesName
--- PASS: TestRenameSessionUpdatesName (0.00s)
=== RUN   TestArchiveSessionsMovesAll
--- PASS: TestArchiveSessionsMovesAll (0.00s)
=== RUN   TestArchiveSessionsNoneToArchive
--- PASS: TestArchiveSessionsNoneToArchive (0.00s)
=== RUN   TestParseSessionLog
--- PASS: TestParseSessionLog (0.00s)
=== RUN   TestParseSessionLogSkipsProcessingHeaders
--- PASS: TestParseSessionLogSkipsProcessingHeaders (0.00s)
=== RUN   TestViewSessionNoLog
--- PASS: TestViewSessionNoLog (0.00s)
=== RUN   TestSlugify
--- PASS: TestSlugify (0.00s)
=== RUN   TestStripSurroundingQuotes
=== RUN   TestStripSurroundingQuotes/"My_New_Session"
=== RUN   TestStripSurroundingQuotes/'My_New_Session'
=== RUN   TestStripSurroundingQuotes/My_New_Session
=== RUN   TestStripSurroundingQuotes/"unterminated
=== RUN   TestStripSurroundingQuotes/"
=== RUN   TestStripSurroundingQuotes/#00
=== RUN   TestStripSurroundingQuotes/"mismatched'
--- PASS: TestStripSurroundingQuotes (0.00s)
    --- PASS: TestStripSurroundingQuotes/"My_New_Session" (0.00s)
    --- PASS: TestStripSurroundingQuotes/'My_New_Session' (0.00s)
    --- PASS: TestStripSurroundingQuotes/My_New_Session (0.00s)
    --- PASS: TestStripSurroundingQuotes/"unterminated (0.00s)
    --- PASS: TestStripSurroundingQuotes/" (0.00s)
    --- PASS: TestStripSurroundingQuotes/#00 (0.00s)
    --- PASS: TestStripSurroundingQuotes/"mismatched' (0.00s)
=== RUN   TestCreateSessionStripsQuotes
--- PASS: TestCreateSessionStripsQuotes (0.00s)
=== RUN   TestRenameSessionStripsQuotes
--- PASS: TestRenameSessionStripsQuotes (0.00s)
=== RUN   TestTriggerSessionSyncNoClientIsANoOp
--- PASS: TestTriggerSessionSyncNoClientIsANoOp (0.00s)
=== RUN   TestTriggerSessionSyncPostsAllGlobsToPeerHTTPPort
2026/10/08 12:17:47 connected to local-representative at 127.0.0.1:35413 as "sessions"
--- PASS: TestTriggerSessionSyncPostsAllGlobsToPeerHTTPPort (0.01s)
=== RUN   TestTriggerSessionSyncNoHTTPPortIsANoOp
2026/10/08 12:17:47 connected to local-representative at 127.0.0.1:42121 as "sessions"
--- PASS: TestTriggerSessionSyncNoHTTPPortIsANoOp (0.01s)
=== RUN   TestRequestSessionPullBuildsExpectedURL
--- PASS: TestRequestSessionPullBuildsExpectedURL (0.00s)
=== RUN   TestRequestSessionPullReportsIncompleteOnUpstreamErrors
--- PASS: TestRequestSessionPullReportsIncompleteOnUpstreamErrors (0.00s)
=== RUN   TestTriggerSessionsDiscoveryNoClientIsANoOp
--- PASS: TestTriggerSessionsDiscoveryNoClientIsANoOp (0.00s)
=== RUN   TestTriggerSessionsDiscoveryPostsToPeerHTTPPort
2026/10/08 12:17:47 connected to local-representative at 127.0.0.1:42911 as "sessions"
--- PASS: TestTriggerSessionsDiscoveryPostsToPeerHTTPPort (0.01s)
=== RUN   TestListSessionsWithRemoteSkipsAlreadyKnownIDs
2026/10/08 12:17:47 connected to local-representative at 127.0.0.1:36327 as "sessions"
--- PASS: TestListSessionsWithRemoteSkipsAlreadyKnownIDs (0.01s)
=== RUN   TestHandleSetSessionPullsRemoteOnlySession
2026/10/08 12:17:47 connected to local-representative at 127.0.0.1:37215 as "sessions"
--- PASS: TestHandleSetSessionPullsRemoteOnlySession (0.01s)
PASS
ok  	session-manager	0.361s
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/session-manager'

=== Testing the-conversationalist ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/the-conversationalist'
go test -v ./...
=== RUN   TestPushConvoStateNoClient
--- PASS: TestPushConvoStateNoClient (0.00s)
=== RUN   TestHandleReprCommandToleratesJunk
2026/10/08 12:17:48 repr: ignoring unrecognised command "__convo:"
2026/10/08 12:17:48 repr: ignoring unrecognised command "__convo:bogus arg"
--- PASS: TestHandleReprCommandToleratesJunk (0.00s)
=== RUN   TestConvoStateMsgShape
--- PASS: TestConvoStateMsgShape (0.00s)
=== RUN   TestStartConnectLoopThenStop
--- PASS: TestStartConnectLoopThenStop (0.05s)
=== RUN   TestStopConnectLoopNoopWhenIdle
--- PASS: TestStopConnectLoopNoopWhenIdle (0.00s)
=== RUN   TestSetAutoConnectArmsRetryLoopAndFlag
--- PASS: TestSetAutoConnectArmsRetryLoopAndFlag (0.00s)
=== RUN   TestConnectLoopResumesAfterUnintentionalDisconnect
2026/10/08 12:17:48 connected to local-representative at 127.0.0.1:46731 as "convo"
2026/10/08 12:17:48 disconnected from local-representative at 127.0.0.1:46731 — auto-connect resuming the retry cycle
2026/10/08 12:17:48 connected to local-representative at 127.0.0.1:46731 as "convo"
--- PASS: TestConnectLoopResumesAfterUnintentionalDisconnect (0.02s)
=== RUN   TestConnectLoopDoesNotResumeWithoutAutoConnect
2026/10/08 12:17:48 connected to local-representative at 127.0.0.1:38871 as "convo"
2026/10/08 12:17:48 disconnected from local-representative at 127.0.0.1:38871
--- PASS: TestConnectLoopDoesNotResumeWithoutAutoConnect (0.22s)
=== RUN   TestUploadToFilesSendsMultipartFile
--- PASS: TestUploadToFilesSendsMultipartFile (0.00s)
=== RUN   TestUploadToFilesUnexpectedStatus
--- PASS: TestUploadToFilesUnexpectedStatus (0.00s)
=== RUN   TestSaveTranscriptNoSession
--- PASS: TestSaveTranscriptNoSession (0.00s)
=== RUN   TestSaveTranscriptNoReprConnection
--- PASS: TestSaveTranscriptNoReprConnection (0.00s)
=== RUN   TestStartTranscriptionNoRegion
2026/10/08 12:17:49 transcribe: no AWS region configured: set AWS_REGION (or AWS_DEFAULT_REGION) in the environment, add a region to the shared AWS config, or start with --aws-region
--- PASS: TestStartTranscriptionNoRegion (0.00s)
=== RUN   TestStopTranscriptionNoSession
--- PASS: TestStopTranscriptionNoSession (0.00s)
PASS
ok  	the-conversationalist	0.308s
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/the-conversationalist'

=== Testing ianar ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/ianar'
CGO_ENABLED=1 go test -v ./...
=== RUN   TestArtifactStoreDropsOldest
--- PASS: TestArtifactStoreDropsOldest (0.00s)
=== RUN   TestDecodeDataURL
--- PASS: TestDecodeDataURL (0.00s)
=== RUN   TestCaptureArtifactIsTheImageAsIs
--- PASS: TestCaptureArtifactIsTheImageAsIs (0.00s)
=== RUN   TestClipArtifact
--- PASS: TestClipArtifact (0.00s)
=== RUN   TestInspectArtifact
--- PASS: TestInspectArtifact (0.00s)
=== RUN   TestSequenceArtifactReportsAFailedRun
--- PASS: TestSequenceArtifactReportsAFailedRun (0.00s)
=== RUN   TestHandleRunSequenceKeepsTheRunForSaving
2026/10/08 12:17:52 robot: running sequence "fc-hello-world" (keyboard via compositor (org.gnome.Mutter.RemoteDesktop))
2026/10/08 12:17:52 robot: sequence "fc-hello-world" failed at step 5 (Press enter to submit the command): instruction 4 (wait-for-text): after 3s, 0 line(s) on screen show "hello world!" -- needed more than 0
--- PASS: TestHandleRunSequenceKeepsTheRunForSaving (0.08s)
=== RUN   TestHandleSaveArtifactNeedsLocalRepresentative
--- PASS: TestHandleSaveArtifactNeedsLocalRepresentative (0.00s)
=== RUN   TestUploadToFilesSendsMultipartFile
--- PASS: TestUploadToFilesSendsMultipartFile (0.00s)
=== RUN   TestBoxInspectionScalesToTheImage
--- PASS: TestBoxInspectionScalesToTheImage (0.00s)
=== RUN   TestRecordNativeClipPrefersCompositor
2026/10/08 12:17:52 robot: recording a 4s native clip
--- PASS: TestRecordNativeClipPrefersCompositor (0.00s)
=== RUN   TestRecordNativeClipFallsBackToFrames
2026/10/08 12:17:52 robot: recording a 4s native clip
2026/10/08 12:17:52 robot: compositor screen recording unavailable; sampling frames for the clip instead
--- PASS: TestRecordNativeClipFallsBackToFrames (0.00s)
=== RUN   TestRecordNativeClipKeepsFramesOnMidClipFailure
2026/10/08 12:17:52 robot: recording a 4s native clip
2026/10/08 12:17:52 robot: compositor screen recording unavailable; sampling frames for the clip instead
2026/10/08 12:17:52 robot: frame grab failed 300ms into the clip, ending it early: every capture path failed: robotgo: boom
--- PASS: TestRecordNativeClipKeepsFramesOnMidClipFailure (0.00s)
=== RUN   TestRecordNativeClipReportsEveryFailure
2026/10/08 12:17:52 robot: recording a 4s native clip
2026/10/08 12:17:52 robot: recorder boom; sampling frames for the clip instead
--- PASS: TestRecordNativeClipReportsEveryFailure (0.00s)
=== RUN   TestRecordNativeClipRejectsConcurrentClip
--- PASS: TestRecordNativeClipRejectsConcurrentClip (0.00s)
=== RUN   TestHandleClipNativeReportsResult
2026/10/08 12:17:52 robot: recording a 4s native clip
--- PASS: TestHandleClipNativeReportsResult (0.00s)
=== RUN   TestDownscale
--- PASS: TestDownscale (0.07s)
=== RUN   TestMimeTypeForVideo
--- PASS: TestMimeTypeForVideo (0.00s)
=== RUN   TestRunSequenceCancelledStopsBeforeNextStep
2026/10/08 12:17:52 robot: running sequence "test" (keyboard via compositor (org.gnome.Mutter.RemoteDesktop))
2026/10/08 12:17:52 robot: sequence "test" failed at step 2 (Type b): cancelled
--- PASS: TestRunSequenceCancelledStopsBeforeNextStep (0.00s)
=== RUN   TestActionRunnerStopsBetweenInstructions
--- PASS: TestActionRunnerStopsBetweenInstructions (0.00s)
=== RUN   TestPollScreenEndsOnCancel
--- PASS: TestPollScreenEndsOnCancel (0.00s)
=== RUN   TestCancelLRRun
2026/10/08 12:17:52 robot: local-representative cancelled run r1
--- PASS: TestCancelLRRun (0.00s)
=== RUN   TestCaptureForLRSavesScreenshot
2026/10/08 12:17:52 robot: capturing native display, robotgo reports screen size 4x4
2026/10/08 12:17:52 robot: saved a native capture for local-representative (c1) as ianar-capture-native-node-a-2026-10-08T12-17-52.png
--- PASS: TestCaptureForLRSavesScreenshot (0.00s)
=== RUN   TestCaptureForLRWithoutLR
2026/10/08 12:17:52 robot: capturing native display, robotgo reports screen size 4x4
--- PASS: TestCaptureForLRWithoutLR (0.00s)
=== RUN   TestCaptureForLRCaptureFails
2026/10/08 12:17:52 robot: capturing native display, robotgo reports screen size 4x4
--- PASS: TestCaptureForLRCaptureFails (0.00s)
=== RUN   TestLRRecordingStartStop
2026/10/08 12:17:52 robot: recording the screen for local-representative (r1)
--- PASS: TestLRRecordingStartStop (0.00s)
=== RUN   TestRecordingArtifactName
--- PASS: TestRecordingArtifactName (0.00s)
=== RUN   TestRobotRunWithoutRecording
2026/10/08 12:17:52 robot: running sequence "lr-abc" (keyboard via compositor (org.gnome.Mutter.RemoteDesktop))
--- PASS: TestRobotRunWithoutRecording (0.00s)
=== RUN   TestCompileRobotRun
--- PASS: TestCompileRobotRun (0.00s)
=== RUN   TestCompileRobotRunRejectsBadSteps
--- PASS: TestCompileRobotRunRejectsBadSteps (0.00s)
=== RUN   TestRobotOpsPayload
--- PASS: TestRobotOpsPayload (0.00s)
=== RUN   TestSendRobotOpsNoClient
--- PASS: TestSendRobotOpsNoClient (0.00s)
=== RUN   TestPushRobotStateNoClient
--- PASS: TestPushRobotStateNoClient (0.00s)
=== RUN   TestHandleReprCommandToleratesJunk
2026/10/08 12:17:52 repr: ignoring unrecognised command "__robot:"
2026/10/08 12:17:52 repr: ignoring unrecognised command "__robot:bogus arg"
--- PASS: TestHandleReprCommandToleratesJunk (0.00s)
=== RUN   TestRobotStateMsgShape
--- PASS: TestRobotStateMsgShape (0.00s)
=== RUN   TestStartConnectLoopThenStop
--- PASS: TestStartConnectLoopThenStop (0.05s)
=== RUN   TestStopConnectLoopNoopWhenIdle
--- PASS: TestStopConnectLoopNoopWhenIdle (0.00s)
=== RUN   TestSetAutoConnectArmsRetryLoopAndFlag
--- PASS: TestSetAutoConnectArmsRetryLoopAndFlag (0.00s)
=== RUN   TestConnectLoopResumesAfterUnintentionalDisconnect
2026/10/08 12:17:52 connected to local-representative at 127.0.0.1:34915 as "robot"
2026/10/08 12:17:52 disconnected from local-representative at 127.0.0.1:34915 — auto-connect resuming the retry cycle
--- PASS: TestConnectLoopResumesAfterUnintentionalDisconnect (0.01s)
=== RUN   TestConnectLoopDoesNotResumeWithoutAutoConnect
2026/10/08 12:17:52 connected to local-representative at 127.0.0.1:35877 as "robot"
2026/10/08 12:17:52 disconnected from local-representative at 127.0.0.1:35877
--- PASS: TestConnectLoopDoesNotResumeWithoutAutoConnect (0.21s)
=== RUN   TestCaptureNativeDisplayEncodesCapturedImage
2026/10/08 12:17:52 robot: capturing native display, robotgo reports screen size 4x4
--- PASS: TestCaptureNativeDisplayEncodesCapturedImage (0.00s)
=== RUN   TestCaptureNativeDisplayFallsBackToPortal
2026/10/08 12:17:52 robot: capturing native display, robotgo reports screen size 4x4
--- PASS: TestCaptureNativeDisplayFallsBackToPortal (0.00s)
=== RUN   TestCaptureNativeDisplaySkipsBlankFrames
2026/10/08 12:17:52 robot: capturing native display, robotgo reports screen size 4x4
--- PASS: TestCaptureNativeDisplaySkipsBlankFrames (0.00s)
=== RUN   TestCaptureNativeDisplayReturnsBlankFrameWhenNothingHasContent
2026/10/08 12:17:52 robot: capturing native display, robotgo reports screen size 4x4
2026/10/08 12:17:52 robot: WARNING -- no capture path returned screen content; returning the black frame from robotgo (robotgo: every pixel is black; D-Bus compositor screenshot: no portal)
--- PASS: TestCaptureNativeDisplayReturnsBlankFrameWhenNothingHasContent (0.00s)
=== RUN   TestCaptureNativeDisplayReportsEveryFailure
2026/10/08 12:17:52 robot: capturing native display, robotgo reports screen size 4x4
--- PASS: TestCaptureNativeDisplayReportsEveryFailure (0.00s)
=== RUN   TestWarmUpRobotDisplayInstallsErrorHandler
2026/10/08 12:17:52 robot: robotgo display ready, reported screen size 4x4
--- PASS: TestWarmUpRobotDisplayInstallsErrorHandler (0.00s)
=== RUN   TestCirclePointsShape
--- PASS: TestCirclePointsShape (0.00s)
=== RUN   TestCirclePointsClockwise
--- PASS: TestCirclePointsClockwise (0.00s)
=== RUN   TestCircleMousePropagatesLocationError
2026/10/08 12:17:52 robot: compositor pointer input unavailable; driving the pointer with robotgo instead
--- PASS: TestCircleMousePropagatesLocationError (0.00s)
=== RUN   TestCircleMousePrefersCompositor
--- PASS: TestCircleMousePrefersCompositor (0.00s)
=== RUN   TestCircleMouseDoesNotFallBackMidDrive
--- PASS: TestCircleMouseDoesNotFallBackMidDrive (0.00s)
=== RUN   TestDriveCircleRelative
--- PASS: TestDriveCircleRelative (0.00s)
=== RUN   TestHandleCaptureBrowserEmptyData
--- PASS: TestHandleCaptureBrowserEmptyData (0.00s)
=== RUN   TestHandleCaptureBrowserPassesThroughImage
--- PASS: TestHandleCaptureBrowserPassesThroughImage (0.00s)
=== RUN   TestHandleCaptureNativeReportsError
2026/10/08 12:17:52 robot: capturing native display, robotgo reports screen size 4x4
--- PASS: TestHandleCaptureNativeReportsError (0.00s)
=== RUN   TestHandleCircleMouseReportsError
2026/10/08 12:17:52 robot: compositor pointer input unavailable; driving the pointer with robotgo instead
--- PASS: TestHandleCircleMouseReportsError (0.00s)
=== RUN   TestDriveCircle
--- PASS: TestDriveCircle (0.00s)
=== RUN   TestDriveCirclePropagatesMoveError
--- PASS: TestDriveCirclePropagatesMoveError (0.00s)
=== RUN   TestEnsureScreenAwake
=== RUN   TestEnsureScreenAwake/awake
=== RUN   TestEnsureScreenAwake/locked
=== RUN   TestEnsureScreenAwake/blanked_wakes
2026/10/08 12:17:52 robot: the screen has blanked; waking it
=== RUN   TestEnsureScreenAwake/blanked_then_locked
2026/10/08 12:17:52 robot: the screen has blanked; waking it
=== RUN   TestEnsureScreenAwake/blanked_never_wakes
2026/10/08 12:17:52 robot: the screen has blanked; waking it
2026/10/08 12:17:52 robot: asking the screen saver to wake: refused
=== RUN   TestEnsureScreenAwake/state_unavailable
2026/10/08 12:17:52 robot: can't tell whether the screen is locked (screen state unavailable: test); assuming not
2026/10/08 12:17:52 robot: can't tell whether the screen is blanked (screen state unavailable: test); assuming not
--- PASS: TestEnsureScreenAwake (0.00s)
    --- PASS: TestEnsureScreenAwake/awake (0.00s)
    --- PASS: TestEnsureScreenAwake/locked (0.00s)
    --- PASS: TestEnsureScreenAwake/blanked_wakes (0.00s)
    --- PASS: TestEnsureScreenAwake/blanked_then_locked (0.00s)
    --- PASS: TestEnsureScreenAwake/blanked_never_wakes (0.00s)
    --- PASS: TestEnsureScreenAwake/state_unavailable (0.00s)
=== RUN   TestUnlockScreen
=== RUN   TestUnlockScreen/not_locked
=== RUN   TestUnlockScreen/not_locked_but_blanked_wakes
2026/10/08 12:17:52 robot: the screen has blanked; waking it
=== RUN   TestUnlockScreen/locked_unlocks
2026/10/08 12:17:52 robot: the screen is locked; asking logind to unlock it
=== RUN   TestUnlockScreen/unlock_refused
2026/10/08 12:17:52 robot: the screen is locked; asking logind to unlock it
=== RUN   TestUnlockScreen/stays_locked
2026/10/08 12:17:52 robot: the screen is locked; asking logind to unlock it
--- PASS: TestUnlockScreen (0.00s)
    --- PASS: TestUnlockScreen/not_locked (0.00s)
    --- PASS: TestUnlockScreen/not_locked_but_blanked_wakes (0.00s)
    --- PASS: TestUnlockScreen/locked_unlocks (0.00s)
    --- PASS: TestUnlockScreen/unlock_refused (0.00s)
    --- PASS: TestUnlockScreen/stays_locked (0.00s)
=== RUN   TestRunSequenceRunsEachStep
2026/10/08 12:17:52 robot: running sequence "test" (keyboard via compositor (org.gnome.Mutter.RemoteDesktop))
--- PASS: TestRunSequenceRunsEachStep (0.00s)
=== RUN   TestRunSequenceStopsAtTheFirstFailure
2026/10/08 12:17:52 robot: running sequence "test" (keyboard via compositor (org.gnome.Mutter.RemoteDesktop))
2026/10/08 12:17:52 robot: sequence "test" failed at step 2 (Type b): key boom
--- PASS: TestRunSequenceStopsAtTheFirstFailure (0.00s)
=== RUN   TestRunSequenceRejectsConcurrentRuns
--- PASS: TestRunSequenceRejectsConcurrentRuns (0.00s)
=== RUN   TestStartNativeRecordingPrefersCompositor
--- PASS: TestStartNativeRecordingPrefersCompositor (0.00s)
=== RUN   TestOpenKeyboardFallsBackToRobotgo
2026/10/08 12:17:52 robot: compositor pointer input unavailable; driving the keyboard with robotgo instead
--- PASS: TestOpenKeyboardFallsBackToRobotgo (0.00s)
=== RUN   TestKeysymKeyboard
--- PASS: TestKeysymKeyboard (0.00s)
=== RUN   TestKeysymKeyboardReleasesModifiersOnFailure
--- PASS: TestKeysymKeyboardReleasesModifiersOnFailure (0.00s)
=== RUN   TestFederationCommandTarget
--- PASS: TestFederationCommandTarget (0.00s)
=== RUN   TestFederationCommandTargetNotRunning
--- PASS: TestFederationCommandTargetNotRunning (0.00s)
=== RUN   TestFocusWindowTriesEachPath
2026/10/08 12:17:52 robot: focusing "" via a: window focus path unavailable
2026/10/08 12:17:52 robot: focusing "" via a: a boom
2026/10/08 12:17:52 robot: focusing "" via b: b boom
--- PASS: TestFocusWindowTriesEachPath (0.00s)
=== RUN   TestExampleLibraryIsValid
--- PASS: TestExampleLibraryIsValid (0.00s)
=== RUN   TestSeqLibYAMLRoundTrip
--- PASS: TestSeqLibYAMLRoundTrip (0.00s)
=== RUN   TestDecodeHandWrittenYAML
--- PASS: TestDecodeHandWrittenYAML (0.00s)
=== RUN   TestDecodeSeqLibErrors
--- PASS: TestDecodeSeqLibErrors (0.00s)
=== RUN   TestYAMLScalarQuoting
--- PASS: TestYAMLScalarQuoting (0.00s)
=== RUN   TestExpand
--- PASS: TestExpand (0.00s)
=== RUN   TestParseChords
--- PASS: TestParseChords (0.00s)
=== RUN   TestActionValidation
--- PASS: TestActionValidation (0.00s)
=== RUN   TestSeqLibraryEdits
2026/10/08 12:17:52 sequence-v2: no library at /tmp/TestSeqLibraryEdits3826211519/001/ianar/lib.yaml yet; starting from the examples
--- PASS: TestSeqLibraryEdits (0.01s)
=== RUN   TestOpenSeqLibrarySetsABrokenFileAside
2026/10/08 12:17:52 sequence-v2: couldn't load /tmp/TestOpenSeqLibrarySetsABrokenFileAside3831557309/001/lib.yaml (line 2: expected "," or "]" in [...]); moved it to /tmp/TestOpenSeqLibrarySetsABrokenFileAside3831557309/001/lib.yaml.broken-2026-10-08T12-17-52 and started from the examples
--- PASS: TestOpenSeqLibrarySetsABrokenFileAside (0.00s)
=== RUN   TestOpenSeqLibraryUpgradesUneditedExamples
2026/10/08 12:17:52 sequence-v2: updated to this IANAR's version of the built-in examples (you hadn't edited them): action open-url
--- PASS: TestOpenSeqLibraryUpgradesUneditedExamples (0.01s)
=== RUN   TestOpenSeqLibraryKeepsUnrecordedExamples
--- PASS: TestOpenSeqLibraryKeepsUnrecordedExamples (0.01s)
=== RUN   TestOpenSeqLibraryRetiresUneditedExamples
2026/10/08 12:17:52 sequence-v2: removed built-in examples this IANAR no longer ships (you hadn't edited them): sequence desktop-text-file, action open-file
--- PASS: TestOpenSeqLibraryRetiresUneditedExamples (0.01s)
=== RUN   TestOpenSeqLibraryKeepsEditedRetiredExamples
--- PASS: TestOpenSeqLibraryKeepsEditedRetiredExamples (0.01s)
=== RUN   TestRunFCHelloWorldV2
2026/10/08 12:17:52 robot: running sequence "fc-hello-world" (keyboard via compositor (org.gnome.Mutter.RemoteDesktop))
--- PASS: TestRunFCHelloWorldV2 (0.08s)
=== RUN   TestRunFCHelloWorldV2FailsWhenNoOutputAppears
2026/10/08 12:17:52 robot: running sequence "fc-hello-world" (keyboard via compositor (org.gnome.Mutter.RemoteDesktop))
2026/10/08 12:17:52 robot: sequence "fc-hello-world" failed at step 5 (Press enter to submit the command): instruction 4 (wait-for-text): after 3s, 1 line(s) on screen show "hello world!" -- needed more than 1
--- PASS: TestRunFCHelloWorldV2FailsWhenNoOutputAppears (0.08s)
=== RUN   TestLocateIcon
--- PASS: TestLocateIcon (2.76s)
=== RUN   TestRunFirefoxWeather
2026/10/08 12:17:55 robot: running sequence "firefox-weather" (keyboard via compositor (org.gnome.Mutter.RemoteDesktop))
--- PASS: TestRunFirefoxWeather (0.57s)
=== RUN   TestRunFileActions
2026/10/08 12:17:56 robot: running sequence "files" (keyboard via compositor (org.gnome.Mutter.RemoteDesktop))
2026/10/08 12:17:56 robot: sequence "files" failed at step 5 (Check a file's contents): open /tmp/TestRunFileActions1893152123/001/notes.txt: no such file or directory
--- PASS: TestRunFileActions (0.00s)
=== RUN   TestCompileFillsLabelsAndDetail
--- PASS: TestCompileFillsLabelsAndDetail (0.00s)
=== RUN   TestParseTesseractTSV
--- PASS: TestParseTesseractTSV (0.00s)
=== RUN   TestMergeWordsKeepsTheMoreConfidentReading
--- PASS: TestMergeWordsKeepsTheMoreConfidentReading (0.00s)
=== RUN   TestGroupLines
--- PASS: TestGroupLines (0.00s)
=== RUN   TestFindLinesMatchesWholeLinesOnly
--- PASS: TestFindLinesMatchesWholeLinesOnly (0.00s)
=== RUN   TestFindLinesBridgesADroppedDash
--- PASS: TestFindLinesBridgesADroppedDash (0.00s)
=== RUN   TestWrappedLinesFindsAWrappedLabel
--- PASS: TestWrappedLinesFindsAWrappedLabel (0.00s)
=== RUN   TestReadScreenTextReadsBothWaysAndScales
--- PASS: TestReadScreenTextReadsBothWaysAndScales (0.11s)
=== RUN   TestReadScreenTextScalesWideCaptures
--- PASS: TestReadScreenTextScalesWideCaptures (0.27s)
=== RUN   TestSameTextAllowsOCRSlips
--- PASS: TestSameTextAllowsOCRSlips (0.00s)
=== RUN   TestReadScreenTextReportsMissingOCR
--- PASS: TestReadScreenTextReportsMissingOCR (0.00s)
=== RUN   TestPrepareForOCR
--- PASS: TestPrepareForOCR (0.00s)
=== RUN   TestFocusViaVisionClicksTheTitle
--- PASS: TestFocusViaVisionClicksTheTitle (0.02s)
=== RUN   TestFocusViaVisionReportsNearMisses
--- PASS: TestFocusViaVisionReportsNearMisses (0.03s)
=== RUN   TestClickAtFallsBackToRobotgo
2026/10/08 12:17:56 robot: compositor pointer input unavailable; clicking with robotgo instead
--- PASS: TestClickAtFallsBackToRobotgo (0.00s)
=== RUN   TestClickAtAcrossMonitors
--- PASS: TestClickAtAcrossMonitors (0.00s)
=== RUN   TestPointerScale
--- PASS: TestPointerScale (0.00s)
=== RUN   TestCropAroundStaysInside
--- PASS: TestCropAroundStaysInside (0.00s)
PASS
ok  	ianar	4.448s
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/ianar'

=== Testing local-representative ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/local-representative'
go test -v ./...
=== RUN   TestRandomTokenAvoidsLookalikes
--- PASS: TestRandomTokenAvoidsLookalikes (0.00s)
=== RUN   TestControlStateListsSequences
--- PASS: TestControlStateListsSequences (0.00s)
=== RUN   TestControlStartUnknownSequence
--- PASS: TestControlStartUnknownSequence (0.00s)
=== RUN   TestWaitForOutputSeesOnlyNewLinesFromThatInstance
--- PASS: TestWaitForOutputSeesOnlyNewLinesFromThatInstance (0.40s)
=== RUN   TestFCExpectOutputFailText
--- PASS: TestFCExpectOutputFailText (0.00s)
=== RUN   TestFCExpectOutputSinceRun
--- PASS: TestFCExpectOutputSinceRun (0.20s)
=== RUN   TestRobotRunBudget
--- PASS: TestRobotRunBudget (0.00s)
=== RUN   TestWaitForCancelled
--- PASS: TestWaitForCancelled (0.00s)
=== RUN   TestRobotRunNeedsRobot
--- PASS: TestRobotRunNeedsRobot (0.00s)
=== RUN   TestHandoffSequenceRecordsTheRobotsScreen
--- PASS: TestHandoffSequenceRecordsTheRobotsScreen (0.00s)
=== RUN   TestHandoffSequenceFetchesAndDeletesFoundIt
--- PASS: TestHandoffSequenceFetchesAndDeletesFoundIt (0.00s)
=== RUN   TestRecordingWithoutRobotIsNotFatal
--- PASS: TestRecordingWithoutRobotIsNotFatal (0.00s)
=== RUN   TestHandoffSequenceUnlocksFirst
--- PASS: TestHandoffSequenceUnlocksFirst (0.00s)
=== RUN   TestIsPlayableVideo
--- PASS: TestIsPlayableVideo (0.00s)
=== RUN   TestRecordingsInState
--- PASS: TestRecordingsInState (0.00s)
=== RUN   TestNoteRobotRecordDelivers
--- PASS: TestNoteRobotRecordDelivers (0.00s)
=== RUN   TestSetValueReplaces
--- PASS: TestSetValueReplaces (0.01s)
=== RUN   TestMarkerPrintedBy
--- PASS: TestMarkerPrintedBy (0.00s)
=== RUN   TestNoteRobotRunDeliversResult
--- PASS: TestNoteRobotRunDeliversResult (0.00s)
=== RUN   TestAskUserWaitsForContinue
--- PASS: TestAskUserWaitsForContinue (0.01s)
=== RUN   TestAskUserCancelled
--- PASS: TestAskUserCancelled (0.05s)
=== RUN   TestFCCaptureEcho
--- PASS: TestFCCaptureEcho (0.20s)
=== RUN   TestFCCaptureEchoFailures
--- PASS: TestFCCaptureEchoFailures (0.20s)
=== RUN   TestEchoArgument
--- PASS: TestEchoArgument (0.00s)
=== RUN   TestOutputOp
--- PASS: TestOutputOp (0.00s)
=== RUN   TestExampleLibraryIsValid
--- PASS: TestExampleLibraryIsValid (0.00s)
=== RUN   TestControlLibYAMLRoundTrip
--- PASS: TestControlLibYAMLRoundTrip (0.00s)
=== RUN   TestDecodeControlLibByHand
--- PASS: TestDecodeControlLibByHand (0.00s)
=== RUN   TestDecodeControlLibRejects
--- PASS: TestDecodeControlLibRejects (0.00s)
=== RUN   TestValidateControlInstruction
--- PASS: TestValidateControlInstruction (0.00s)
=== RUN   TestSequenceValidation
--- PASS: TestSequenceValidation (0.00s)
=== RUN   TestCompileControlSequence
--- PASS: TestCompileControlSequence (0.00s)
=== RUN   TestActionRunnerNativeOps
--- PASS: TestActionRunnerNativeOps (0.00s)
=== RUN   TestActionRunnerStopsAtFirstFailure
--- PASS: TestActionRunnerStopsAtFirstFailure (0.20s)
=== RUN   TestActionRunnerNeedsTheRobotForRobotOps
--- PASS: TestActionRunnerNeedsTheRobotForRobotOps (0.00s)
=== RUN   TestLibrarySaveRenameDelete
--- PASS: TestLibrarySaveRenameDelete (0.00s)
=== RUN   TestExportImport
    controllib_test.go:269: imported 8 actions, 1 sequences (added action unlock-screen, action launch-fc, action echo-marker, action robot-take-local, action robot-type-command, action robot-give-back, action fetch-node-file, action fc-remove-file, sequence fc-robot-handoff)
--- FAIL: TestExportImport (0.00s)
=== RUN   TestLibraryPersists
2026/10/08 12:18:00 control: no library at /tmp/TestLibraryPersists1001147022/001/control-v1.yaml yet; starting from the examples
2026/10/08 12:18:00 control: couldn't load /tmp/TestLibraryPersists1001147022/001/control-v1.yaml (line 1: expected "," or "]" in [...]); moved it to /tmp/TestLibraryPersists1001147022/001/control-v1.yaml.broken-2026-10-08T12-18-00 and started from the examples
--- PASS: TestLibraryPersists (0.01s)
=== RUN   TestUpgradeControlExamples
--- PASS: TestUpgradeControlExamples (0.00s)
=== RUN   TestUpgradeAddsNewExamples
--- PASS: TestUpgradeAddsNewExamples (0.01s)
=== RUN   TestNodeControlType
--- PASS: TestNodeControlType (0.00s)
=== RUN   TestHandleLibRequest
--- PASS: TestHandleLibRequest (0.01s)
=== RUN   TestRobotOpsDescribeSteps
--- PASS: TestRobotOpsDescribeSteps (0.00s)
=== RUN   TestNodesFromAgentCoordinator
--- PASS: TestNodesFromAgentCoordinator (0.00s)
=== RUN   TestCheckNodeControls
--- PASS: TestCheckNodeControls (0.00s)
=== RUN   TestCaptureTwoNodesNeedsConnectedNodes
--- PASS: TestCaptureTwoNodesNeedsConnectedNodes (0.00s)
=== RUN   TestCaptureTwoNodesExample
--- PASS: TestCaptureTwoNodesExample (0.00s)
=== RUN   TestNodeCaptureNeedsAgentCoordinator
--- PASS: TestNodeCaptureNeedsAgentCoordinator (0.00s)
=== RUN   TestNodeCaptureResultsDelivered
--- PASS: TestNodeCaptureResultsDelivered (0.00s)
=== RUN   TestNodeWaitConnectedFresh
--- PASS: TestNodeWaitConnectedFresh (0.00s)
=== RUN   TestFetchAllowed
--- PASS: TestFetchAllowed (0.00s)
=== RUN   TestFetchHere
2026/10/08 12:18:00 files: uploaded test-lr_tmp_TestFetchHere109482067_002_a.log (4 bytes) -> host-cache/e7456e11_test-lr_tmp_TestFetchHere109482067_002_a.log
2026/10/08 12:18:00 files: uploaded test-lr_tmp_TestFetchHere109482067_002_b.log (4 bytes) -> host-cache/fe7dfaa1_test-lr_tmp_TestFetchHere109482067_002_b.log
2026/10/08 12:18:00 files: uploaded test-lr_tmp_TestFetchHere109482067_002_link.log (2 bytes) -> host-cache/21eb219e_test-lr_tmp_TestFetchHere109482067_002_link.log
2026/10/08 12:18:00 files: uploaded test-lr_tmp_TestFetchHere109482067_002_a.log (4 bytes) -> host-cache/6578b5d8_test-lr_tmp_TestFetchHere109482067_002_a.log
2026/10/08 12:18:00 files: uploaded test-lr_tmp_TestFetchHere109482067_002_b.log (4 bytes) -> host-cache/5096f7ab_test-lr_tmp_TestFetchHere109482067_002_b.log
2026/10/08 12:18:00 files: uploaded test-lr_tmp_TestFetchHere109482067_002_a.log (4 bytes) -> host-cache/e7553d0d_test-lr_tmp_TestFetchHere109482067_002_a.log
--- PASS: TestFetchHere (0.00s)
=== RUN   TestNodeFetchFileHere
2026/10/08 12:18:00 files: uploaded test-lr_tmp_TestNodeFetchFileHere3310632424_002_x.txt (1 bytes) -> host-cache/d2f64bf4_test-lr_tmp_TestNodeFetchFileHere3310632424_002_x.txt
--- PASS: TestNodeFetchFileHere (0.01s)
=== RUN   TestNodeFetchResultsDelivered
--- PASS: TestNodeFetchResultsDelivered (0.00s)
=== RUN   TestScreenshotsInState
--- PASS: TestScreenshotsInState (0.00s)
=== RUN   TestSaveRun
2026/10/08 12:18:00 files: uploaded shot.png (9 bytes) -> host-cache/4b2c0760_shot.png
2026/10/08 12:18:00 files: uploaded lr-control-seq-2026-10-08T12-18-00.zip (1007 bytes) -> host-cache/963ebb95_lr-control-seq-2026-10-08T12-18-00.zip
--- PASS: TestSaveRun (0.00s)
=== RUN   TestSaveRunWithoutRun
--- PASS: TestSaveRunWithoutRun (0.00s)
=== RUN   TestUniqueZipName
--- PASS: TestUniqueZipName (0.00s)
=== RUN   TestIsFCName
--- PASS: TestIsFCName (0.00s)
=== RUN   TestFCInstancesOrderAndLabels
--- PASS: TestFCInstancesOrderAndLabels (0.00s)
=== RUN   TestFCInstanceStateAndDisconnect
--- PASS: TestFCInstanceStateAndDisconnect (0.00s)
=== RUN   TestDefaultFCKeyPrefersRemoteControl
--- PASS: TestDefaultFCKeyPrefersRemoteControl (0.00s)
=== RUN   TestNextInstanceSkipsConnectedIDs
2026/10/08 12:18:00 procman: federation-command#1 is still connected from before; skipping that instance id
2026/10/08 12:18:00 procman: federation-command#2 is still connected from before; skipping that instance id
--- PASS: TestNextInstanceSkipsConnectedIDs (0.00s)
=== RUN   TestFCInstanceSessionAndRidealong
--- PASS: TestFCInstanceSessionAndRidealong (0.00s)
=== RUN   TestHandleFilePeersNoAC
--- PASS: TestHandleFilePeersNoAC (0.00s)
=== RUN   TestFilePeersFromListsOtherNodesThroughAC
--- PASS: TestFilePeersFromListsOtherNodesThroughAC (0.00s)
=== RUN   TestClassifyKind
--- PASS: TestClassifyKind (0.00s)
=== RUN   TestSanitizeFilename
--- PASS: TestSanitizeFilename (0.00s)
=== RUN   TestDisplayNameRoundTrip
--- PASS: TestDisplayNameRoundTrip (0.00s)
=== RUN   TestFileUploadAndList
2026/10/08 12:18:00 files: uploaded hello.txt (11 bytes) -> host-cache/82bf7703_hello.txt
--- PASS: TestFileUploadAndList (0.00s)
=== RUN   TestFileUploadRejectedWhenProxied
--- PASS: TestFileUploadRejectedWhenProxied (0.00s)
=== RUN   TestFileUploadAllowedWhenRelayedByAC
2026/10/08 12:18:00 files: uploaded hello.txt (5 bytes) -> host-cache/bffed032_hello.txt
--- PASS: TestFileUploadAllowedWhenRelayedByAC (0.00s)
=== RUN   TestFileUploadRejectedWithWrongRelayStamp
--- PASS: TestFileUploadRejectedWithWrongRelayStamp (0.00s)
=== RUN   TestSweepExpiredFiles
2026/10/08 12:18:00 files: removed expired host-cache file aaaaaaaa_old.txt (expired 1h0m0s ago)
--- PASS: TestSweepExpiredFiles (0.00s)
=== RUN   TestUploadWritesManifestHiddenFromListing
2026/10/08 12:18:00 files: uploaded hello.txt (11 bytes) -> host-cache/ba603463_hello.txt
--- PASS: TestUploadWritesManifestHiddenFromListing (0.00s)
=== RUN   TestUploadRejectsManifestPrefixedName
2026/10/08 12:18:00 files: upload of ".manifest_x.yaml" failed: upload rejected: filenames starting with ".manifest_" are reserved for host-cache manifests
--- PASS: TestUploadRejectsManifestPrefixedName (0.00s)
=== RUN   TestSweepExpiredFilesRemovesManifest
2026/10/08 12:18:00 files: removed expired host-cache file aaaaaaaa_old.txt (expired 1h0m0s ago)
--- PASS: TestSweepExpiredFilesRemovesManifest (0.00s)
=== RUN   TestHandleFileRaw
2026/10/08 12:18:00 files: uploaded hello.txt (11 bytes) -> host-cache/0457f713_hello.txt
--- PASS: TestHandleFileRaw (0.01s)
=== RUN   TestHandleFileHold
2026/10/08 12:18:00 files: uploaded hello.txt (5 bytes) -> host-cache/a968c503_hello.txt
2026/10/08 12:18:00 files: held a968c503_hello.txt (72h cache)
--- PASS: TestHandleFileHold (0.00s)
=== RUN   TestHandleFileHoldUnknownID
--- PASS: TestHandleFileHoldUnknownID (0.00s)
=== RUN   TestHandleFilePersist
2026/10/08 12:18:00 files: uploaded hello.txt (11 bytes) -> host-cache/012be8e7_hello.txt
2026/10/08 12:18:00 files: persisted 012be8e7_hello.txt -> host-store/012be8e7_hello.txt
--- PASS: TestHandleFilePersist (0.00s)
=== RUN   TestHandleFileDelete
2026/10/08 12:18:00 files: uploaded hello.txt (5 bytes) -> host-cache/cf26712c_hello.txt
2026/10/08 12:18:00 files: deleted cf26712c_hello.txt
--- PASS: TestHandleFileDelete (0.00s)
=== RUN   TestHandleFileDeletePersisted
2026/10/08 12:18:00 files: uploaded hello.txt (5 bytes) -> host-cache/84bc5270_hello.txt
2026/10/08 12:18:00 files: persisted 84bc5270_hello.txt -> host-store/84bc5270_hello.txt
2026/10/08 12:18:00 files: deleted 84bc5270_hello.txt
--- PASS: TestHandleFileDeletePersisted (0.00s)
=== RUN   TestManifestCreatorSurvivesHoldAndPersist
2026/10/08 12:18:00 files: uploaded hello.txt (5 bytes) -> host-cache/d9ebc6a6_hello.txt
2026/10/08 12:18:00 files: held d9ebc6a6_hello.txt (72h cache)
2026/10/08 12:18:00 files: persisted d9ebc6a6_hello.txt -> host-store/d9ebc6a6_hello.txt
--- PASS: TestManifestCreatorSurvivesHoldAndPersist (0.00s)
=== RUN   TestHandleFileItemUnknownAction
2026/10/08 12:18:00 files: uploaded hello.txt (5 bytes) -> host-cache/c8e9c0fa_hello.txt
--- PASS: TestHandleFileItemUnknownAction (0.00s)
=== RUN   TestHandleFileHighlight
2026/10/08 12:18:00 files: uploaded hello.txt (5 bytes) -> host-cache/dd16cf8b_hello.txt
2026/10/08 12:18:00 files: highlighted=true for dd16cf8b_hello.txt
2026/10/08 12:18:00 files: highlighted=false for dd16cf8b_hello.txt
--- PASS: TestHandleFileHighlight (0.00s)
=== RUN   TestHandleFileHighlightUnknownID
--- PASS: TestHandleFileHighlightUnknownID (0.00s)
=== RUN   TestHighlightSurvivesHoldAndPersist
2026/10/08 12:18:00 files: uploaded hello.txt (5 bytes) -> host-cache/cd119082_hello.txt
2026/10/08 12:18:00 files: highlighted=true for cd119082_hello.txt
2026/10/08 12:18:00 files: held cd119082_hello.txt (72h cache)
2026/10/08 12:18:00 files: persisted cd119082_hello.txt -> host-store/cd119082_hello.txt
--- PASS: TestHighlightSurvivesHoldAndPersist (0.00s)
=== RUN   TestSweepHeldFileSurvivesPastFileCacheTTL
--- PASS: TestSweepHeldFileSurvivesPastFileCacheTTL (0.00s)
=== RUN   TestSweepRemovesHeldFileAfterHoldExpiry
2026/10/08 12:18:00 files: removed expired host-cache file aaaaaaaa_held.txt (expired 1m1s ago)
--- PASS: TestSweepRemovesHeldFileAfterHoldExpiry (0.00s)
=== RUN   TestSweepNeverTouchesHostStore
--- PASS: TestSweepNeverTouchesHostStore (0.00s)
=== RUN   TestMarkupSaveMarksFileAndStaysHidden
2026/10/08 12:18:00 files: uploaded shot.png (14 bytes) -> host-cache/2c299968_shot.png
2026/10/08 12:18:00 files: saved in-progress markup for 2c299968_shot.png
--- PASS: TestMarkupSaveMarksFileAndStaysHidden (0.00s)
=== RUN   TestMarkupGetMissingIs404
2026/10/08 12:18:00 files: uploaded shot.png (14 bytes) -> host-cache/80ee95f7_shot.png
--- PASS: TestMarkupGetMissingIs404 (0.00s)
=== RUN   TestMarkupCommitWritesIntoOriginalAndClearsSidecar
2026/10/08 12:18:00 files: uploaded shot.png (14 bytes) -> host-cache/a2a3bdc4_shot.png
2026/10/08 12:18:00 files: saved in-progress markup for a2a3bdc4_shot.png
2026/10/08 12:18:00 files: committed markup into a2a3bdc4_shot.png (15 bytes)
--- PASS: TestMarkupCommitWritesIntoOriginalAndClearsSidecar (0.00s)
=== RUN   TestMarkupCopyCreatesNewFileAndLeavesOriginalUntouched
2026/10/08 12:18:00 files: uploaded shot.png (14 bytes) -> host-cache/3cb62234_shot.png
2026/10/08 12:18:00 files: saved in-progress markup for 3cb62234_shot.png
2026/10/08 12:18:00 files: copied markup of 3cb62234_shot.png into new host-cache file 336a6147_shot-markup.jpg
--- PASS: TestMarkupCopyCreatesNewFileAndLeavesOriginalUntouched (0.00s)
=== RUN   TestMarkupCancelDiscardsSidecarWithoutTouchingOriginal
2026/10/08 12:18:00 files: uploaded shot.png (14 bytes) -> host-cache/25b2f129_shot.png
2026/10/08 12:18:00 files: saved in-progress markup for 25b2f129_shot.png
2026/10/08 12:18:00 files: cancelled in-progress markup for 25b2f129_shot.png
--- PASS: TestMarkupCancelDiscardsSidecarWithoutTouchingOriginal (0.00s)
=== RUN   TestUploadRejectsMarkupPrefixedName
2026/10/08 12:18:00 files: upload of ".markup_whatever.jpg" failed: upload rejected: filenames starting with ".markup_" are reserved for in-progress markup sidecars
--- PASS: TestUploadRejectsMarkupPrefixedName (0.00s)
=== RUN   TestResolveConfigLayering
--- PASS: TestResolveConfigLayering (0.00s)
=== RUN   TestResolveConfigDevMode
--- PASS: TestResolveConfigDevMode (0.00s)
=== RUN   TestResolveConfigRejectsBadBool
--- PASS: TestResolveConfigRejectsBadBool (0.00s)
=== RUN   TestGetACStateReportsConnecting
--- PASS: TestGetACStateReportsConnecting (0.00s)
=== RUN   TestACStateMsgStampsTarget
--- PASS: TestACStateMsgStampsTarget (0.00s)
=== RUN   TestStopAutoConnectAC
--- PASS: TestStopAutoConnectAC (0.00s)
=== RUN   TestSetAutoConnectACTogglesEnabledFlag
2026/10/08 12:18:00 auto-connect enabled: dialing agent-coordinator at 127.0.0.1:1 every 10s for up to 10m0s (runs in background)
--- PASS: TestSetAutoConnectACTogglesEnabledFlag (0.00s)
=== RUN   TestDisconnectACTerminatesAutoConnect
2026/10/08 12:18:00 auto-connect enabled: dialing agent-coordinator at 127.0.0.1:1 every 10s for up to 10m0s (runs in background)
2026/10/08 12:18:00 auto-connect enabled: dialing agent-coordinator at 127.0.0.1:40471 every 10s for up to 10m0s (runs in background)
2026/10/08 12:18:00 auto-connect: attempting connection to agent-coordinator at 127.0.0.1:40471
2026/10/08 12:18:00 connecting to agent-coordinator at 127.0.0.1:40471 as "test-lr"
2026/10/08 12:18:00 tunnel: opened to agent-coordinator at 127.0.0.1:40471
2026/10/08 12:18:00 connected to agent-coordinator at 127.0.0.1:40471
2026/10/08 12:18:01 disconnected from agent-coordinator at 127.0.0.1:40471
--- PASS: TestDisconnectACTerminatesAutoConnect (0.21s)
=== RUN   TestSplitList
--- PASS: TestSplitList (0.00s)
=== RUN   TestParseAutoLaunchEntry
--- PASS: TestParseAutoLaunchEntry (0.00s)
=== RUN   TestWrapInTerminal
--- PASS: TestWrapInTerminal (0.00s)
=== RUN   TestTerminalCandidatesPreferVisible
--- PASS: TestTerminalCandidatesPreferVisible (0.00s)
=== RUN   TestTerminalLaunchEnvStripsActivationTokens
--- PASS: TestTerminalLaunchEnvStripsActivationTokens (0.00s)
=== RUN   TestFederationCommandBuildEnv
--- PASS: TestFederationCommandBuildEnv (0.00s)
=== RUN   TestFederationCommandDevModeCascades
--- PASS: TestFederationCommandDevModeCascades (0.00s)
=== RUN   TestFederationCommandNewInstanceAdoptsSMSession
--- PASS: TestFederationCommandNewInstanceAdoptsSMSession (0.00s)
=== RUN   TestFederationCommandExistingSessionWinsOverSM
--- PASS: TestFederationCommandExistingSessionWinsOverSM (0.00s)
=== RUN   TestCondoccerDevModeCascades
--- PASS: TestCondoccerDevModeCascades (0.00s)
=== RUN   TestCondoccerManagedSpec
--- PASS: TestCondoccerManagedSpec (0.00s)
=== RUN   TestTerminateDetachedDoesNotSignal
2026/10/08 12:18:01 system: stopping detached session for federation-command#1: [true]
--- PASS: TestTerminateDetachedDoesNotSignal (0.01s)
=== RUN   TestSystemStateSelf
--- PASS: TestSystemStateSelf (0.00s)
=== RUN   TestSystemStateDevModeCascadesToManaged
--- PASS: TestSystemStateDevModeCascadesToManaged (0.00s)
=== RUN   TestSystemStateSelfUpdateAvailable
--- PASS: TestSystemStateSelfUpdateAvailable (0.00s)
=== RUN   TestSystemStateSelfAutoUpdate
--- PASS: TestSystemStateSelfAutoUpdate (0.00s)
=== RUN   TestManagedVersionInSystemState
--- PASS: TestManagedVersionInSystemState (0.00s)
=== RUN   TestPollManagedVersionsDetectsDrift
--- PASS: TestPollManagedVersionsDetectsDrift (0.00s)
=== RUN   TestPollManagedVersionsStripsFCPrefix
--- PASS: TestPollManagedVersionsStripsFCPrefix (0.01s)
=== RUN   TestPollManagedVersionsSkipsUnreported
--- PASS: TestPollManagedVersionsSkipsUnreported (0.00s)
=== RUN   TestLaunchManagedUnknown
--- PASS: TestLaunchManagedUnknown (0.00s)
=== RUN   TestResolveAppBinaryOverride
--- PASS: TestResolveAppBinaryOverride (0.00s)
=== RUN   TestLaunchNInstancesAndTerminate
2026/10/08 12:18:01 system: launched test-sleep#1 (pid 3377730): /usr/bin/sleep [30]
2026/10/08 12:18:01 system: launched test-sleep#2 (pid 3377731): /usr/bin/sleep [30]
2026/10/08 12:18:01 system: terminating test-sleep#1 (pid 3377730)
2026/10/08 12:18:01 system: test-sleep#1 (pid 3377730) exited (exit -1)
2026/10/08 12:18:01 system: terminating test-sleep#2 (pid 3377731)
--- PASS: TestLaunchNInstancesAndTerminate (0.02s)
=== RUN   TestHandleSystemCommand
2026/10/08 12:18:01 system: test-sleep#2 (pid 3377731) exited (exit -1)
2026/10/08 12:18:01 system: launched test-syscmd#1 (pid 3377732): /usr/bin/sleep [30]
2026/10/08 12:18:01 system: terminating test-syscmd#1 (pid 3377732)
2026/10/08 12:18:01 system: test-syscmd#1 (pid 3377732) exited (exit -1)
2026/10/08 12:18:01 system: remote launch command missing application name
2026/10/08 12:18:01 system: ignoring unrecognised remote system command "__system:bogus x"
--- PASS: TestHandleSystemCommand (0.02s)
=== RUN   TestRestartManaged
2026/10/08 12:18:01 system: launched test-restart#1 (pid 3377733): /usr/bin/sleep [30]
2026/10/08 12:18:01 system: terminating test-restart#1 (pid 3377733)
2026/10/08 12:18:01 system: test-restart#1 (pid 3377733) exited (exit -1)
2026/10/08 12:18:01 system: launched test-restart#2 (pid 3377734): /usr/bin/sleep [30]
2026/10/08 12:18:01 system: terminating test-restart#2 (pid 3377734)
--- PASS: TestRestartManaged (0.02s)
=== RUN   TestHandleSystemCommandRestartManaged
2026/10/08 12:18:01 system: test-restart#2 (pid 3377734) exited (exit -1)
2026/10/08 12:18:01 system: launched test-syscmd-restart#1 (pid 3377735): /usr/bin/sleep [30]
2026/10/08 12:18:01 system: terminating test-syscmd-restart#1 (pid 3377735)
2026/10/08 12:18:01 system: test-syscmd-restart#1 (pid 3377735) exited (exit -1)
2026/10/08 12:18:01 system: launched test-syscmd-restart#2 (pid 3377736): /usr/bin/sleep [30]
2026/10/08 12:18:01 system: remote restart-managed command missing target
2026/10/08 12:18:01 system: terminating test-syscmd-restart#2 (pid 3377736)
--- PASS: TestHandleSystemCommandRestartManaged (0.02s)
=== RUN   TestLaunchManagedSingleton
2026/10/08 12:18:01 system: test-syscmd-restart#2 (pid 3377736) exited (exit -1)
2026/10/08 12:18:01 system: launched test-singleton#1 (pid 3377737): /usr/bin/sleep [30]
2026/10/08 12:18:01 system: terminating test-singleton#1 (pid 3377737)
--- PASS: TestLaunchManagedSingleton (0.00s)
=== RUN   TestRecordLaunchFailure
2026/10/08 12:18:01 system: test-singleton#1 (pid 3377737) exited (exit -1)
2026/10/08 12:18:01 system: failed to launch test-missing#1: could not locate "this-binary-does-not-exist-anywhere" (looked next to local-representative, in /AI-evo1-dev/bin, and on PATH)
--- PASS: TestRecordLaunchFailure (0.00s)
=== RUN   TestRunningManagedTokens
2026/10/08 12:18:01 system: launched test-tokens-single#1 (pid 3377738): /usr/bin/sleep [30]
2026/10/08 12:18:01 system: launched test-tokens-multi#1 (pid 3377739): /usr/bin/sleep [30]
2026/10/08 12:18:01 system: launched test-tokens-multi#2 (pid 3377740): /usr/bin/sleep [30]
2026/10/08 12:18:01 system: terminating test-tokens-single#1 (pid 3377738)
2026/10/08 12:18:01 system: test-tokens-single#1 (pid 3377738) exited (exit -1)
2026/10/08 12:18:01 system: terminating test-tokens-multi#1 (pid 3377739)
2026/10/08 12:18:01 system: terminating test-tokens-multi#2 (pid 3377740)
--- PASS: TestRunningManagedTokens (0.02s)
=== RUN   TestTerminateManagedForRestart
2026/10/08 12:18:01 system: test-tokens-multi#2 (pid 3377740) exited (exit -1)
2026/10/08 12:18:01 system: test-tokens-multi#1 (pid 3377739) exited (exit -1)
2026/10/08 12:18:01 system: launched test-restart-all#1 (pid 3377742): /usr/bin/sleep [30]
2026/10/08 12:18:01 system: launched test-restart-all#2 (pid 3377743): /usr/bin/sleep [30]
2026/10/08 12:18:01 restart: terminating 2 LR-launched managed instance(s) before exit: test-restart-all#1, test-restart-all#2
2026/10/08 12:18:01 system: terminating test-restart-all#1 (pid 3377742)
2026/10/08 12:18:01 system: terminating test-restart-all#2 (pid 3377743)
2026/10/08 12:18:01 system: test-restart-all#2 (pid 3377743) exited (exit -1)
2026/10/08 12:18:01 system: test-restart-all#1 (pid 3377742) exited (exit -1)
--- PASS: TestTerminateManagedForRestart (0.02s)
=== RUN   TestResolveConfigDevRepo
--- PASS: TestResolveConfigDevRepo (0.00s)
=== RUN   TestRepoWatchPollAndMaybePull
--- PASS: TestRepoWatchPollAndMaybePull (0.05s)
=== RUN   TestRepoWatchRebuildSuccessClearsRebuildReady
2026/10/08 12:18:01 dev-repo: running 'make deploy-dev-binaries' in /tmp/TestRepoWatchRebuildSuccessClearsRebuildReady1879986727/001
2026/10/08 12:18:01 dev-repo: rebuild succeeded
--- PASS: TestRepoWatchRebuildSuccessClearsRebuildReady (0.04s)
=== RUN   TestRepoWatchRebuildFailureRecordsError
2026/10/08 12:18:01 dev-repo: running 'make deploy-dev-binaries' in /tmp/TestRepoWatchRebuildFailureRecordsError2632403099/001
2026/10/08 12:18:01 dev-repo: rebuild failed: exit status 2: make[2]: Entering directory '/tmp/TestRepoWatchRebuildFailureRecordsError2632403099/001'
exit 1
make[2]: *** [Makefile:2: deploy-dev-binaries] Error 1
make[2]: Leaving directory '/tmp/TestRepoWatchRebuildFailureRecordsError2632403099/001'
--- PASS: TestRepoWatchRebuildFailureRecordsError (0.02s)
=== RUN   TestRepoWatchRequestRebuildSkipsWhenNotReady
2026/10/08 12:18:01 dev-repo: running 'make deploy-dev-binaries' in /tmp/TestRepoWatchRequestRebuildSkipsWhenNotReady2271942831/001
2026/10/08 12:18:01 dev-repo: rebuild succeeded
--- PASS: TestRepoWatchRequestRebuildSkipsWhenNotReady (0.32s)
=== RUN   TestRepoWatchRequestRebuildSkipsWhenDirty
--- PASS: TestRepoWatchRequestRebuildSkipsWhenDirty (0.33s)
=== RUN   TestRepoWatchAutoRebuildDebounces
--- PASS: TestRepoWatchAutoRebuildDebounces (0.04s)
=== RUN   TestRepoWatchAutoRebuildFiresWhenDeadlineElapses
2026/10/08 12:18:02 dev-repo: running 'make deploy-dev-binaries' in /tmp/TestRepoWatchAutoRebuildFiresWhenDeadlineElapses1392766570/001
2026/10/08 12:18:02 dev-repo: rebuild succeeded
    repowatch_test.go:302: expected the debounce timer to clear once the rebuild fires
--- FAIL: TestRepoWatchAutoRebuildFiresWhenDeadlineElapses (0.05s)
=== RUN   TestRepoWatchAutoRebuildResetsOnFurtherChange
--- PASS: TestRepoWatchAutoRebuildResetsOnFurtherChange (0.06s)
=== RUN   TestRepoWatchAutoRebuildDisarmsWhenNotReady
--- PASS: TestRepoWatchAutoRebuildDisarmsWhenNotReady (0.04s)
=== RUN   TestRepoWatchAutoRebuildOffStaysDisarmed
--- PASS: TestRepoWatchAutoRebuildOffStaysDisarmed (0.04s)
=== RUN   TestRepoWatchCondocLockForcesRebuildNotReady
--- PASS: TestRepoWatchCondocLockForcesRebuildNotReady (0.03s)
=== RUN   TestRepoWatchBuildLockDefersCompletionWhilePresent
--- PASS: TestRepoWatchBuildLockDefersCompletionWhilePresent (0.02s)
=== RUN   TestRepoWatchNewRepoWatchSeedsBuildingFromLockFile
--- PASS: TestRepoWatchNewRepoWatchSeedsBuildingFromLockFile (0.02s)
=== RUN   TestServerRebuildControlsNoRepoWatched
2026/10/08 12:18:02 system: rebuild requested (test) but no repo is being watched (launch with --dev-repo)
2026/10/08 12:18:02 system: rebuild requested (operator) but no repo is being watched (launch with --dev-repo)
--- PASS: TestServerRebuildControlsNoRepoWatched (0.00s)
=== RUN   TestCurrentStateCapturesAutoRebuild
--- PASS: TestCurrentStateCapturesAutoRebuild (0.02s)
=== RUN   TestCurrentStateCapturesAutoUpdate
--- PASS: TestCurrentStateCapturesAutoUpdate (0.00s)
=== RUN   TestCurrentStateCapturesACTarget
    reststate_test.go:64: expected AutoConnect=true at 10.0.0.5:9000 while connecting, got {AutoRebuild:false AutoUpdate:false AutoConnect:false ACHost: ACPort: ManagedApps:[]}
--- FAIL: TestCurrentStateCapturesACTarget (0.00s)
=== RUN   TestCurrentStateCapturesManagedApps
2026/10/08 12:18:02 system: launched test-reststate-managed#1 (pid 3377946): /usr/bin/sleep [30]
2026/10/08 12:18:02 system: terminating test-reststate-managed#1 (pid 3377946)
--- PASS: TestCurrentStateCapturesManagedApps (0.00s)
=== RUN   TestLoadPreviousStateNoEnv
--- PASS: TestLoadPreviousStateNoEnv (0.00s)
=== RUN   TestLoadPreviousStateRoundTrip
2026/10/08 12:18:02 system: test-reststate-managed#1 (pid 3377946) exited (exit -1)
2026/10/08 12:18:02 ignoring unparseable restart state: invalid character 'o' in literal null (expecting 'u')
--- PASS: TestLoadPreviousStateRoundTrip (0.00s)
=== RUN   TestApplyToConfigOverridesArguments
--- PASS: TestApplyToConfigOverridesArguments (0.00s)
=== RUN   TestApplyToConfigOverridesAutoLaunch
--- PASS: TestApplyToConfigOverridesAutoLaunch (0.00s)
=== RUN   TestSelfVersionWatchPoll
--- PASS: TestSelfVersionWatchPoll (0.00s)
=== RUN   TestSelfVersionWatchPollFailure
2026/10/08 12:18:02 self-version: /tmp/TestSelfVersionWatchPollFailure2955890497/001/does-not-exist --version failed: fork/exec /tmp/TestSelfVersionWatchPollFailure2955890497/001/does-not-exist: no such file or directory
--- PASS: TestSelfVersionWatchPollFailure (0.00s)
=== RUN   TestSelfVersionWatchNilSafe
--- PASS: TestSelfVersionWatchNilSafe (0.00s)
=== RUN   TestSelfVersionWatchPollPending
--- PASS: TestSelfVersionWatchPollPending (0.00s)
=== RUN   TestSelfVersionWatchAutoUpdateFiresOnPoll
--- PASS: TestSelfVersionWatchAutoUpdateFiresOnPoll (0.00s)
=== RUN   TestSelfVersionWatchAutoUpdateFiresOnToggle
--- PASS: TestSelfVersionWatchAutoUpdateFiresOnToggle (0.00s)
=== RUN   TestServerSetAutoUpdateNoSelfVersion
--- PASS: TestServerSetAutoUpdateNoSelfVersion (0.00s)
=== RUN   TestHandleSessionsListMatchesGlobAndReportsChecksum
--- PASS: TestHandleSessionsListMatchesGlobAndReportsChecksum (0.00s)
=== RUN   TestHandleSessionsFileServesBytesAndGuardsTraversal
--- PASS: TestHandleSessionsFileServesBytesAndGuardsTraversal (0.00s)
=== RUN   TestHandleSessionsPullRefusesProxiedRequest
--- PASS: TestHandleSessionsPullRefusesProxiedRequest (0.00s)
=== RUN   TestHandleSessionsPullNoACIsANoOp
--- PASS: TestHandleSessionsPullNoACIsANoOp (0.00s)
=== RUN   TestPullSessionFilesFromOnceNeverRefetchesAnExistingName
--- PASS: TestPullSessionFilesFromOnceNeverRefetchesAnExistingName (0.01s)
=== RUN   TestPullSessionFilesFromSyncRefetchesOnChecksumMismatch
--- PASS: TestPullSessionFilesFromSyncRefetchesOnChecksumMismatch (0.00s)
=== RUN   TestPullSessionFilesFromReportsNotOKOnUnreachableHost
2026/10/08 12:18:02 sessions pull: listing sess-1 on host host-b: unexpected status 502
--- PASS: TestPullSessionFilesFromReportsNotOKOnUnreachableHost (0.00s)
=== RUN   TestHandleSessionsIndexListsEverySessionWithName
--- PASS: TestHandleSessionsIndexListsEverySessionWithName (0.00s)
=== RUN   TestHandleSessionsDiscoverRefusesProxiedRequest
--- PASS: TestHandleSessionsDiscoverRefusesProxiedRequest (0.00s)
=== RUN   TestIndexSessionsFromTagsEachEntryWithItsHost
--- PASS: TestIndexSessionsFromTagsEachEntryWithItsHost (0.00s)
=== RUN   TestHandleSessionsDiscoverNoACIsANoOp
--- PASS: TestHandleSessionsDiscoverNoACIsANoOp (0.00s)
=== RUN   TestTCAvailabilityCommand
--- PASS: TestTCAvailabilityCommand (0.00s)
=== RUN   TestHandleTCAvailabilityCommand
--- PASS: TestHandleTCAvailabilityCommand (0.00s)
=== RUN   TestSingleConnListenerAcceptsOnceThenUnblocksOnClose
--- PASS: TestSingleConnListenerAcceptsOnceThenUnblocksOnClose (0.05s)
=== RUN   TestFirstByteConnOnlyFiresOnRealData
--- PASS: TestFirstByteConnOnlyFiresOnRealData (0.00s)
=== RUN   TestFirstByteConnFiresOnceRealDataArrives
--- PASS: TestFirstByteConnFiresOnceRealDataArrives (0.00s)
FAIL
FAIL	local-representative	3.015s
FAIL
make[1]: *** [Makefile:55: test] Error 1
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/local-representative'

=== Testing agent-coordinator ===
make[1]: Entering directory '/home/jedsall/workspace/research/AI-evo1/agent-coordinator'
go test -v ./...
=== RUN   TestConnectedHostNames
--- PASS: TestConnectedHostNames (0.00s)
=== RUN   TestRelayNodeCaptureWithoutRepresentable
2026/10/08 12:18:03 control: bad node-capture request from lr-a: invalid character 'o' in literal null (expecting 'u')
2026/10/08 12:18:03 control: bad node-fetch request from lr-a: invalid character 'o' in literal null (expecting 'u')
--- PASS: TestRelayNodeCaptureWithoutRepresentable (0.00s)
=== RUN   TestFCTargeted
--- PASS: TestFCTargeted (0.00s)
=== RUN   TestFCInstancesMsgDefaultsToEmptyList
--- PASS: TestFCInstancesMsgDefaultsToEmptyList (0.00s)
=== RUN   TestFileInfoHighlightedRoundTrips
--- PASS: TestFileInfoHighlightedRoundTrips (0.00s)
=== RUN   TestFileInfoMarkedUpRoundTrips
--- PASS: TestFileInfoMarkedUpRoundTrips (0.00s)
=== RUN   TestHandleFileUploadRelay
--- PASS: TestHandleFileUploadRelay (0.00s)
=== RUN   TestHandleFileUploadRelayUnknownHost
--- PASS: TestHandleFileUploadRelayUnknownHost (0.00s)
=== RUN   TestProxyToHostStripsClientSuppliedRelayHeader
--- PASS: TestProxyToHostStripsClientSuppliedRelayHeader (0.00s)
=== RUN   TestHandleHostsAPIReportsConnectedHosts
--- PASS: TestHandleHostsAPIReportsConnectedHosts (0.00s)
=== RUN   TestHandleHostsAPIRejectsNonGET
--- PASS: TestHandleHostsAPIRejectsNonGET (0.00s)
=== RUN   TestProcInfoAutoUpdateRoundTrips
--- PASS: TestProcInfoAutoUpdateRoundTrips (0.00s)
=== RUN   TestRepoStateCondocLockedRoundTrips
--- PASS: TestRepoStateCondocLockedRoundTrips (0.00s)
=== RUN   TestSelfVersionWatchPoll
--- PASS: TestSelfVersionWatchPoll (0.00s)
=== RUN   TestSelfVersionWatchPollFailure
2026/10/08 12:18:03 self-version: /tmp/TestSelfVersionWatchPollFailure2348006023/001/does-not-exist --version failed: fork/exec /tmp/TestSelfVersionWatchPollFailure2348006023/001/does-not-exist: no such file or directory
--- PASS: TestSelfVersionWatchPollFailure (0.00s)
=== RUN   TestSelfVersionWatchNilSafe
--- PASS: TestSelfVersionWatchNilSafe (0.00s)
=== RUN   TestSelfVersionWatchPollPending
--- PASS: TestSelfVersionWatchPollPending (0.00s)
=== RUN   TestSelfVersionWatchAutoUpdateFiresOnPoll
--- PASS: TestSelfVersionWatchAutoUpdateFiresOnPoll (0.00s)
=== RUN   TestSelfVersionWatchAutoUpdateFiresOnToggle
--- PASS: TestSelfVersionWatchAutoUpdateFiresOnToggle (0.00s)
=== RUN   TestServerSetAutoUpdateNoSelfVersion
--- PASS: TestServerSetAutoUpdateNoSelfVersion (0.00s)
=== RUN   TestAnyConvoAvailable
--- PASS: TestAnyConvoAvailable (0.00s)
=== RUN   TestBroadcastTCAvailabilityOnlyBroadcastsOnChange
--- PASS: TestBroadcastTCAvailabilityOnlyBroadcastsOnChange (0.00s)
=== RUN   TestTCAvailabilityCommand
--- PASS: TestTCAvailabilityCommand (0.00s)
=== RUN   TestHostProxyTransportPrefersTunnel
--- PASS: TestHostProxyTransportPrefersTunnel (0.00s)
=== RUN   TestHostProxyTransportFallsBackWithoutTunnel
--- PASS: TestHostProxyTransportFallsBackWithoutTunnel (0.00s)
PASS
ok  	agent-coordinator	0.020s
make[1]: Leaving directory '/home/jedsall/workspace/research/AI-evo1/agent-coordinator'

=== Tests FAILED in: clauditable federation-command condoccer local-representative ===
make: *** [Makefile:21: test-all] Error 1


Let's fix these failures now.


prompt: [`bd637a5`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/bd637a5a32254e1f5e980f98ae08bbc8e17ba6c6) → reply: [`adea047`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/adea047e3de34e02155850fa9395a8173d0c0890)

## Reply A

I fixed all seven failing tests in the four sub-projects, but I couldn't run anything: `go` and `gofmt` both needed approval again. Please re-run `make test`.

Two of the failures came from real bugs in the program code. The other five were tests that were wrong or out of date.

- **federation-command (`TestParseCLIArgs`, `TestParseCLIArgsConfigFilePrecedence`, `TestAutoConnectImpliesRemote`):** same cause as last time. LR sets `FC_AUTO_CONNECT`, `FC_DEV_MODE`, `FC_LR_HOST`, `FC_LR_PORT`, `FC_SESSION` and `FC_INSTANCE_ID` when it launches FC. FC never cleared them, so every shell FC opens inherited them.
  - **The bug:** an FC started from one of those shells would quietly auto-connect, turn on dev mode and take the parent's instance ID. `main.go` now has a `clearLaunchEnv()` that removes these variables once FC has read them.
  - **The tests:** a new `isolateLaunchEnv(t)` helper unsets them in the CLI-parsing tests.
- **local-representative `TestRepoWatchAutoRebuildFiresWhenDeadlineElapses`:** a real bug. When the 90s auto-rebuild countdown ran out, the countdown wasn't cleared until the next check, so the status showed "auto-rebuild pending, 0s" for the whole build. `maybeAutoRebuild` in `repowatch.go` now clears it before starting the rebuild.
- **local-representative `TestCurrentStateCapturesACTarget`:** out of date. Revision I changed the saved restart state to record the auto-connect toggle rather than whether a connection attempt is in progress. I rewrote the test to check the toggle.
- **local-representative `TestExportImport`:** out of date. The robot hand-off example sequence gained two actions (`fetch-node-file`, `fc-remove-file`), so the test now expects 8 actions instead of 6.
- **clauditable `TestConsolidatePrimaryToJSONLPromotesLocalSecondary`:** the test's mistake. It added its marker text after the raw-output separator, a part of the file that is deliberately left out of `session.jsonl`, so the marker could never show up. It now goes in the part that is copied over.
- **condoccer `TestAddVoiceNoteResource`:** the test's mistake. It expected an empty Impls folder, but its own setup puts `Step1Prompt.md` there. It now only fails if a voice note adds some other file.

These tests had probably been failing for a while without anyone seeing them, because `make test` used to stop at the first sub-project that failed.


## Substep Completed

This substep was completed at 1791462545 (Thu Oct 8 12:29:05 PM UTC 2026).
