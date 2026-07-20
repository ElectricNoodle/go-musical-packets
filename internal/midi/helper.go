package midi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

// RunHelper runs the private native MIDI subprocess protocol.
func RunHelper(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "MIDI helper command is required")
		return 2
	}
	switch args[0] {
	case "devices":
		return helperDevices(stdout, stderr)
	case "output":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "MIDI helper output number is required")
			return 2
		}
		number, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintln(stderr, "MIDI helper output number is invalid")
			return 2
		}
		return helperOutput(number, stdin, stdout)
	default:
		fmt.Fprintln(stderr, "unknown MIDI helper command")
		return 2
	}
}

func helperDevices(stdout, stderr io.Writer) int {
	driver, err := newNativeDriver()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer driver.Close()
	devices, err := driver.Devices()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(devices); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func helperOutput(number int, stdin io.Reader, stdout io.Writer) int {
	driver, err := newNativeDriver()
	if err != nil {
		_ = writeFrame(stdout, statusError, []byte(err.Error()))
		return 1
	}
	defer driver.Close()
	output, err := driver.Open(number)
	if err != nil {
		_ = writeFrame(stdout, statusError, []byte(err.Error()))
		return 1
	}
	defer output.Close()
	if err := writeFrame(stdout, statusOK, nil); err != nil {
		return 1
	}

	reader := bufio.NewReader(stdin)
	for {
		op, payload, err := readFrame(reader)
		if err != nil {
			return 1
		}
		switch op {
		case opSend:
			err = output.Send(payload)
		case opClose:
			err = output.Close()
			if err != nil {
				_ = writeFrame(stdout, statusError, []byte(err.Error()))
				return 1
			}
			_ = writeFrame(stdout, statusOK, nil)
			return 0
		default:
			err = fmt.Errorf("unknown MIDI helper operation %d", op)
		}
		if err != nil {
			if writeFrame(stdout, statusError, []byte(err.Error())) != nil {
				return 1
			}
			continue
		}
		if writeFrame(stdout, statusOK, nil) != nil {
			return 1
		}
	}
}
