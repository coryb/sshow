package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/coryb/sshow"
	"github.com/google/uuid"
)

func main() {
	globalFlags := flag.NewFlagSet("", flag.ContinueOnError)
	verbose := false
	globalFlags.BoolVar(&verbose, "v", false, "verbose logging for debugging")
	globalFlags.Parse(os.Args[1:])

	args := globalFlags.Args()
	ctx := context.Background()

	if verbose {
		ctx = sshow.WithLogger(ctx, sshow.DebugLogger{Logger: sshow.Log(ctx)})
	}

	if len(args) == 0 {
		sshow.Log(ctx).Printf("Usage: %s server|ssh|broker", os.Args[0])
		os.Exit(1)
	}

	switch args[0] {
	case "server":
		serverFlags := flag.NewFlagSet("server", flag.ExitOnError)
		var runOptions sshow.ServerRunOptions
		serverFlags.StringVar(&runOptions.LocalCommand, "cmd", "bash -l", "default command run for incomming ssh connections")
		serverFlags.BoolVar(&runOptions.MirrorCommand, "mirror", true, "mirror ssh command output to current terminal")
		serverFlags.StringVar(&runOptions.SessionKey, "key", uuid.New().String(), "shared key for client to connect")
		serverFlags.StringVar(&runOptions.STUNServer, "stun", "stun.l.google.com:19302", "STUN server")
		serverFlags.StringVar(&runOptions.SignalServer, "signal", "https://nobo-signaling.appspot.com", "Signal server")
		serverFlags.Parse(args[1:])

		sshow.Log(ctx).Printf("Session Key: %s", runOptions.SessionKey)

		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT)
		ctx, cancel := context.WithCancel(ctx)
		go func() {
			select {
			case <-ctx.Done():
			case <-sig:
				cancel()
			}
		}()

		if err := sshow.ServerRun(ctx, runOptions); err != nil {
			panic(err)
		}
	case "ssh":
		clientFlags := flag.NewFlagSet("ssh", flag.ExitOnError)
		runOptions := sshow.ClientRunOptions{
			Stdin:  os.Stdin,
			Stdout: os.Stdout,
			Stderr: os.Stderr,
		}
		clientFlags.StringVar(&runOptions.SessionKey, "key", "", "shared key for client to connect")
		clientFlags.StringVar(&runOptions.STUNServer, "stun", "stun.l.google.com:19302", "STUN server")
		clientFlags.StringVar(&runOptions.SignalServer, "signal", "https://nobo-signaling.appspot.com", "Signal server")
		clientFlags.Parse(args[1:])

		if runOptions.SessionKey == "" {
			panic("-key option required")
		}

		if err := sshow.ClientRun(ctx, runOptions); err != nil {
			panic(err)
		}

	case "broker":
		brokerFlags := flag.NewFlagSet("broker", flag.ExitOnError)
		runOptions := sshow.BrokerRunOptions{}
		brokerFlags.IntVar(&runOptions.STUNPort, "stun-port", 3478, "STUN server port to listen on")
		brokerFlags.IntVar(&runOptions.SignalPort, "signal-port", 8080, "Signal server port to listen on")
		brokerFlags.Parse(args[1:])

		if err := sshow.BrokerRun(ctx, runOptions); err != nil {
			panic(err)
		}
	default:
		panic(fmt.Sprintf(`Unexpected command %q, expected one of server|client|broker`, args[0]))
	}
}
