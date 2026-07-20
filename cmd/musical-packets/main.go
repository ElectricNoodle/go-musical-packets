package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ElectricNoodle/go-musical-packets/internal/capture"
	"github.com/ElectricNoodle/go-musical-packets/internal/midi"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	switch args[0] {
	case "_midi-helper":
		return midi.RunHelper(args[1:], os.Stdin, stdout, stderr)
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
	fmt.Fprintln(w, "  interfaces    list packet-capture interfaces")
	fmt.Fprintln(w, "  devices       list MIDI output devices")
	fmt.Fprintln(w, "  version       print build version information")
	fmt.Fprintln(w, "  help          print this help")
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
