package midi

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	helperCommand      = "_midi-helper"
	opSend        byte = 1
	opClose       byte = 2
	statusOK      byte = 0
	statusError   byte = 1
	maximumFrame       = 1 << 20
	helperTimeout      = 5 * time.Second
)

type processDriver struct {
	executable string
}

type processOutput struct {
	mu      sync.Mutex
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	stderr  *bytes.Buffer
	closed  bool
}

// NewDriver constructs a process-isolated native driver. RtMidi can terminate
// its process on some native initialization errors, so it is never loaded in
// the long-running application process.
func NewDriver() (Driver, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate executable for MIDI helper: %w", err)
	}
	return &processDriver{executable: executable}, nil
}

func (d *processDriver) Devices() ([]Device, error) {
	command := exec.Command(d.executable, helperCommand, "devices")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := runWithTimeout(command, helperTimeout); err != nil {
		return nil, helperFailure("enumerate MIDI devices", err, stderr.String())
	}
	var devices []Device
	if err := json.Unmarshal(stdout.Bytes(), &devices); err != nil {
		return nil, fmt.Errorf("decode MIDI helper response: %w", err)
	}
	return devices, nil
}

func (d *processDriver) Open(number int) (Output, error) {
	command := exec.Command(d.executable, helperCommand, "output", strconv.Itoa(number))
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create MIDI helper input: %w", err)
	}
	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("create MIDI helper output: %w", err)
	}
	stderr := &bytes.Buffer{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start MIDI helper: %w", err)
	}
	output := &processOutput{
		command: command,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdoutPipe),
		stderr:  stderr,
	}
	status, message, err := readFrameWithTimeout(output.stdout, helperTimeout)
	if err != nil {
		_ = stdin.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		waitErr := command.Wait()
		return nil, helperFailure("open MIDI output", errors.Join(err, waitErr), stderr.String())
	}
	if status != statusOK {
		_ = stdin.Close()
		_ = command.Wait()
		return nil, errors.New(string(message))
	}
	return output, nil
}

func (d *processDriver) Close() error { return nil }

func (o *processOutput) Send(message []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return errors.New("MIDI output is closed")
	}
	if err := writeFrame(o.stdin, opSend, message); err != nil {
		return fmt.Errorf("write MIDI helper request: %w", err)
	}
	status, response, err := readFrameWithTimeout(o.stdout, helperTimeout)
	if err != nil {
		o.terminate()
		return helperFailure("read MIDI helper response", err, o.stderr.String())
	}
	if status != statusOK {
		return errors.New(string(response))
	}
	return nil
}

func (o *processOutput) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	requestErr := writeFrame(o.stdin, opClose, nil)
	var responseErr error
	if requestErr == nil {
		status, response, err := readFrameWithTimeout(o.stdout, helperTimeout)
		responseErr = err
		if err == nil && status != statusOK {
			responseErr = errors.New(string(response))
		}
	}
	_ = o.stdin.Close()
	waitErr := waitWithTimeout(o.command, helperTimeout)
	if err := errors.Join(requestErr, responseErr, waitErr); err != nil {
		return helperFailure("close MIDI helper", err, o.stderr.String())
	}
	return nil
}

func (o *processOutput) terminate() {
	o.closed = true
	_ = o.stdin.Close()
	if o.command.Process != nil {
		_ = o.command.Process.Kill()
	}
	_ = o.command.Wait()
}

func writeFrame(writer io.Writer, kind byte, payload []byte) error {
	if len(payload) > maximumFrame {
		return errors.New("MIDI helper frame exceeds size limit")
	}
	header := [5]byte{kind}
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err := writer.Write(payload)
	return err
}

func readFrame(reader io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > maximumFrame {
		return 0, nil, errors.New("MIDI helper frame exceeds size limit")
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

type frameResult struct {
	kind    byte
	payload []byte
	err     error
}

func readFrameWithTimeout(reader io.Reader, timeout time.Duration) (byte, []byte, error) {
	done := make(chan frameResult, 1)
	go func() {
		kind, payload, err := readFrame(reader)
		done <- frameResult{kind: kind, payload: payload, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.kind, result.payload, result.err
	case <-timer.C:
		return 0, nil, errors.New("MIDI helper response timed out")
	}
}

func runWithTimeout(command *exec.Cmd, timeout time.Duration) error {
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		<-done
		return errors.New("MIDI helper timed out")
	}
}

func waitWithTimeout(command *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		<-done
		return errors.New("MIDI helper shutdown timed out")
	}
}

func helperFailure(operation string, err error, stderr string) error {
	stderr = summarizeStderr(stderr)
	if stderr == "" {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w: %s", operation, err, stderr)
}

func summarizeStderr(stderr string) string {
	var summary []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "SIG") || strings.HasPrefix(line, "goroutine ") || strings.HasPrefix(line, "PC=") {
			break
		}
		summary = append(summary, line)
		if len(summary) == 3 {
			break
		}
	}
	return strings.Join(summary, "; ")
}
