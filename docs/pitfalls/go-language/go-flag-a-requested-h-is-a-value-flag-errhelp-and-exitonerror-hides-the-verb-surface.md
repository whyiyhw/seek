### go flag: a requested -h is a VALUE (flag.ErrHelp), and ExitOnError hides the verb surface
- **Saw**: `seek serve --frontend=web -h` exited 2 with usage on stderr; `seek --help` listed only flags, so verbs looked nonexistent.
- **Why**: a sub-FlagSet's `ContinueOnError` returns `flag.ErrHelp` as an error that servecli's catch-all fed to `badConfig` (`args[0]` only was checked); `flag.CommandLine`'s `ExitOnError` `-h` prints `PrintDefaults` only, so verbs dispatched before `flag.Parse` never appear.
- **Fix**: recognise `-h`/`--help` anywhere before `--` and print help to stdout; keep `errors.Is(err, flag.ErrHelp) → nil`; a custom `flag.Usage` lists commands.
- **Lesson**: requested help is never an error, nor only args[0]; `flag.PrintDefaults` is not discoverability — whatever `main` dispatches by hand must be listed by hand.
- **Refs**: `internal/servecli/servecli.go:Run`, `cmd/seek/main.go:helpRequested`/`printUsage`, `TestRun_HelpFlag_AnywhereBeforeTerminator`, `TestHelpRequested`, `TestPrintUsage_ListsCommands`
