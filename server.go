package sshow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/creack/pty"
	"github.com/gliderlabs/ssh"
	"github.com/kballard/go-shellquote"
	"github.com/pions/webrtc"
	"github.com/pions/webrtc/pkg/datachannel"
	"github.com/pions/webrtc/pkg/ice"
	"golang.org/x/sync/errgroup"
)

type ServerRunOptions struct {
	LocalCommand  string
	MirrorCommand bool
	SessionKey    string

	STUNServer   string
	SignalServer string
}

func ServerRun(ctx context.Context, opts ServerRunOptions) error {
	portCh := make(chan int)

	ctx = withSTUNServer(ctx, opts.STUNServer)
	ctx = withSignalServer(ctx, opts.SignalServer)

	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		defer Log(ctx).Debugf("SSHD exiting")
		return StartSSHD(ctx, opts.LocalCommand, opts.MirrorCommand, portCh)
	})
	eg.Go(func() error {
		defer Log(ctx).Debugf("Signal server exiting")
		port := <-portCh
		return Serve(ctx, opts.SessionKey, "127.0.0.1:"+strconv.Itoa(port))
	})
	return eg.Wait()
}

func setWinsize(f *os.File, w, h int) error {
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCSWINSZ),
		uintptr(unsafe.Pointer(&struct{ h, w, x, y uint16 }{uint16(h), uint16(w), 0, 0})))
	if err > 0 {
		return err
	}
	return nil
}

func StartSSHD(ctx context.Context, sshCmd string, mirrorSession bool, portCh chan<- int) error {
	cmd, err := shellquote.Split(sshCmd)
	if err != nil {
		return fmt.Errorf("unable to parse ssh command %q: %w", sshCmd, err)
	}

	ssh.Handle(func(s ssh.Session) {
		eg, ctx := errgroup.WithContext(s.Context())
		Log(ctx).Printf("SSH Connection.  Running: %s", sshCmd)
		cmd := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
		ptyReq, winCh, isPty := s.Pty()
		if isPty {
			cmd.Env = append(cmd.Env, fmt.Sprintf("TERM=%s", ptyReq.Term))
			// FIXME use github.com/hinshun/vt10x
			f, err := pty.Start(cmd)
			if err != nil {
				panic(err)
			}
			eg.Go(func() (err error) {
				for {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case win := <-winCh:
						if err := setWinsize(f, win.Width, win.Height); err != nil {
							return fmt.Errorf("failed to set winsize to w:%d, h:%d: %w", win.Width, win.Height, err)
						}
					}
				}
			})
			eg.Go(func() error {
				_, err := io.Copy(f, s) // stdin
				return err
			})

			var output io.Writer = s
			if mirrorSession {
				output = io.MultiWriter(s, os.Stderr)
			}
			// Note not part of errgroup since this is non cancellable
			go func() {
				io.Copy(output, f) // stdout
			}()
			eg.Go(func() error {
				err := cmd.Wait()
				exitError := &exec.ExitError{}
				if errors.As(err, &exitError) {
					s.Exit(exitError.ExitCode())
				} else {
					s.Exit(255)
				}
				return err
			})
			eg.Wait()
		} else {
			Log(ctx).Debugf("No PTY requested, closing connection")
			io.WriteString(s, "No PTY requested.\n")
			s.Exit(1)
		}
	})

	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to find available port for ssh: %w", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	Log(ctx).Printf("starting ssh server on port %d...", port)
	go func() {
		select {
		case <-ctx.Done():
		case portCh <- port:
		}
	}()

	srv := &ssh.Server{Handler: nil}

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	return srv.Serve(l)
}

func Serve(ctx context.Context, key, addr string) error {
	configuration := webrtc.RTCConfiguration{
		IceServers: []webrtc.RTCIceServer{{
			URLs: []string{
				"stun:" + stunServer(ctx),
			},
		}},
	}

	Log(ctx).Debugf("server started")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case v := <-pull(ctx, key):
			Log(ctx).Debugf("info: %#v", v)
			pc, err := webrtc.New(configuration)
			if err != nil {
				Log(ctx).Errorf("rtc error: %s", err)
				continue
			}
			ssh, err := net.Dial("tcp", addr)
			if err != nil {
				Log(ctx).Errorf("ssh dial filed: %s", err)
				pc.Close()
				continue
			}
			pc.OnICEConnectionStateChange(func(state ice.ConnectionState) {
				Log(ctx).Debugf("pc ice state change:", state)
				if state == ice.ConnectionStateDisconnected {
					pc.Close()
					ssh.Close()
				}
			})
			pc.OnDataChannel(func(dc *webrtc.RTCDataChannel) {
				dc.OnOpen(func() {
					Log(ctx).Debugf("dial: %s", addr)
					io.Copy(&sendWrap{dc}, ssh)
					Log(ctx).Debugf("disconnected")
				})
				dc.Onmessage(func(payload datachannel.Payload) {
					if p, ok := payload.(*datachannel.PayloadBinary); ok {
						_, err := ssh.Write(p.Data)
						if err != nil {
							Log(ctx).Errorf("ssh write failed: %s", err)
							pc.Close()
							return
						}
					}
				})
			})
			if err := pc.SetRemoteDescription(webrtc.RTCSessionDescription{
				Type: webrtc.RTCSdpTypeOffer,
				Sdp:  v.SDP,
			}); err != nil {
				Log(ctx).Errorf("rtc error: %s", err)
				pc.Close()
				ssh.Close()
				continue
			}
			answer, err := pc.CreateAnswer(nil)
			if err != nil {
				Log(ctx).Errorf("rtc error: %s", err)
				pc.Close()
				ssh.Close()
				continue
			}
			if err := push(ctx, v.Source, key, answer.Sdp); err != nil {
				Log(ctx).Errorf("rtc error: %s", err)
				pc.Close()
				ssh.Close()
				continue
			}
		}
	}
}

// Much of this code originated from https://github.com/nobonobo/ssh-p2p
// Copyright (c) 2018 irieda nobonobo
// MIT License: https://github.com/nobonobo/ssh-p2p/blob/master/LICENSE

// The SSH daemon is modified from:
// https://github.com/gliderlabs/ssh/blob/master/_examples/ssh-pty/pty.go
// Copyright (c) 2016 Glider Labs. All rights reserved.
// BSD License: https://github.com/gliderlabs/ssh/blob/master/LICENSE
