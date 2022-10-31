package sshow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strconv"
	"sync"
	"time"

	"github.com/pion/turn/v2"
	"golang.org/x/sync/errgroup"
)

type BrokerRunOptions struct {
	STUNPort      int
	STUNAddresses []string
	SignalPort    int
}

func BrokerRun(ctx context.Context, opts BrokerRunOptions) error {
	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		defer Log(ctx).Printf("SignalServer ended")
		return StartSignalServer(ctx, opts.SignalPort)
	})
	eg.Go(func() error {
		defer Log(ctx).Printf("TurnServer ended")
		return StartTurnServer(ctx, opts.STUNPort, opts.STUNAddresses)
	})
	return eg.Wait()
}

// // URI default signaling server
// const URI = "http://localhost:8080"

// ConnectInfo SDP by offer or answer
type ConnectInfo struct {
	Source string `json:"source"`
	SDP    string `json:"sdp"`
}

func push(ctx context.Context, dst, src, sdp string) error {
	buf := bytes.NewBuffer(nil)
	if err := json.NewEncoder(buf).Encode(ConnectInfo{
		Source: src,
		SDP:    sdp,
	}); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", signalServer(ctx)+path.Join("/", "push", dst), buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http failed")
	}
	return nil
}

// FIXME this should be a grpc streaming service, not HTTP polling
func pull(ctx context.Context, id string) <-chan ConnectInfo {
	ch := make(chan ConnectInfo)
	var retry time.Duration
	go func() {
		faild := func() {
			if retry < 10 {
				retry++
			}
			time.Sleep(retry * time.Second)
		}
		defer close(ch)
		for {
			req, err := http.NewRequestWithContext(ctx, "GET", signalServer(ctx)+path.Join("/", "pull", id), nil)
			if err != nil {
				if ctx.Err() == context.Canceled {
					return
				}
				Log(ctx).Errorf("get failed: %s", err)
				faild()
				continue
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				if ctx.Err() == context.Canceled {
					return
				}
				Log(ctx).Errorf("get failed: %s", err)
				faild()
				continue
			}
			defer res.Body.Close()
			retry = time.Duration(0)
			var info ConnectInfo
			if err := json.NewDecoder(res.Body).Decode(&info); err != nil {
				if err == io.EOF {
					continue
				}
				if ctx.Err() == context.Canceled {
					return
				}
				Log(ctx).Errorf("get failed: %s", err)
				faild()
				continue
			}
			if len(info.Source) > 0 && len(info.SDP) > 0 {
				ch <- info
			}
		}
	}()
	return ch
}

var (
	res = map[string]chan ConnectInfo{}
	mu  sync.RWMutex
)

func StartSignalServer(ctx context.Context, port int) error {
	http.Handle("/pull/", http.StripPrefix("/pull/", pullData()))
	http.Handle("/push/", http.StripPrefix("/push/", pushData()))

	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return fmt.Errorf("failed to bind to :%d: %w", port, err)
	}
	server := &http.Server{
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
	return server.Serve(l)
}

func pushData() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var info ConnectInfo
		if err := json.NewDecoder(r.Body).Decode(&info); err != nil {
			Log(r.Context()).Printf("json decode failed:", err)
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		mu.RLock()
		defer mu.RUnlock()
		select {
		default:
		case res[r.URL.Path] <- info:
		}
	})
}

func pullData() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ch := res[r.URL.Path]
		if ch == nil {
			ch = make(chan ConnectInfo)
			res[r.URL.Path] = ch
		}
		mu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		select {
		case <-ctx.Done():
			http.Error(w, ``, http.StatusRequestTimeout)
			return
		case v := <-ch:
			w.Header().Add("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(v); err != nil {
				Log(ctx).Printf("json encode failed:", err)
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}
		}
	})
}

func StartTurnServer(ctx context.Context, port int, addresses []string) error {
	ips := []net.IP{}
	if len(addresses) > 0 {
		for _, addr := range addresses {
			ip := net.ParseIP(addr)
			if ip == nil {
				return fmt.Errorf("provided address %q is invalid", addr)
			}
			ips = append(ips, ip)
		}
	} else {
		// No addresses specified so bind to all routable IPs
		ifaces, err := net.Interfaces()
		if err != nil {
			return fmt.Errorf("unable to read network interfaces: %w", err)
		}
		for _, i := range ifaces {
			addrs, err := i.Addrs()
			if err != nil {
				return fmt.Errorf("unable to read network interface: %w", err)
			}
			for _, a := range addrs {
				if v, ok := a.(*net.IPNet); ok {
					if i.Flags&net.FlagUp == 0 ||
						i.Flags&net.FlagPointToPoint != 0 ||
						v.IP.IsLinkLocalUnicast() {
						continue
					}
					ips = append(ips, v.IP)
				}
			}
		}
	}
	packetConfigs := []turn.PacketConnConfig{}
	listenerConfigs := []turn.ListenerConfig{}
	for _, ip := range ips {
		var lc net.ListenConfig
		udpListener, err := lc.ListenPacket(ctx, "udp", fmt.Sprintf("[%s]:%d", ip, port))
		if err != nil {
			return fmt.Errorf("failed to bind UDP %s:%d: %s", ip, port, err)
		}
		Log(ctx).Printf("Listening UDP [%s]:%d", ip, port)
		packetConfigs = append(packetConfigs, turn.PacketConnConfig{
			PacketConn: udpListener,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
				RelayAddress: ip,
				Address:      ip.String(),
			},
		})

		tcpListener, err := lc.Listen(ctx, "tcp", fmt.Sprintf("[%s]:%d", ip, port))
		if err != nil {
			return fmt.Errorf("failed to bind TCP %s:%d: %s", ip, port, err)
		}
		Log(ctx).Printf("Listening TCP [%s]:%d", ip, port)
		listenerConfigs = append(listenerConfigs, turn.ListenerConfig{
			Listener: tcpListener,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
				RelayAddress: ip,
				Address:      ip.String(),
			},
		})

	}

	s, err := turn.NewServer(turn.ServerConfig{
		PacketConnConfigs: packetConfigs,
		ListenerConfigs:   listenerConfigs,
	})
	if err != nil {
		return fmt.Errorf("failed to create TURN server: %w", err)
	}
	<-ctx.Done()
	return s.Close()
}

// Much of this code originated from https://github.com/nobonobo/ssh-p2p
// Copyright (c) 2018 irieda nobonobo
// MIT License: https://github.com/nobonobo/ssh-p2p/blob/master/LICENSE
