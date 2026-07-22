package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ElectricNoodle/go-musical-packets/internal/app"
	"github.com/ElectricNoodle/go-musical-packets/internal/capture"
	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/midi"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	// The private MIDI helper must retain the operating system's default signal
	// behavior so its parent can terminate it if native I/O becomes unresponsive.
	if len(os.Args) > 1 && os.Args[1] == "_midi-helper" {
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runContext(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runContext(context.Background(), args, stdout, stderr)
}

func runContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	switch args[0] {
	case "_midi-helper":
		return midi.RunHelper(args[1:], os.Stdin, stdout, stderr)
	case "run":
		return runService(ctx, args[1:], stdout, stderr)
	case "replay":
		return runReplay(ctx, args[1:], stdout, stderr)
	case "validate-config":
		return validateConfig(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "musical-packets %s (%s)\n", version, commit)
		return 0
	case "interfaces":
		return printInterfaces(stdout, stderr)
	case "devices":
		return printMIDIDevices(stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: musical-packets <command>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  run              run the configured standalone, edge, or host service")
	fmt.Fprintln(w, "  replay           replay a PCAP file through the MIDI pipeline")
	fmt.Fprintln(w, "  validate-config  validate a YAML configuration file")
	fmt.Fprintln(w, "  interfaces       list packet-capture interfaces")
	fmt.Fprintln(w, "  devices          list MIDI output devices")
	fmt.Fprintln(w, "  version          print build version information")
	fmt.Fprintln(w, "  help             print this help")
}

func runReplay(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	recordingPath, configurationPath, code := replayPaths(args, stdout, stderr)
	if code >= 0 {
		return code
	}
	configuration, err := config.Load(configurationPath)
	if err != nil {
		fmt.Fprintf(stderr, "load configuration: %v\n", err)
		return 1
	}
	logger := applicationLogger(configuration.Logging, stderr)
	logger.Info("starting PCAP replay", "path", recordingPath)
	if err := app.RunReplay(ctx, configuration, recordingPath); err != nil {
		logger.Error("PCAP replay stopped", "error", err)
		return 1
	}
	if ctx.Err() != nil {
		logger.Info("PCAP replay canceled", "path", recordingPath)
		return 0
	}
	logger.Info("PCAP replay completed", "path", recordingPath)
	return 0
}

func runService(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	path, code := configPath("run", args, stdout, stderr)
	if code >= 0 {
		return code
	}
	repository, err := config.NewFileRepository(path)
	if err != nil {
		fmt.Fprintf(stderr, "load configuration: %v\n", err)
		return 1
	}
	snapshot, err := repository.Read()
	if err != nil {
		fmt.Fprintf(stderr, "load configuration: %v\n", err)
		return 1
	}
	configuration := snapshot.Config
	logger := applicationLogger(configuration.Logging, stderr)
	logger.Info("starting musical-packets service",
		"instance", configuration.Instance.ID,
		"role", configuration.Instance.Role,
		"listen_address", configuration.Server.ListenAddress,
	)
	if err := app.RunWithOptions(ctx, configuration, app.RunOptions{
		ConfigPath:       path,
		ExpectedRevision: snapshot.Revision,
	}); err != nil {
		logger.Error("musical-packets service stopped", "error", err)
		return 1
	}
	logger.Info("musical-packets service stopped", "role", configuration.Instance.Role)
	return 0
}

func applicationLogger(configuration config.LoggingConfig, writer io.Writer) *slog.Logger {
	var level slog.Level
	switch configuration.Level {
	case config.LogLevelDebug:
		level = slog.LevelDebug
	case config.LogLevelWarn:
		level = slog.LevelWarn
	case config.LogLevelError:
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	options := &slog.HandlerOptions{Level: level}
	if configuration.Format == config.LogFormatJSON {
		return slog.New(slog.NewJSONHandler(writer, options))
	}
	return slog.New(slog.NewTextHandler(writer, options))
}

func validateConfig(args []string, stdout, stderr io.Writer) int {
	path, code := configPath("validate-config", args, stdout, stderr)
	if code >= 0 {
		return code
	}
	if _, err := config.Load(path); err != nil {
		fmt.Fprintf(stderr, "invalid configuration: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "configuration is valid")
	return 0
}

// configPath returns a non-negative exit code when flag processing has already
// handled the command. A negative code means the caller may proceed.
func configPath(command string, args []string, stdout, stderr io.Writer) (string, int) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	var flagOutput bytes.Buffer
	flags.SetOutput(&flagOutput)
	flags.Usage = func() {
		fmt.Fprintf(&flagOutput, "usage: musical-packets %s --config <path>\n", command)
		flags.PrintDefaults()
	}
	path := flags.String("config", "", "path to the YAML configuration file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.Copy(stdout, &flagOutput)
			return "", 0
		}
		_, _ = io.Copy(stderr, &flagOutput)
		return "", 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "%s: unexpected arguments: %s\n", command, strings.Join(flags.Args(), " "))
		writeConfigUsage(stderr, command)
		return "", 2
	}
	if strings.TrimSpace(*path) == "" {
		fmt.Fprintf(stderr, "%s: --config is required\n", command)
		writeConfigUsage(stderr, command)
		return "", 2
	}
	return *path, -1
}

func writeConfigUsage(writer io.Writer, command string) {
	fmt.Fprintf(writer, "usage: musical-packets %s --config <path>\n", command)
	fmt.Fprintln(writer, "  -config string")
	fmt.Fprintln(writer, "    \tpath to the YAML configuration file")
}

// replayPaths accepts the recording before or after --config. The standard
// flag package stops parsing at the first positional argument, while the
// documented command form puts the recording first.
func replayPaths(args []string, stdout, stderr io.Writer) (recordingPath, configurationPath string, code int) {
	usage := func(writer io.Writer) {
		fmt.Fprintln(writer, "usage: musical-packets replay <recording.pcap> --config <path>")
		fmt.Fprintln(writer, "  -config string")
		fmt.Fprintln(writer, "    \tpath to the YAML configuration file")
	}

	positionals := make([]string, 0, 1)
	positionalOnly := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if positionalOnly {
			positionals = append(positionals, argument)
			continue
		}
		switch {
		case argument == "-h" || argument == "--help":
			usage(stdout)
			return "", "", 0
		case argument == "-config" || argument == "--config":
			if index+1 >= len(args) {
				fmt.Fprintln(stderr, "replay: --config requires a value")
				usage(stderr)
				return "", "", 2
			}
			index++
			configurationPath = args[index]
		case strings.HasPrefix(argument, "-config="):
			configurationPath = strings.TrimPrefix(argument, "-config=")
		case strings.HasPrefix(argument, "--config="):
			configurationPath = strings.TrimPrefix(argument, "--config=")
		case argument == "--":
			positionalOnly = true
		case strings.HasPrefix(argument, "-"):
			fmt.Fprintf(stderr, "replay: unknown flag %q\n", argument)
			usage(stderr)
			return "", "", 2
		default:
			positionals = append(positionals, argument)
		}
	}

	if len(positionals) != 1 || (len(positionals) == 1 && strings.TrimSpace(positionals[0]) == "") {
		if len(positionals) == 0 || (len(positionals) == 1 && strings.TrimSpace(positionals[0]) == "") {
			fmt.Fprintln(stderr, "replay: recording path is required")
		} else {
			fmt.Fprintf(stderr, "replay: unexpected arguments: %s\n", strings.Join(positionals[1:], " "))
		}
		usage(stderr)
		return "", "", 2
	}
	if strings.TrimSpace(configurationPath) == "" {
		fmt.Fprintln(stderr, "replay: --config is required")
		usage(stderr)
		return "", "", 2
	}
	return positionals[0], configurationPath, -1
}

func printInterfaces(stdout, stderr io.Writer) int {
	interfaces, err := capture.Interfaces()
	if err != nil {
		fmt.Fprintf(stderr, "list capture interfaces: %v\n", err)
		return 1
	}
	for _, device := range interfaces {
		state := "down"
		if device.Up {
			state = "up"
		}
		if device.Loopback {
			state += ",loopback"
		}
		addresses := make([]string, 0, len(device.Addresses))
		for _, address := range device.Addresses {
			addresses = append(addresses, address.String())
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s", device.Name, state, strings.Join(addresses, ","))
		if device.Description != "" {
			fmt.Fprintf(stdout, "\t%s", device.Description)
		}
		fmt.Fprintln(stdout)
	}
	return 0
}

func printMIDIDevices(stdout, stderr io.Writer) int {
	driver, err := midi.NewDriver()
	if err != nil {
		fmt.Fprintf(stderr, "initialize MIDI: %v\n", err)
		return 1
	}
	defer func() {
		if err := driver.Close(); err != nil {
			fmt.Fprintf(stderr, "close MIDI driver: %v\n", err)
		}
	}()

	devices, err := driver.Devices()
	if err != nil {
		fmt.Fprintf(stderr, "list MIDI devices: %v\n", err)
		return 1
	}
	for _, device := range devices {
		fmt.Fprintf(stdout, "%d\t%s\n", device.Number, device.Name)
	}
	return 0
}
