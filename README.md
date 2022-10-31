`sshow` is a Go library to facilitate p2p ssh connections to help diagnose problems on coworkers computers when everyone is distributed and behind restrictive VPNs.
There are example commands that may be used directly in ./cmd although these will likely be to limited and you may want to reimplement them in your own projects.

This project uses and/or adapts various awesome projects:
  * https://github.com/pion/webrtc
  * https://github.com/pion/turn
  * https://github.com/nobonobo/ssh-p2p
  * https://github.com/gliderlabs/ssh

## WARNING: the code is a proof of concept, error handling needs to be cleaned up.

The provided command line can be installed like:
```
go install github.com/coryb/sshow/cmd/sshow@latest
```

### Enable inbound connections

The installed `sshow` tool can be useful for experimenting.  The "target" system where to allow incoming connections would run:
```
sshow server
```
```
2022/10/30 22:26:53 Session Key: ba9c92a1-27c6-4e3b-8b6e-3ebad7a44240
2022/10/30 22:26:53 starting ssh server on port 63085...
```

The session key will need to be shared with the client for the ssh to negotiate to the server.  The SSH server is started on the loopback network on a random available network port.

### Connecting to target

To connect to the target system you would use:
```
sshow ssh -key ba9c92a1-27c6-4e3b-8b6e-3ebad7a44240
```
```
2022/10/30 22:52:41 Client connect done
$ 
```
The shell is now on the remote target system. When you exit the bash terminal (`bash -l` is run by default on connection) then the client disconnect.

### Signal Broker

By default the `sshow server` and `sshow ssh` command use hosted projects (not affiliated with this project) for the STUN service and for the Signaling service.  The hosts used by default are: `stun.l.google.com:19302` for STUN and `https://nobo-signaling.appspot.com` for Signalling.  The later is maintained by the author of https://github.com/nobonobo/ssh-p2p.

If you want to host your own STUN and Signaling service, you can use use this project. 
When you run:
```
sshow broker
```
It will start a STUN/TURN service on port 3478 on all available IP addresses.  It will also start an HTTP service for Signaling on port 8080.

To use these services, you would run the `sshow server` on like:
```
sshow server --stun localhost:3478 --signal http://localhost:8080
```
And the client would run:
```
sshow ssh --stun localhost:3478 --signal http://localhost:8080 --key ba9c92a1-27c6-4e3b-8b6e-3ebad7a44240
```

This example is not terribly useful, as you can only connect to localhost from localhost. If the `sshow broker` command was run on a hosted server inside a corporate network, then your and your coworkers should be able to use it by using the hosted STUN and signal hosts.
