package sshow

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"

	"github.com/google/uuid"
	"github.com/pions/webrtc"
	"github.com/pions/webrtc/pkg/datachannel"
	"github.com/pions/webrtc/pkg/ice"
	"golang.org/x/sync/errgroup"
)

type ClientRunOptions struct {
	SessionKey string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer

	STUNServer   string
	SignalServer string
}

func ClientRun(ctx context.Context, opts ClientRunOptions) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = withSTUNServer(ctx, opts.STUNServer)
	ctx = withSignalServer(ctx, opts.SignalServer)

	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to allocate port for client: %w", err)
	}
	port := l.Addr().(*net.TCPAddr).Port

	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		sock, err := l.Accept()
		if err != nil {
			return err
		}
		return StartClient(ctx, opts.SessionKey, sock)
	})
	eg.Go(func() error {
		defer cancel()
		cmd := exec.CommandContext(ctx, "ssh", "-t", "-o", "UserKnownHostsFile=/dev/null", "-o", "StrictHostKeyChecking=no", "-p", strconv.Itoa(port), "127.0.0.1")
		cmd.Stdin = opts.Stdin
		cmd.Stdout = opts.Stdout
		cmd.Stderr = opts.Stderr
		return cmd.Run()
	})
	return eg.Wait()
}

type sendWrap struct {
	*webrtc.RTCDataChannel
}

func (s *sendWrap) Write(b []byte) (int, error) {
	err := s.RTCDataChannel.Send(datachannel.PayloadBinary{Data: b})
	return len(b), err
}

func StartClient(ctx context.Context, key string, sock net.Conn) error {
	configuration := webrtc.RTCConfiguration{
		IceServers: []webrtc.RTCIceServer{{
			URLs: []string{
				"stun:" + stunServer(ctx),
			},
		}},
	}

	defer Log(ctx).Printf("Client connect done")
	id := uuid.New().String()
	Log(ctx).Debugf("client id: %s", id)
	pc, err := webrtc.New(configuration)
	if err != nil {
		return fmt.Errorf("rtc error: %w", err)
	}
	pc.OnICEConnectionStateChange(func(state ice.ConnectionState) {
		Log(ctx).Debugf("pc ice state change: %s", state)
		if state == ice.ConnectionStateDisconnected || state == ice.ConnectionStateClosed {
			sock.Close()
		}
	})
	dc, err := pc.CreateDataChannel("data", nil)
	if err != nil {
		pc.Close()
		return fmt.Errorf("create dc failed: %w", err)
	}
	dc.OnOpen(func() {
		io.Copy(&sendWrap{dc}, sock)
		pc.Close()
		Log(ctx).Debugf("disconnected")
	})
	dc.OnMessage(func(payload datachannel.Payload) {
		if p, ok := payload.(*datachannel.PayloadBinary); ok {
			_, err := sock.Write(p.Data)
			if err != nil {
				Log(ctx).Errorf("sock write failed:", err)
				pc.Close()
				return
			}
		}
	})
	Log(ctx).Debugf("DataChannel: %#v", dc)
	go func() {
		for {
			select {
			case <-ctx.Done():
				break
			case v := <-pull(ctx, id):
				Log(ctx).Debugf("info: %#v", v)
				if err := pc.SetRemoteDescription(webrtc.RTCSessionDescription{
					Type: webrtc.RTCSdpTypeAnswer,
					Sdp:  v.SDP,
				}); err != nil {
					Log(ctx).Errorf("rtc error: %s", err)
					pc.Close()
					return
				}
				return
			}
		}
	}()
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		pc.Close()
		return fmt.Errorf("create offer error: %w", err)
	}
	if err := push(ctx, key, id, offer.Sdp); err != nil {
		pc.Close()
		return fmt.Errorf("push error: %w", err)
	}
	return nil
}

// Much of this code originated from https://github.com/nobonobo/ssh-p2p
// Copyright (c) 2018 irieda nobonobo
// MIT License: https://github.com/nobonobo/ssh-p2p/blob/master/LICENSE
