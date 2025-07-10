# yggdrasil-peerman

One day, I noticed that I suddenly have over 1Mb/s network traffic for no obvious reason. Investigation revealed that the culprit was [yggdrasil](https://github.com/yggdrasil-network/yggdrasil-go/):

My laptop -- while inside a LAN -- somehow became the shortest path between two public peers, and traffic had to go into my LAN, into my laptop, then *out of* my LAN and then on to destination. Not a very desirable situation when I have a router that is also on Yggdrasil. If there's a shorter path and I'm that shorter path, sure, ok, but a PC inside a LAN obviously isn't shorter.

The root cause of the problem was the fact that the router and my laptop, which connect to each other through local peer discovery, had differing lists of public peers. The correct solution for machines not having a public internet connection of their own is not to use any public peers, right?

But it is a laptop. It can, at times, connect through multiple networks, some of which have Yggdrasil routers I trust to have a connection to the rest of the network, and some don't. Editing the configuration depending on where I connect from is not ideal.

Fortunately, I don't have to, I can use the admin API of yggdrasil to do it for me, and this program is how.

## How it works

`yggdrasil-peerman` is meant to be run as a daemon. Upon reading the configuration and connecting to the Yggdrasil admin API endpoint, it drops privileges and sits there, checking the list of peers at regular configurable intervals.

It identifies whether any peers with specific public keys, the "trusted routers", are currently connected on link-local IP addresses, which it takes to mean that it's in a LAN with them. If so, it removes all the peers in a configured list from the current list of peers. If not so, it adds those peers back in. Peers not in this list remain untouched.

The logic here is that as long we are locally connected to a trusted router, we assume that it has a network connection and will route our abstract yggdrasil traffic for us, while if we don't see one, we should seek to connect to public peers.

## Configuration

**/etc/yggdrasil/peerman.yaml**:

```yaml
endpoint: <the admin API endpoint on your machine>
routers:
  - <key of a trusted router>
  - ...
peers:
  - <peers to toggle same as in yggdrasil.conf>
  - ...
# Loop time as time - 10s, 1m, etc.
looptime: 60s
```

To launch, the reasonable way is systemd:

**/etc/systemd/system/yggdrasil-peerman.service**:

```systemd
[Unit]
Description=Yggdrasil Network Peer Manager
Wants=network-online.target
Wants=yggdrasil.service
After=network-online.target
After=yggdrasil.service
ConditionPathExists=/var/run/yggdrasil

[Service]
Group=yggdrasil
ProtectSystem=strict
NoNewPrivileges=true
ReadWritePaths=/var/run/yggdrasil/ /run/yggdrasil/
SyslogIdentifier=yggdrasil-peerman
ExecStart=/usr/local/bin/yggdrasil-peerman -c /etc/yggdrasil/peerman.yaml
Restart=always
TimeoutStopSec=5

[Install]
WantedBy=multi-user.target
```

This is a bit arcane, at least for me -- please check the systemd manual and understand what each of the options means. `yggdrasil-peerman` needs to read its own config file, needs access to the admin API endpoint, which defaults to `unix:///var/run/yggdrasil/yggdrasil.sock` on Linux, and it needs nothing else whatsoever, so ideally systemd should limit it to that exactly.

## Limitations

Currenly only works on Linux, because I decided it needs to drop privileges after startup, and that is my narrow use case. In general, I believe this logic should be a feature of Yggdrasil itself, and this is just a solution to tide me over until it is. I still welcome pull requests and what not to make it more applicable to other environments.

## License

Due to this program cribbing bits and pieces from `yggdrasilctl`, it is licensed under the terms of GPLv3, which I *think* is the correct thing to do. Please advise if it isn't.
